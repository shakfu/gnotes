package store

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/shakfu/gwiki/internal/event"
	"github.com/shakfu/gwiki/internal/state"
	"github.com/shakfu/gwiki/internal/ulid"
)

// A legacy project is a .gwiki directory holding project.json and one JSONL
// event log per author under events/. It is imported into an empty database
// the first time it opens, and its files are left in place.
const (
	legacyConfigFile = "project.json"
	legacyEventsDir  = "events"

	// LogExt is the extension of a legacy event log.
	LogExt = ".jsonl"
)

// Import reports a legacy project brought into the database.
type Import struct {
	// From is the directory the logs were read from.
	From string

	// Events counts the events applied.
	Events int

	// Unapplied counts events replay rejected, and Unknown events naming an
	// action this build does not know. Neither reaches the database.
	Unapplied int
	Unknown   int

	// Torn names the logs whose incomplete last record was dropped.
	Torn []string
}

// importLegacy replays the legacy logs into the database, one event at a time,
// so each event's changes are recorded at its own time and author and the
// project's history survives. The import is one transaction, and a meta row
// marks it done, so it never runs twice, even into a database emptied since.
func (p *Project) importLegacy() error {
	if p.imported(p.db) {
		return nil
	}
	dir := filepath.Join(p.Root, DirName)
	raw, err := os.ReadFile(filepath.Join(dir, legacyConfigFile))
	if err != nil {
		return fmt.Errorf("read the legacy project config: %w", err)
	}
	var cfg struct {
		Config
		EventsRoot string `json:"eventsRoot"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return fmt.Errorf("parse %s: %w", filepath.Join(dir, legacyConfigFile), err)
	}
	from := filepath.Join(dir, cfg.EventsRoot, legacyEventsDir)
	if filepath.IsAbs(cfg.EventsRoot) {
		from = filepath.Join(cfg.EventsRoot, legacyEventsDir)
	}

	files, err := logFiles(from)
	if err != nil {
		return err
	}
	imp := &Import{From: from}
	var events []event.Event
	for _, path := range files {
		ev, unknown, torn, err := parseLog(path)
		if err != nil {
			return err
		}
		events = append(events, ev...)
		imp.Unknown += unknown
		if torn {
			imp.Torn = append(imp.Torn, filepath.Base(path))
		}
	}
	event.Sort(events)

	tx, err := p.db.Begin()
	if err != nil {
		return fmt.Errorf("write %s: %w", p.Path, err)
	}
	defer tx.Rollback()
	// Checked again under the write lock, so two processes opening the
	// project at once import it once.
	if p.imported(tx) {
		return nil
	}

	st, _ := state.Materialize(nil)
	for i := range events {
		e := &events[i]
		if err := st.ApplyUnsorted(e); err != nil {
			imp.Unapplied++
			continue
		}
		at, _ := ulid.Timestamp(e.ID)
		nodes, contributors := st.TakeChanged()
		if err := writeRows(tx, at, e.UserID, nodes, contributors); err != nil {
			return fmt.Errorf("import event %s into %s: %w", e.ID, p.Path, err)
		}
		imp.Events++
	}
	if err := writeConfig(tx, p.Path, cfg.Config); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO meta (k, v) VALUES ('legacy_import', ?)`, from); err != nil {
		return fmt.Errorf("write %s: %w", p.Path, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("write %s: %w", p.Path, err)
	}
	p.Imported = imp
	return nil
}

// imported reports whether the legacy logs were already imported. An
// unreadable database answers true, leaving the error to the load that follows.
func (p *Project) imported(q querier) bool {
	var one int
	err := q.QueryRow(`SELECT 1 FROM meta WHERE k = 'legacy_import'`).Scan(&one)
	return !errors.Is(err, sql.ErrNoRows)
}

// logFiles lists the event logs in dir, sorted by name. A missing directory
// holds no logs.
func logFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read the legacy events directory: %w", err)
	}

	var out []string
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != LogExt {
			continue
		}
		// An editor backup or a stray export is not named after an author.
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
// A malformed record is fatal, except an unterminated last line that does not
// decode: a process died mid-append, and it is reported as torn and dropped.
func parseLog(path string) (events []event.Event, unknown int, torn bool, err error) {
	author, _ := ParseLogName(path)
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, false, fmt.Errorf("read %s: %w", path, err)
	}
	unterminated := len(raw) > 0 && raw[len(raw)-1] != '\n'

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
			var u *event.ErrUnknownAction
			switch {
			case errors.As(err, &u):
				unknown++
				continue
			case unterminated && rest == nil:
				return events, unknown, true, nil
			default:
				return nil, 0, false, fmt.Errorf("%s:%d: %w", filepath.Base(path), lineNo, err)
			}
		}
		e.UserID = author
		events = append(events, e)
	}
	return events, unknown, false, nil
}

// ParseLogName recovers the author id from a legacy log filename: the id, a
// dot, then a sanitised display name.
func ParseLogName(base string) (id string, ok bool) {
	name := strings.TrimSuffix(filepath.Base(base), LogExt)
	if name == filepath.Base(base) {
		return "", false
	}
	// The id segment never contains a dot; the name may.
	if i := strings.IndexByte(name, '.'); i >= 0 {
		name = name[:i]
	}
	// A case-folding filesystem may have lowered the id.
	id = ulid.Canonical(name)
	if !ulid.Valid(id) {
		return "", false
	}
	return id, true
}
