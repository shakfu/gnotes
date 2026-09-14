package store

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
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

	// Torn names the logs whose last record was incomplete, one event lost
	// each. See parseLog.
	Torn []string
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
		torn    bool
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

			ev, skipped, torn, err := parseLog(path)
			results[i] = parsed{events: ev, skipped: skipped, torn: torn, err: err}
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
	for i, r := range results {
		out.Events = append(out.Events, r.events...)
		for action, n := range r.skipped {
			if out.Skipped == nil {
				out.Skipped = make(map[event.Action]int)
			}
			out.Skipped[action] += n
		}
		if r.torn {
			out.Torn = append(out.Torn, filepath.Base(files[i]))
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
//
// A malformed record is fatal, with one exception: an unterminated last line
// that does not decode is a torn tail, left by a process that died mid-append.
// It is reported and dropped rather than refusing the project, because the
// alternative is that one crash makes every later command fail to open. Only
// the last line qualifies, and only when the file has no final newline, so a
// record damaged anywhere else is still refused. A last line that decodes is a
// whole record that lost only its newline, and is kept. Append settles the
// tail the same way before it writes again.
func parseLog(path string) ([]event.Event, map[event.Action]int, bool, error) {
	author, ok := ParseLogName(filepath.Base(path))
	if !ok {
		return nil, nil, false, fmt.Errorf("event log %s is not named after an author id", filepath.Base(path))
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, false, fmt.Errorf("read %s: %w", path, err)
	}
	unterminated := len(raw) > 0 && raw[len(raw)-1] != '\n'

	// Roughly 150 bytes per line in practice; a close guess avoids regrowing
	// the slice several times on a long log.
	events := make([]event.Event, 0, len(raw)/150+1)
	var skipped map[event.Action]int
	var torn bool

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
			if unterminated && rest == nil {
				torn = true
				continue
			}
			return nil, nil, false, fmt.Errorf("%s:%d: %w", filepath.Base(path), lineNo, err)
		}

		// The author is carried by the filename, not repeated on every line.
		e.UserID = author
		events = append(events, e)
	}

	return events, skipped, torn, nil
}

// Snapshot records the size and modification time of every event log, so a
// caller can notice that another process has written without reading the logs.
//
// Sizes and times are enough: the logs are append-only, so any change moves at
// least one of them. Hashing the contents would be more certain and would cost
// a full read on every check.
type Snapshot map[string]FileStamp

// FileStamp is what a Snapshot records for one log.
type FileStamp struct {
	Size    int64
	ModTime int64
}

// Snap takes a Snapshot of the project's event logs. A missing or unreadable
// directory yields an empty one.
func Snap(p *Project) Snapshot {
	entries, err := os.ReadDir(p.EventsDir())
	if err != nil {
		return Snapshot{}
	}

	snap := make(Snapshot, len(entries))
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != LogExt {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		snap[e.Name()] = FileStamp{Size: info.Size(), ModTime: info.ModTime().UnixNano()}
	}
	return snap
}

// Equal reports whether two snapshots describe the same logs.
func (s Snapshot) Equal(other Snapshot) bool { return maps.Equal(s, other) }

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
//
// It returns how many bytes the log grew by, net of any torn tail it removed,
// so a caller can tell its own write from another process's.
func Append(p *Project, actor Actor, events []event.Event) (int64, error) {
	if len(events) == 0 {
		return 0, nil
	}
	if !actor.Valid() {
		return 0, errors.New("cannot write events: the current user is not configured; run 'gnotes init'")
	}

	dir := p.EventsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, fmt.Errorf("create events directory: %w", err)
	}

	path := filepath.Join(dir, LogName(actor))
	terminate, removed, err := settleTail(path)
	if err != nil {
		return 0, err
	}

	// Build the whole batch first. A single write keeps a partially applied
	// operation off disk: either every event of a command lands or none does.
	var buf bytes.Buffer
	buf.Grow(len(events)*160 + 1)
	if terminate {
		// Part of the batch rather than a write of its own, so it still
		// arrives under O_APPEND and cannot split another process's append.
		buf.WriteByte('\n')
	}
	for _, e := range events {
		line, err := event.Encode(e)
		if err != nil {
			return 0, err
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", path, err)
	}

	if _, err := f.Write(buf.Bytes()); err != nil {
		f.Close()
		return 0, fmt.Errorf("append to %s: %w", path, err)
	}
	// No fsync. A command that returns is durable against a crashed gnotes,
	// not against a power cut: the write has reached the page cache, and what
	// protects the log across a lost machine is the git sync, not the disk
	// barrier. Paying an fsync per command would cost more than it protects.
	if err := f.Close(); err != nil {
		return 0, fmt.Errorf("close %s: %w", path, err)
	}
	return int64(buf.Len()) - removed, nil
}

// settleTail deals with a log whose last line has no newline, so that the next
// batch does not land behind it and fuse to it. A fused line is malformed in
// the interior of the file, which Load refuses for good, where an unterminated
// last line is something it can still work with.
//
// It answers the question the same way parseLog does. A tail that decodes is a
// whole record that lost only its newline; the caller writes that newline
// ahead of the batch and the record survives. A tail that does not decode is
// half a record and is truncated away.
//
// The cost on the normal path is a stat and a one-byte read. Truncating only
// while the file is still the size it was measured at keeps this from cutting
// off a concurrent append by the same author from another process; if one is
// in flight the repair is left for the next command.
func settleTail(path string) (terminate bool, removed int64, err error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, 0, nil
		}
		return false, 0, fmt.Errorf("stat %s: %w", path, err)
	}
	size := info.Size()
	if size == 0 {
		return false, 0, nil
	}

	f, err := os.Open(path)
	if err != nil {
		return false, 0, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	var last [1]byte
	if _, err := f.ReadAt(last[:], size-1); err != nil {
		return false, 0, fmt.Errorf("read %s: %w", path, err)
	}
	if last[0] == '\n' {
		return false, 0, nil
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return false, 0, fmt.Errorf("read %s: %w", path, err)
	}
	cut := int64(bytes.LastIndexByte(raw, '\n') + 1)
	if decodable(bytes.TrimSpace(raw[cut:])) {
		return true, 0, nil
	}

	if again, err := os.Stat(path); err != nil || again.Size() != size {
		return false, 0, nil
	}
	if err := os.Truncate(path, cut); err != nil {
		return false, 0, fmt.Errorf("discard the torn tail of %s: %w", path, err)
	}
	return false, size - cut, nil
}

// decodable reports whether a line is a whole record. An action this build does
// not implement still counts: the record is complete, only its meaning is not
// available here.
func decodable(line []byte) bool {
	if len(line) == 0 {
		return false
	}
	if _, err := event.Decode(line); err != nil {
		var unknown *event.ErrUnknownAction
		return errors.As(err, &unknown)
	}
	return true
}
