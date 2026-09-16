package wiki

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"

	_ "modernc.org/sqlite"
)

// schemaVersion is PRAGMA user_version. Any other value, or a file SQLite
// cannot read, is deleted and rebuilt: the cache holds nothing the pages do not.
// 4: a directory's README is titled, found and linked by the directory.
// 5: a checklist item's text no longer holds its due: date.
const schemaVersion = 5

const schema = `
CREATE TABLE meta (k TEXT PRIMARY KEY, v TEXT NOT NULL);

CREATE TABLE files (
	path  TEXT PRIMARY KEY,
	size  INTEGER NOT NULL,
	mtime INTEGER NOT NULL,
	hash  TEXT NOT NULL
);

-- path is the page's identity: relative to the pages directory, without .md.
CREATE TABLE pages (
	num      INTEGER PRIMARY KEY,
	path     TEXT NOT NULL UNIQUE,
	title    TEXT NOT NULL,
	stem     TEXT NOT NULL,
	type     TEXT NOT NULL,
	status   TEXT NOT NULL,
	priority TEXT NOT NULL,
	due      TEXT NOT NULL,
	body     TEXT NOT NULL
);
CREATE INDEX pages_lower_path ON pages (lower(path));
CREATE INDEX pages_lower_title ON pages (lower(title));
CREATE INDEX pages_stem ON pages (stem);

CREATE TABLE headings (page TEXT NOT NULL, slug TEXT NOT NULL, text TEXT NOT NULL, level INTEGER NOT NULL, line INTEGER NOT NULL);
CREATE INDEX headings_page ON headings (page);

CREATE TABLE tags (page TEXT NOT NULL, tag TEXT NOT NULL);
CREATE INDEX tags_page ON tags (page);
CREATE INDEX tags_tag ON tags (tag);

CREATE TABLE assignees (page TEXT NOT NULL, who TEXT NOT NULL);
CREATE INDEX assignees_page ON assignees (page);

-- kind is page, heading, file, line or external. resolved is the target page's
-- path, or the repository-relative path of a file. The key columns hold a wiki
-- link's target normalised for each resolution rule, so a change to one page
-- re-resolves only the links that could name it.
CREATE TABLE links (
	page       TEXT NOT NULL,
	line       INTEGER NOT NULL,
	col        INTEGER NOT NULL,
	start      INTEGER NOT NULL,
	stop       INTEGER NOT NULL,
	dest_start INTEGER NOT NULL,
	dest_stop  INTEGER NOT NULL,
	form       TEXT NOT NULL,
	image      INTEGER NOT NULL,
	ref        INTEGER NOT NULL,
	label      TEXT NOT NULL,
	target     TEXT NOT NULL,
	anchor     TEXT NOT NULL,
	kind       TEXT NOT NULL,
	resolved   TEXT NOT NULL,
	line_from  INTEGER NOT NULL,
	line_to    INTEGER NOT NULL,
	status     TEXT NOT NULL,
	key_path   TEXT NOT NULL,
	key_hyph   TEXT NOT NULL,
	key_stem   TEXT NOT NULL
);
CREATE INDEX links_page ON links (page);
CREATE INDEX links_key_path ON links (key_path);
CREATE INDEX links_key_hyph ON links (key_hyph);
CREATE INDEX links_key_stem ON links (key_stem);
CREATE INDEX links_resolved ON links (resolved);

CREATE TABLE checklist (page TEXT NOT NULL, line INTEGER NOT NULL, text TEXT NOT NULL, done INTEGER NOT NULL, due TEXT NOT NULL, box INTEGER NOT NULL);
CREATE INDEX checklist_page ON checklist (page);

-- gone holds the content hash of removed pages, so a page that reappears
-- elsewhere with the same content, in this refresh or a later one, is recorded
-- in renames for link fixes to offer.
CREATE TABLE gone (path TEXT NOT NULL, hash TEXT NOT NULL);
CREATE INDEX gone_hash ON gone (hash);
CREATE TABLE renames (old TEXT NOT NULL, new TEXT NOT NULL);

CREATE VIRTUAL TABLE pages_fts USING fts5 (
	title, headings, tags, body,
	tokenize = 'unicode61 remove_diacritics 2'
);
`

// Wiki is an open project and its cache.
type Wiki struct {
	*Project
	db *sql.DB
}

// Open opens the cache for p, creating or rebuilding it as needed, and brings
// it up to date.
func Open(p *Project) (*Wiki, error) {
	w := &Wiki{Project: p}
	if err := w.connect(); err != nil {
		// Unreadable or from another schema: start again, once.
		w.discard()
		if err := w.connect(); err != nil {
			return nil, err
		}
	}
	if _, err := w.Refresh(); err != nil {
		w.Close()
		return nil, err
	}
	return w, nil
}

// Close releases the cache.
func (w *Wiki) Close() error {
	if w.db == nil {
		return nil
	}
	return w.db.Close()
}

// Rebuild deletes the cache and indexes every page again.
func (w *Wiki) Rebuild() error {
	w.discard()
	if err := w.connect(); err != nil {
		return err
	}
	_, err := w.Refresh()
	return err
}

func (w *Wiki) discard() {
	w.Close()
	w.db = nil
	for _, suffix := range []string{"", "-wal", "-shm"} {
		os.Remove(w.Cache() + suffix)
	}
}

func (w *Wiki) connect() error {
	q := url.Values{}
	q.Set("_txlock", "immediate")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	dsn := (&url.URL{Scheme: "file", OmitHost: true, Path: w.Cache(), RawQuery: q.Encode()}).String()

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return err
	}
	w.db = db

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("open %s: %w", w.Cache(), err)
	}
	defer tx.Rollback()
	var version int
	if err := tx.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("read %s: %w", w.Cache(), err)
	}
	switch version {
	case schemaVersion:
		return nil
	case 0:
		var tables int
		if err := tx.QueryRow(`SELECT count(*) FROM sqlite_schema`).Scan(&tables); err != nil {
			return err
		}
		if tables != 0 {
			return fmt.Errorf("%s holds tables but no schema version", w.Cache())
		}
	default:
		return fmt.Errorf("%s has schema %d, this build uses %d", w.Cache(), version, schemaVersion)
	}
	if _, err := tx.Exec(schema); err != nil {
		return fmt.Errorf("create %s: %w", w.Cache(), err)
	}
	if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, schemaVersion)); err != nil {
		return err
	}
	return tx.Commit()
}
