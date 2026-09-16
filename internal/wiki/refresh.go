package wiki

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"hash/fnv"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/shakfu/gwiki/internal/markdown"
)

// Changes reports what a refresh found.
type Changes struct {
	Added, Modified, Removed []string
	Renamed                  [][2]string // old path, new path
}

// Empty reports whether nothing changed.
func (c Changes) Empty() bool {
	return len(c.Added)+len(c.Modified)+len(c.Removed) == 0
}

type fileStat struct {
	rel         string // relative to the pages directory, slash-separated, with .md
	size, mtime int64
}

type parsed struct {
	fileStat
	hash string
	src  []byte
	page *markdown.Page
	err  error
}

// Refresh brings the cache up to date with the pages on disk.
//
// Pages are compared by size and modification time; only those that differ are
// read. A fingerprint of every page's name, size and time answers the common
// case, nothing changed, without reading the file table. A write transaction is
// taken only when something changed.
func (w *Wiki) Refresh() (Changes, error) {
	var ch Changes
	found, err := w.scan()
	if err != nil {
		return ch, err
	}
	print := fingerprint(found)
	if stored, err := storedFingerprint(w.db); err != nil || stored == print {
		return ch, err
	}

	// Under the write lock from here: another process may have indexed the
	// same change while this one waited, and the file table read must be the
	// one this refresh writes against.
	tx, err := w.db.Begin()
	if err != nil {
		return ch, fmt.Errorf("write %s: %w", w.Cache(), err)
	}
	defer tx.Rollback()
	if stored, err := storedFingerprint(tx); err != nil || stored == print {
		return ch, err
	}

	known, err := knownFiles(tx)
	if err != nil {
		return ch, err
	}
	var todo []fileStat
	for _, f := range found {
		k, ok := known[f.rel]
		switch {
		case !ok:
			ch.Added = append(ch.Added, pageID(f.rel))
			todo = append(todo, f)
		case k.size != f.size || k.mtime != f.mtime:
			ch.Modified = append(ch.Modified, pageID(f.rel))
			todo = append(todo, f)
		}
		delete(known, f.rel)
	}
	for rel := range known {
		ch.Removed = append(ch.Removed, pageID(rel))
	}
	sort.Strings(ch.Added)
	sort.Strings(ch.Modified)
	sort.Strings(ch.Removed)

	pages := w.parseAll(todo)
	for _, p := range pages {
		if p.err != nil {
			return ch, p.err
		}
	}

	wr, err := w.newWriter(tx)
	if err != nil {
		return ch, err
	}
	defer wr.close()

	for _, id := range ch.Removed {
		if err := wr.forget(id); err != nil {
			return ch, err
		}
		if _, err := tx.Exec(`INSERT INTO gone (path, hash) VALUES (?, ?)`, id, known[id+".md"].hash); err != nil {
			return ch, err
		}
	}
	// Bounded: only recent removals are worth offering as renames.
	if _, err := tx.Exec(`DELETE FROM gone WHERE rowid <= (SELECT max(rowid) FROM gone) - 1000`); err != nil {
		return ch, err
	}

	added := map[string]bool{}
	for _, id := range ch.Added {
		added[id] = true
	}
	for _, id := range ch.Modified {
		if err := wr.forget(id); err != nil {
			return ch, err
		}
	}
	// The index is every page as it will be after this refresh, so each new
	// link is written with its final status.
	if wr.ix, err = loadIndex(tx); err != nil {
		return ch, err
	}
	for _, p := range pages {
		wr.ix.add(pageID(p.rel), p.page)
	}
	for _, p := range pages {
		id := pageID(p.rel)
		if err := wr.insert(p); err != nil {
			return ch, fmt.Errorf("index %s: %w", p.rel, err)
		}
		if !added[id] {
			continue
		}
		var old string
		switch err := tx.QueryRow(`SELECT path FROM gone WHERE hash = ? AND path != ? ORDER BY rowid DESC LIMIT 1`, p.hash, id).Scan(&old); err {
		case nil:
			ch.Renamed = append(ch.Renamed, [2]string{old, id})
			if _, err := tx.Exec(`INSERT INTO renames (old, new) VALUES (?, ?)`, old, id); err != nil {
				return ch, err
			}
			if _, err := tx.Exec(`DELETE FROM gone WHERE path = ?`, old); err != nil {
				return ch, err
			}
		case sql.ErrNoRows:
		default:
			return ch, err
		}
	}

	if err := wr.resolve(); err != nil {
		return ch, err
	}
	if _, err := tx.Exec(`INSERT OR REPLACE INTO meta (k, v) VALUES ('fingerprint', ?)`, print); err != nil {
		return ch, err
	}
	if err := tx.Commit(); err != nil {
		return ch, fmt.Errorf("write %s: %w", w.Cache(), err)
	}
	return ch, nil
}

// fingerprint summarises every page's name, size and modification time. The
// per-file hashes are summed, so the order of the scan does not matter.
func fingerprint(files []fileStat) string {
	var sum uint64
	var buf [16]byte
	for _, f := range files {
		h := fnv.New64a()
		h.Write([]byte(f.rel))
		for i := 0; i < 8; i++ {
			buf[i] = byte(f.size >> (8 * i))
			buf[8+i] = byte(f.mtime >> (8 * i))
		}
		h.Write(buf[:])
		sum += h.Sum64()
	}
	return strconv.Itoa(len(files)) + ":" + strconv.FormatUint(sum, 16)
}

// scan lists every page with its size and modification time. Directories are
// listed in one goroutine and files are stat'ed on a pool, which measured three
// times faster than filepath.WalkDir on 5,000 pages. Hidden entries are skipped.
func (w *Wiki) scan() ([]fileStat, error) {
	root := w.PagesPath()
	var rels []string
	dirs := []string{""}
	for len(dirs) > 0 {
		dir := dirs[len(dirs)-1]
		dirs = dirs[:len(dirs)-1]
		entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(dir)))
		if err != nil {
			if dir == "" && os.IsNotExist(err) {
				return nil, ErrNotFound
			}
			return nil, err
		}
		for _, e := range entries {
			name := e.Name()
			if strings.HasPrefix(name, ".") {
				continue
			}
			rel := path.Join(dir, name)
			switch {
			case e.IsDir():
				dirs = append(dirs, rel)
			case strings.HasSuffix(name, ".md"):
				rels = append(rels, rel)
			}
		}
	}

	out := make([]fileStat, len(rels))
	ok := make([]bool, len(rels))
	parallel(len(rels), func(i int) {
		info, err := os.Stat(filepath.Join(root, filepath.FromSlash(rels[i])))
		if err != nil || !info.Mode().IsRegular() {
			return
		}
		out[i], ok[i] = fileStat{rel: rels[i], size: info.Size(), mtime: info.ModTime().UnixNano()}, true
	})
	kept := out[:0]
	for i := range out {
		if ok[i] {
			kept = append(kept, out[i])
		}
	}
	return kept, nil
}

type knownFile struct {
	size, mtime int64
	hash        string
}

func storedFingerprint(q interface {
	QueryRow(string, ...any) *sql.Row
}) (string, error) {
	var v string
	if err := q.QueryRow(`SELECT v FROM meta WHERE k = 'fingerprint'`).Scan(&v); err != nil && err != sql.ErrNoRows {
		return "", err
	}
	return v, nil
}

func knownFiles(tx *sql.Tx) (map[string]knownFile, error) {
	rows, err := tx.Query(`SELECT path, size, mtime, hash FROM files`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]knownFile{}
	for rows.Next() {
		var rel string
		var k knownFile
		if err := rows.Scan(&rel, &k.size, &k.mtime, &k.hash); err != nil {
			return nil, err
		}
		out[rel] = k
	}
	return out, rows.Err()
}

func (w *Wiki) parseAll(files []fileStat) []parsed {
	out := make([]parsed, len(files))
	parallel(len(files), func(i int) {
		p := parsed{fileStat: files[i]}
		p.src, p.err = os.ReadFile(filepath.Join(w.PagesPath(), filepath.FromSlash(files[i].rel)))
		if p.err == nil {
			sum := sha256.Sum256(p.src)
			p.hash = hex.EncodeToString(sum[:])
			p.page = markdown.Parse(p.src)
		}
		out[i] = p
	})
	return out
}

// parallel runs fn for 0..n-1 on up to 16 goroutines, each taking indices from
// a shared counter.
func parallel(n int, fn func(int)) {
	workers := min(runtime.GOMAXPROCS(0), 16, n)
	if workers <= 1 {
		for i := 0; i < n; i++ {
			fn(i)
		}
		return
	}
	var next atomic.Int64
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := int(next.Add(1) - 1); i < n; i = int(next.Add(1) - 1) {
				fn(i)
			}
		}()
	}
	wg.Wait()
}

func pageID(rel string) string { return strings.TrimSuffix(rel, ".md") }

// writer holds one refresh's prepared statements, and the keys of the pages it
// touched, which decide the links to re-resolve.
type writer struct {
	w          *Wiki
	tx         *sql.Tx
	stmts      map[string]*sql.Stmt
	lineCounts map[string]int

	// Lowercase paths, titles and stems, and exact paths, of every page added,
	// changed or removed, before and after the change.
	paths, titles, stems, ids map[string]bool

	// ix resolves page links as they are inserted.
	ix *index
}

var writerStatements = map[string]string{
	"page":       `SELECT num, title, stem FROM pages WHERE path = ?`,
	"del fts":    `DELETE FROM pages_fts WHERE rowid = ?`,
	"del page":   `DELETE FROM pages WHERE path = ?`,
	"del head":   `DELETE FROM headings WHERE page = ?`,
	"del tags":   `DELETE FROM tags WHERE page = ?`,
	"del assign": `DELETE FROM assignees WHERE page = ?`,
	"del links":  `DELETE FROM links WHERE page = ?`,
	"del check":  `DELETE FROM checklist WHERE page = ?`,
	"del file":   `DELETE FROM files WHERE path = ?`,
	"ins file":   `INSERT INTO files (path, size, mtime, hash) VALUES (?, ?, ?, ?)`,
	"ins page":   `INSERT INTO pages (path, title, stem, type, status, priority, due, body) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
	"ins head":   `INSERT INTO headings (page, slug, text, level, line) VALUES (?, ?, ?, ?, ?)`,
	"ins tag":    `INSERT INTO tags (page, tag) VALUES (?, ?)`,
	"ins assign": `INSERT INTO assignees (page, who) VALUES (?, ?)`,
	"ins fts":    `INSERT INTO pages_fts (rowid, title, headings, tags, body) VALUES (?, ?, ?, ?, ?)`,
	"ins link": `INSERT INTO links (page, line, col, start, stop, dest_start, dest_stop, form, image, ref,
			label, target, anchor, kind, resolved, line_from, line_to, status, key_path, key_hyph, key_stem)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
	"ins check": `INSERT INTO checklist (page, line, text, done, due, box) VALUES (?, ?, ?, ?, ?, ?)`,
}

func (w *Wiki) newWriter(tx *sql.Tx) (*writer, error) {
	wr := &writer{w: w, tx: tx, stmts: map[string]*sql.Stmt{}, lineCounts: map[string]int{},
		paths: map[string]bool{}, titles: map[string]bool{}, stems: map[string]bool{}, ids: map[string]bool{}}
	for name, q := range writerStatements {
		s, err := tx.Prepare(q)
		if err != nil {
			wr.close()
			return nil, err
		}
		wr.stmts[name] = s
	}
	return wr, nil
}

func (wr *writer) close() {
	for _, s := range wr.stmts {
		s.Close()
	}
}

func (wr *writer) exec(name string, args ...any) error {
	_, err := wr.stmts[name].Exec(args...)
	return err
}

func (wr *writer) touch(id, title, stem string) {
	wr.ids[id] = true
	wr.paths[strings.ToLower(id)] = true
	wr.titles[strings.ToLower(title)] = true
	wr.stems[stem] = true
}

// forget deletes every row of a page, remembering its keys.
func (wr *writer) forget(id string) error {
	var num int64
	var title, stem string
	switch err := wr.stmts["page"].QueryRow(id).Scan(&num, &title, &stem); err {
	case nil:
		wr.touch(id, title, stem)
		if err := wr.exec("del fts", num); err != nil {
			return err
		}
	case sql.ErrNoRows:
	default:
		return err
	}
	for _, name := range []string{"del page", "del head", "del tags", "del assign", "del links", "del check"} {
		if err := wr.exec(name, id); err != nil {
			return err
		}
	}
	return wr.exec("del file", id+".md")
}

func (wr *writer) insert(p parsed) error {
	id := pageID(p.rel)
	pg := p.page
	f := pg.Front
	if f == nil {
		f = &markdown.Front{}
	}
	title := pg.Title
	if title == "" {
		title = path.Base(id)
	}
	stem := strings.ToLower(path.Base(id))
	wr.touch(id, title, stem)
	body := string(p.src[pg.BodyStart:])

	if err := wr.exec("ins file", p.rel, p.size, p.mtime, p.hash); err != nil {
		return err
	}
	res, err := wr.stmts["ins page"].Exec(id, title, stem, strings.ToLower(f.Type), strings.ToLower(f.Status),
		strings.ToLower(f.Priority), f.Due, body)
	if err != nil {
		return err
	}
	num, err := res.LastInsertId()
	if err != nil {
		return err
	}

	var headings []string
	for _, h := range pg.Headings {
		headings = append(headings, h.Text)
		if err := wr.exec("ins head", id, h.Slug, h.Text, h.Level, h.Line); err != nil {
			return err
		}
	}
	var tags []string
	for _, t := range f.Tags {
		if t = strings.ToLower(strings.TrimSpace(t)); t != "" {
			tags = append(tags, t)
			if err := wr.exec("ins tag", id, t); err != nil {
				return err
			}
		}
	}
	for _, a := range f.Assignees {
		if err := wr.exec("ins assign", id, strings.TrimSpace(a)); err != nil {
			return err
		}
	}
	if err := wr.exec("ins fts", num, title, strings.Join(headings, "\n"), strings.Join(tags, " "), body); err != nil {
		return err
	}

	for _, l := range pg.Links {
		c := wr.w.classify(id, l, wr.lineCounts)
		if c.kind == KindPage || c.kind == KindHeading {
			if c.resolved, c.status, err = wr.ix.resolve(id, string(l.Form), l.Target, l.Anchor, c.resolved); err != nil {
				return err
			}
		}
		var kp, kh, ks string
		if l.Form == markdown.FormWiki && l.Target != "" {
			kp, kh, ks = wikiKeys(l.Target)
		}
		if err := wr.exec("ins link", id, l.Line, l.Col, l.Start, l.End, l.DestStart, l.DestEnd, string(l.Form), l.Image, l.Ref,
			l.Label, l.Target, l.Anchor, c.kind, c.resolved, c.from, c.to, c.status, kp, kh, ks); err != nil {
			return err
		}
	}
	for _, t := range pg.Tasks {
		if err := wr.exec("ins check", id, t.Line, t.Text, t.Done, t.Due, t.Box); err != nil {
			return err
		}
	}
	return nil
}
