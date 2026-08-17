package store

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"

	"github.com/shakfu/gnotes/internal/event"
)

// LoadResult is the outcome of reading every log in a project.
type LoadResult struct {
	// Events are all events from all authors, in canonical replay order.
	Events []event.Event

	// Skipped counts events naming actions this build does not implement,
	// keyed by action. They were written by a newer gnotes and are left
	// untouched on disk, so upgrading applies them.
	Skipped map[event.Action]int
}

// Load reads and merges every author's log.
//
// Each file is parsed on its own goroutine. The work is almost entirely JSON
// decoding, which parallelises cleanly because the files are independent by
// construction; only the final ordering needs the whole set.
func Load(p *Project) (LoadResult, error) {
	files, err := logFiles(p.EventsDir())
	if err != nil {
		return LoadResult{}, err
	}
	if len(files) == 0 {
		return LoadResult{}, nil
	}

	type parsed struct {
		events  []event.Event
		skipped map[event.Action]int
		err     error
	}
	results := make([]parsed, len(files))

	// One goroutine per file, capped so a project with many collaborators does
	// not spawn an unbounded number of parsers competing for the same disk.
	limit := runtime.GOMAXPROCS(0)
	if limit > len(files) {
		limit = len(files)
	}
	sem := make(chan struct{}, limit)

	var wg sync.WaitGroup
	for i, path := range files {
		wg.Add(1)
		go func(i int, path string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			ev, skipped, err := parseLog(path)
			results[i] = parsed{events: ev, skipped: skipped, err: err}
		}(i, path)
	}
	wg.Wait()

	total := 0
	for _, r := range results {
		if r.err != nil {
			return LoadResult{}, r.err
		}
		total += len(r.events)
	}

	out := LoadResult{Events: make([]event.Event, 0, total)}
	for _, r := range results {
		out.Events = append(out.Events, r.events...)
		for action, n := range r.skipped {
			if out.Skipped == nil {
				out.Skipped = make(map[event.Action]int)
			}
			out.Skipped[action] += n
		}
	}

	event.Sort(out.Events)
	return out, nil
}

// logFiles lists the event logs in dir, sorted by name so that a load is
// reproducible before ordering even runs. A missing directory is not an error:
// a project with no events yet is a valid empty project.
func logFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read events directory: %w", err)
	}

	var out []string
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != LogExt {
			continue
		}
		// A file whose name is not an author id is not ours. Skipping it keeps
		// an editor backup or a stray export from being replayed as events.
		if _, ok := ParseLogName(e.Name()); !ok {
			continue
		}
		out = append(out, filepath.Join(dir, e.Name()))
	}
	sort.Strings(out)
	return out, nil
}

// parseLog reads one author's log.
//
// The whole file is read at once rather than streamed. A log is proportional
// to the project's edit history, not its data, so it stays in the low
// megabytes; one read beats a syscall per buffer, and it lets each line be
// decoded from a subslice with no copying.
func parseLog(path string) ([]event.Event, map[event.Action]int, error) {
	author, ok := ParseLogName(filepath.Base(path))
	if !ok {
		return nil, nil, fmt.Errorf("event log %s is not named after an author id", filepath.Base(path))
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("read %s: %w", path, err)
	}

	// Roughly 150 bytes per line in practice; a close guess avoids regrowing
	// the slice several times on a long log.
	events := make([]event.Event, 0, len(raw)/150+1)
	var skipped map[event.Action]int

	for lineNo, rest := 1, raw; len(rest) > 0; lineNo++ {
		var line []byte
		if i := bytes.IndexByte(rest, '\n'); i >= 0 {
			line, rest = rest[:i], rest[i+1:]
		} else {
			line, rest = rest, nil
		}

		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}

		e, err := event.Decode(line)
		if err != nil {
			var unknown *event.ErrUnknownAction
			if errors.As(err, &unknown) {
				// Written by a newer gnotes. Step over it rather than refusing
				// the whole log; the line stays on disk for a later upgrade.
				if skipped == nil {
					skipped = make(map[event.Action]int)
				}
				skipped[unknown.Action]++
				continue
			}
			return nil, nil, fmt.Errorf("%s:%d: %w", filepath.Base(path), lineNo, err)
		}

		// The author is carried by the filename, not repeated on every line.
		e.UserID = author
		events = append(events, e)
	}

	return events, skipped, nil
}

// Append writes events to the actor's log as one contiguous batch.
//
// Concurrent appends are safe without a lock. Each author writes only their
// own file, so two gnotes processes belonging to different people never touch
// the same path, and two processes belonging to the same person append under
// O_APPEND, which the kernel serialises against other appends to that file.
//
// Two processes racing may both read the same edge reference and so both
// branch from it. That is not a fault to prevent: it is exactly the situation
// two machines syncing produce, and ordering resolves it the same way.
func Append(p *Project, actor Actor, events []event.Event) error {
	if len(events) == 0 {
		return nil
	}
	if !actor.Valid() {
		return errors.New("cannot write events: the current user is not configured; run 'gnotes init'")
	}

	dir := p.EventsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create events directory: %w", err)
	}

	// Build the whole batch first. A single write keeps a partially applied
	// operation off disk: either every event of a command lands or none does.
	var buf bytes.Buffer
	buf.Grow(len(events) * 160)
	for _, e := range events {
		line, err := event.Encode(e)
		if err != nil {
			return err
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}

	path := filepath.Join(dir, LogName(actor))
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}

	if _, err := f.Write(buf.Bytes()); err != nil {
		f.Close()
		return fmt.Errorf("append to %s: %w", path, err)
	}
	// Durability is git's job here, not ours: the log is committed and pushed,
	// and a torn tail from a crashed process is recovered by the same sync
	// that recovers from a lost machine. Paying an fsync per command would
	// cost more than it protects.
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	return nil
}
