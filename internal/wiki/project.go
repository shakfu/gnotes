// Package wiki is a project's markdown wiki: pages under .gnotes/wiki, and a
// derived SQLite cache of their titles, headings, links, tags and tasks.
//
// The pages are the truth. The cache is rebuilt from them whenever it is
// missing, stale or unreadable, and every query brings it up to date first.
// See docs/dev/wiki-design.md.
package wiki

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Layout, relative to the project root.
const (
	DirName   = ".gnotes"
	PagesDir  = "wiki"
	CacheFile = "cache.db"

	configFile = "config.json"
)

// gitignored are the .gnotes entries that are local to a clone.
var gitignored = []string{CacheFile + "*", "drafts/"}

// ErrNotFound reports that no wiki exists at or above a directory.
var ErrNotFound = errors.New("no gnotes wiki found; run 'gnotes wiki init'")

// Config is .gnotes/config.json.
type Config struct {
	Name string `json:"name"`
}

// Project is a located wiki.
type Project struct {
	// Root holds .gnotes.
	Root string

	// Repo is the top of the enclosing git working tree, or Root outside one.
	// File links resolve within it.
	Repo string

	Config Config
}

// PagesPath returns the absolute pages directory.
func (p *Project) PagesPath() string { return filepath.Join(p.Root, DirName, PagesDir) }

// Cache returns the absolute cache path.
func (p *Project) Cache() string { return filepath.Join(p.Root, DirName, CacheFile) }

// Discover walks up from dir to the nearest directory holding .gnotes/wiki.
func Discover(dir string) (*Project, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	for {
		if info, err := os.Stat(filepath.Join(dir, DirName, PagesDir)); err == nil && info.IsDir() {
			return load(dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, ErrNotFound
		}
		dir = parent
	}
}

// Init creates a wiki in dir, or completes a partial one. It keeps an existing
// config and .gitignore lines.
func Init(dir string) (*Project, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	gn := filepath.Join(dir, DirName)
	if err := os.MkdirAll(filepath.Join(gn, PagesDir), 0o755); err != nil {
		return nil, fmt.Errorf("create %s: %w", filepath.Join(gn, PagesDir), err)
	}

	cfg := filepath.Join(gn, configFile)
	if _, err := os.Stat(cfg); os.IsNotExist(err) {
		raw, _ := json.MarshalIndent(Config{Name: filepath.Base(dir)}, "", "  ")
		if err := os.WriteFile(cfg, append(raw, '\n'), 0o644); err != nil {
			return nil, fmt.Errorf("write %s: %w", cfg, err)
		}
	}

	ignore := filepath.Join(gn, ".gitignore")
	raw, err := os.ReadFile(ignore)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("read %s: %w", ignore, err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	missing := ""
	for _, want := range gitignored {
		if !slices.Contains(lines, want) {
			missing += want + "\n"
		}
	}
	if missing != "" {
		if len(raw) > 0 && raw[len(raw)-1] != '\n' {
			missing = "\n" + missing
		}
		f, err := os.OpenFile(ignore, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return nil, fmt.Errorf("write %s: %w", ignore, err)
		}
		_, werr := f.WriteString(missing)
		if err := errors.Join(werr, f.Close()); err != nil {
			return nil, fmt.Errorf("write %s: %w", ignore, err)
		}
	}
	return load(dir)
}

func load(root string) (*Project, error) {
	p := &Project{Root: root, Repo: root}
	raw, err := os.ReadFile(filepath.Join(root, DirName, configFile))
	switch {
	case err == nil:
		if err := json.Unmarshal(raw, &p.Config); err != nil {
			return nil, fmt.Errorf("parse %s: %w", filepath.Join(root, DirName, configFile), err)
		}
	case !os.IsNotExist(err):
		return nil, err
	}
	if p.Config.Name == "" {
		p.Config.Name = filepath.Base(root)
	}
	for dir := root; ; {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			p.Repo = dir
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return p, nil
}
