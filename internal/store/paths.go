// Package store owns everything on disk: locating a project, reading its
// configuration, and reading and appending the per-author event logs.
//
// Logs are split one file per author rather than shared. That is the single
// decision that makes git the sync layer instead of a source of conflicts: two
// people working offline append to different files, so a merge is a union of
// paths and never a textual conflict. Ordering the union back into one
// sequence is the event package's job, not git's.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shakfu/gnotes/internal/ulid"
)

// Layout constants. The directory is committed alongside code by default, so
// notes travel with the repository they describe.
const (
	// DirName is the project directory, found by walking up from the working
	// directory the way git finds .git.
	DirName = ".gnotes"

	// ConfigFile names the project descriptor inside DirName.
	ConfigFile = "project.json"

	// EventsDirName holds the per-author logs.
	EventsDirName = "events"

	// LogExt is the extension of an event log.
	LogExt = ".jsonl"
)

// ErrNotFound reports that no project exists at or above the starting
// directory.
var ErrNotFound = errors.New("no gnotes project found; run 'gnotes init'")

// ErrExists reports an attempt to initialise a project where one already is.
var ErrExists = errors.New("a gnotes project already exists here")

// Config is the persisted project descriptor.
type Config struct {
	// Name is a human label for the project, shown in the interface.
	Name string `json:"name"`

	// EventsRoot relocates the event logs. It is empty by default, meaning the
	// logs live inside the project directory. It exists so the logs can later
	// be moved into a worktree on a separate branch, keeping notes out of code
	// diffs, without any change to the loading or replay code: both only ever
	// see a resolved path. A relative value is interpreted against the project
	// directory.
	EventsRoot string `json:"eventsRoot,omitempty"`

	// Created is when the project was initialised, RFC 3339.
	Created string `json:"created"`
}

// Project is a located, loaded project on disk.
type Project struct {
	// Dir is the absolute path of the project directory.
	Dir string

	Config Config
}

// EventsDir returns the absolute directory holding the per-author logs.
func (p *Project) EventsDir() string {
	if p.Config.EventsRoot == "" {
		return filepath.Join(p.Dir, EventsDirName)
	}
	if filepath.IsAbs(p.Config.EventsRoot) {
		return filepath.Join(p.Config.EventsRoot, EventsDirName)
	}
	return filepath.Join(p.Dir, p.Config.EventsRoot, EventsDirName)
}

// ConfigPath returns the absolute path of the project descriptor.
func (p *Project) ConfigPath() string {
	return filepath.Join(p.Dir, ConfigFile)
}

// Root returns the directory containing the project directory, which is the
// repository root in the default layout.
func (p *Project) Root() string {
	return filepath.Dir(p.Dir)
}

// Discover walks up from startDir looking for a project directory holding a
// readable descriptor, and returns the nearest one.
//
// The presence of the directory alone is not enough: an empty .gnotes left
// behind by a partial checkout would otherwise shadow a real project further
// up the tree.
func Discover(startDir string) (*Project, error) {
	dir, err := filepath.Abs(startDir)
	if err != nil {
		return nil, fmt.Errorf("resolve %q: %w", startDir, err)
	}

	for {
		candidate := filepath.Join(dir, DirName)
		if info, err := os.Stat(filepath.Join(candidate, ConfigFile)); err == nil && info.Mode().IsRegular() {
			return Open(candidate)
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, ErrNotFound
		}
		dir = parent
	}
}

// Open loads the project whose directory is dir.
func Open(dir string) (*Project, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve %q: %w", dir, err)
	}

	raw, err := os.ReadFile(filepath.Join(abs, ConfigFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("read project config: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", filepath.Join(abs, ConfigFile), err)
	}
	return &Project{Dir: abs, Config: cfg}, nil
}

// Init creates a project directory under root and writes its descriptor.
// It refuses to overwrite an existing project.
func Init(root, name string, now time.Time) (*Project, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve %q: %w", root, err)
	}
	dir := filepath.Join(abs, DirName)

	if _, err := os.Stat(filepath.Join(dir, ConfigFile)); err == nil {
		return nil, ErrExists
	}
	if name == "" {
		name = filepath.Base(abs)
	}

	p := &Project{
		Dir:    dir,
		Config: Config{Name: name, Created: now.UTC().Format(time.RFC3339)},
	}
	if err := os.MkdirAll(p.EventsDir(), 0o755); err != nil {
		return nil, fmt.Errorf("create project directories: %w", err)
	}
	if err := p.writeConfig(); err != nil {
		return nil, err
	}
	return p, nil
}

// writeConfig persists the descriptor via a temporary file and a rename, so a
// crash mid-write leaves the previous descriptor intact rather than a
// truncated one that would make the project unopenable.
func (p *Project) writeConfig() error {
	raw, err := json.MarshalIndent(p.Config, "", "  ")
	if err != nil {
		return fmt.Errorf("encode project config: %w", err)
	}
	raw = append(raw, '\n')

	tmp, err := os.CreateTemp(p.Dir, ConfigFile+".*")
	if err != nil {
		return fmt.Errorf("create temp config: %w", err)
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp config: %w", err)
	}
	if err := os.Rename(tmp.Name(), p.ConfigPath()); err != nil {
		return fmt.Errorf("install config: %w", err)
	}
	return nil
}

// Actor identifies whoever is appending events.
type Actor struct {
	// ID is a ULID minted once per user and kept in the global configuration.
	// It is the stable identity: the display name can change freely without
	// splitting a person's history in two.
	ID string

	// Name is the display name at the time of writing.
	Name string
}

// Valid reports whether the actor can own a log file.
func (a Actor) Valid() bool {
	return ulid.Valid(a.ID) && strings.TrimSpace(a.Name) != "" && len(a.Name) <= 80
}

// LogName returns the log filename for an actor: the id, a dot, then a
// sanitised display name.
//
// The name is in the filename purely so a directory listing is readable; the
// id is what identifies the author. Only the first dot separates the two, so a
// name may contain as many as it likes.
func LogName(a Actor) string {
	return sanitize(a.ID) + "." + sanitize(a.Name) + LogExt
}

// ParseLogName recovers the author id from a log filename.
//
// Only the id is returned. The name segment has been lowercased and stripped
// of anything awkward, so reconstructing a display name from it would produce
// a subtly wrong one; the authoritative name comes from the contributor
// registry in the log itself.
func ParseLogName(base string) (id string, ok bool) {
	name := strings.TrimSuffix(filepath.Base(base), LogExt)
	if name == filepath.Base(base) {
		return "", false
	}

	// Split on the first dot: the id segment never contains one, the name may.
	if i := strings.IndexByte(name, '.'); i >= 0 {
		name = name[:i]
	}

	// Some filesystems fold case. Restoring the canonical ULID casing is
	// lossless and keeps one person from appearing as two authors.
	id = ulid.Canonical(name)
	if !ulid.Valid(id) {
		return "", false
	}
	return id, true
}

// sanitize reduces a string to characters that are safe in a filename on every
// platform gnotes runs on, and lowercases it so that a case-folding filesystem
// cannot produce two names for one file.
func sanitize(s string) string {
	var b strings.Builder
	b.Grow(len(s))

	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z':
			b.WriteByte(c + 32)
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_', c == '.':
			b.WriteByte(c)
		default:
			b.WriteByte('-')
		}
	}

	out := b.String()
	if out == "" {
		return "unknown"
	}
	return out
}
