// Package store owns everything on disk: locating a project, and reading and
// writing its SQLite database.
//
// The database is .gnotes/gnotes.db, in the working tree, for the user to
// commit. Its tables hold the notes themselves; a changes table, filled by
// triggers, records every edit, including edits made with other SQLite
// clients. Projects that predate the database are imported from their JSONL
// logs the first time they open; see legacy.go.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shakfu/gnotes/internal/ulid"
)

// Layout constants.
const (
	// DirName is the project directory, found by walking up from the working
	// directory the way git finds .git.
	DirName = ".gnotes"

	// DBFile names the database inside DirName.
	DBFile = "gnotes.db"

	// gitignore keeps the rollback journal, which exists only during a write,
	// out of commits.
	gitignore = "gnotes.db-journal\n"

	// gitattributes names a diff driver for the database. git shows a binary
	// diff until the driver is configured; see the README.
	gitattributes = "gnotes.db diff=gnotes\n"
)

// ErrNotFound reports that no project exists at or above the starting
// directory.
var ErrNotFound = errors.New("no gnotes project found; run 'gnotes init'")

// ErrExists reports an attempt to initialise a project where one already is.
var ErrExists = errors.New("a gnotes project already exists here")

// Config describes a project. It is stored in the database's meta table.
type Config struct {
	// Name is a human label for the project, shown in the interface.
	Name string `json:"name"`

	// Created is when the project was initialised, RFC 3339.
	Created string `json:"created"`
}

// Project is a located, open project.
type Project struct {
	// Root is the directory holding .gnotes.
	Root string

	// Path is the database file.
	Path string

	Config Config

	// Imported is set when opening the project imported legacy JSONL logs.
	Imported *Import

	db *sql.DB
}

// Close releases the database.
func (p *Project) Close() error { return p.db.Close() }

// Discover walks up from startDir and opens the nearest project.
func Discover(startDir string) (*Project, error) {
	dir, err := filepath.Abs(startDir)
	if err != nil {
		return nil, fmt.Errorf("resolve %q: %w", startDir, err)
	}
	for {
		p, err := OpenAt(dir)
		if !errors.Is(err, ErrNotFound) {
			return p, err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, ErrNotFound
		}
		dir = parent
	}
}

// OpenAt opens the project rooted exactly at root, importing a legacy project
// found there. It returns ErrNotFound when root holds neither.
func OpenAt(root string) (*Project, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve %q: %w", root, err)
	}
	path := filepath.Join(root, DirName, DBFile)
	legacy := isFile(filepath.Join(root, DirName, legacyConfigFile))
	if !legacy && !isFile(path) {
		return nil, ErrNotFound
	}

	p, err := open(root, path, legacy)
	if err != nil {
		return nil, err
	}
	if legacy {
		if err := p.importLegacy(); err != nil {
			p.Close()
			return nil, err
		}
	}
	if err := p.readConfig(); err != nil {
		p.Close()
		return nil, err
	}
	return p, nil
}

// Init creates a project rooted at root. It refuses when one is already there.
func Init(root, name string, now time.Time) (*Project, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve %q: %w", root, err)
	}
	if isFile(filepath.Join(abs, DirName, DBFile)) || isFile(filepath.Join(abs, DirName, legacyConfigFile)) {
		return nil, ErrExists
	}
	if name == "" {
		name = filepath.Base(abs)
	}

	p, err := open(abs, filepath.Join(abs, DirName, DBFile), true)
	if err != nil {
		return nil, err
	}
	p.Config = Config{Name: name, Created: now.UTC().Format(time.RFC3339)}
	if err := p.writeConfig(); err != nil {
		p.Close()
		os.Remove(p.Path)
		return nil, err
	}
	return p, nil
}

// GitRoot returns the nearest directory at or above dir that has a .git entry.
func GitRoot(dir string) (string, bool) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", false
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// Actor identifies whoever is writing.
type Actor struct {
	// ID is a ULID minted once per user and kept in the global configuration.
	// It is the stable identity: the display name can change freely without
	// splitting a person's history in two.
	ID string

	// Name is the display name at the time of writing.
	Name string
}

// Valid reports whether the actor can write.
func (a Actor) Valid() bool {
	return ulid.Valid(a.ID) && strings.TrimSpace(a.Name) != "" && len(a.Name) <= 80
}
