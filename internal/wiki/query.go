package wiki

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/shakfu/gnotes/internal/search"
)

// PageInfo is a page's summary.
type PageInfo struct {
	Path     string   `json:"path"`
	Title    string   `json:"title"`
	Type     string   `json:"type,omitempty"`
	Status   string   `json:"status,omitempty"`
	Priority string   `json:"priority,omitempty"`
	Due      string   `json:"due,omitempty"`
	Tags     []string `json:"tags"`
}

// File returns the page's path relative to the project root.
func (p PageInfo) File() string {
	return filepath.ToSlash(filepath.Join(DirName, PagesDir, p.Path+".md"))
}

// Heading is a heading in a page.
type Heading struct {
	Level int    `json:"level"`
	Text  string `json:"text"`
	Slug  string `json:"slug"`
	Line  int    `json:"line"`
}

// Link is a link as the cache records it.
type Link struct {
	Page     string `json:"page"`
	Line     int    `json:"line"`
	Col      int    `json:"col"`
	Form     string `json:"form"`
	Image    bool   `json:"image,omitempty"`
	Label    string `json:"label"`
	Target   string `json:"target"`
	Anchor   string `json:"anchor,omitempty"`
	Kind     string `json:"kind"`
	Resolved string `json:"resolved,omitempty"`
	Status   string `json:"status"`

	// Ref marks a reference-style link, whose destination is in the shared
	// definition. Start and End bound the whole link, and DestStart and
	// DestEnd the destination, in the page's source; each is -1 when it has no
	// position.
	Ref                bool `json:"-"`
	Start, End         int  `json:"-"`
	DestStart, DestEnd int  `json:"-"`
}

// Written renders the link's destination as it appears in the page.
func (l Link) Written() string {
	dest := l.Target
	if l.Anchor != "" {
		dest += "#" + l.Anchor
	}
	if l.Form == "wiki" {
		return "[[" + dest + "]]"
	}
	return dest
}

// Page is one page in full.
type Page struct {
	PageInfo
	Body      string    `json:"body"`
	Headings  []Heading `json:"headings"`
	Links     []Link    `json:"links"`
	Backlinks []Link    `json:"backlinks"`
	Tasks     []Task    `json:"tasks"`
}

// Hit is a search result.
type Hit struct {
	PageInfo
	Snippet string `json:"snippet"`
}

// Task is a checklist item or a task page.
type Task struct {
	Page     string `json:"page"`
	Line     int    `json:"line,omitempty"` // 0 for a task page
	Text     string `json:"text"`
	Status   string `json:"status"` // open, doing or done
	Priority string `json:"priority,omitempty"`
	Due      string `json:"due,omitempty"`

	// Box is the offset of a checklist item's box character, 0 for a task page.
	Box int `json:"-"`
}

// ErrAmbiguous lists the pages a reference could mean.
type ErrAmbiguous struct {
	Ref        string
	Candidates []PageInfo
}

func (e *ErrAmbiguous) Error() string {
	var names []string
	for _, c := range e.Candidates {
		names = append(names, fmt.Sprintf("%s (%s)", c.Path, c.Title))
	}
	return fmt.Sprintf("%q could mean %s", e.Ref, strings.Join(names, ", "))
}

const pageColumns = `path, title, type, status, priority, due,
	coalesce((SELECT group_concat(tag, ' ') FROM (SELECT tag FROM tags WHERE page = pages.path ORDER BY tag)), '')`

func scanPages(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close() error
}) ([]PageInfo, error) {
	defer rows.Close()
	out := []PageInfo{}
	for rows.Next() {
		var p PageInfo
		var tags string
		if err := rows.Scan(&p.Path, &p.Title, &p.Type, &p.Status, &p.Priority, &p.Due, &tags); err != nil {
			return nil, err
		}
		p.Tags = strings.Fields(tags)
		if p.Tags == nil {
			p.Tags = []string{}
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Pages lists pages under dir (all when empty), with tag when given, by path.
func (w *Wiki) Pages(dir, tag string) ([]PageInfo, error) {
	dir = strings.Trim(dir, "/")
	query := `SELECT ` + pageColumns + ` FROM pages WHERE (? = '' OR path LIKE ? ESCAPE '\')
		AND (? = '' OR path IN (SELECT page FROM tags WHERE tag = ?)) ORDER BY path`
	like := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(dir) + "/%"
	rows, err := w.db.Query(query, dir, like, tag, strings.ToLower(tag))
	if err != nil {
		return nil, err
	}
	return scanPages(rows)
}

// Find resolves a reference typed by a person or agent, trying in order: a
// path, with or without .md; a title; a file name, with spaces read as
// hyphens; a fragment of a path or title. All are case-insensitive. The first
// step that matches decides, and more than one match there is an error that
// lists them.
func (w *Wiki) Find(ref string) (PageInfo, error) {
	r := strings.ToLower(strings.TrimSuffix(strings.Trim(strings.TrimSpace(ref), "/"), ".md"))
	if r == "" {
		return PageInfo{}, errors.New("name a page")
	}
	like := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(r) + "%"
	for _, where := range []string{
		`lower(path) = ?1`,
		`lower(title) = ?1`,
		`stem = ?3`,
		`lower(path) LIKE ?2 ESCAPE '\' OR lower(title) LIKE ?2 ESCAPE '\'`,
	} {
		// Paths first, then the full rows for the few that match.
		rows, err := w.db.Query(`SELECT `+pageColumns+` FROM pages WHERE path IN (SELECT path FROM pages WHERE `+where+`) ORDER BY path`,
			r, like, strings.ReplaceAll(r, " ", "-"))
		if err != nil {
			return PageInfo{}, err
		}
		found, err := scanPages(rows)
		if err != nil {
			return PageInfo{}, err
		}
		switch len(found) {
		case 0:
			continue
		case 1:
			return found[0], nil
		default:
			return PageInfo{}, &ErrAmbiguous{Ref: ref, Candidates: found}
		}
	}
	return PageInfo{}, fmt.Errorf("no page matches %q", ref)
}

// Page returns a page in full.
func (w *Wiki) Page(p string) (Page, error) {
	rows, err := w.db.Query(`SELECT `+pageColumns+` FROM pages WHERE path = ?`, p)
	if err != nil {
		return Page{}, err
	}
	infos, err := scanPages(rows)
	if err != nil {
		return Page{}, err
	}
	if len(infos) == 0 {
		return Page{}, fmt.Errorf("no page %q", p)
	}
	out := Page{PageInfo: infos[0], Headings: []Heading{}}
	if err := w.db.QueryRow(`SELECT body FROM pages WHERE path = ?`, p).Scan(&out.Body); err != nil {
		return Page{}, err
	}

	if out.Headings, err = w.Headings(p); err != nil {
		return Page{}, err
	}

	if out.Links, err = w.links(`page = ?`, p); err != nil {
		return Page{}, err
	}
	if out.Backlinks, err = w.Backlinks(p); err != nil {
		return Page{}, err
	}
	out.Tasks, err = w.Tasks(TaskFilter{Page: p})
	return out, err
}

const linkColumns = `page, line, col, form, image, label, target, anchor, kind, resolved, status, ref, start, stop, dest_start, dest_stop`

func (w *Wiki) links(where string, args ...any) ([]Link, error) {
	rows, err := w.db.Query(`SELECT `+linkColumns+` FROM links WHERE `+where+` ORDER BY page, line, col`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Link{}
	for rows.Next() {
		var l Link
		if err := rows.Scan(&l.Page, &l.Line, &l.Col, &l.Form, &l.Image, &l.Label, &l.Target, &l.Anchor, &l.Kind, &l.Resolved, &l.Status,
			&l.Ref, &l.Start, &l.End, &l.DestStart, &l.DestEnd); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// Headings returns a page's headings in source order.
func (w *Wiki) Headings(p string) ([]Heading, error) {
	rows, err := w.db.Query(`SELECT level, text, slug, line FROM headings WHERE page = ? ORDER BY line`, p)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Heading{}
	for rows.Next() {
		var h Heading
		if err := rows.Scan(&h.Level, &h.Text, &h.Slug, &h.Line); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// Links returns a page's outgoing links in source order.
func (w *Wiki) Links(p string) ([]Link, error) { return w.links(`page = ?`, p) }

// Backlinks returns links from other pages that reach p, including those whose
// heading is missing.
func (w *Wiki) Backlinks(p string) ([]Link, error) {
	return w.links(`resolved = ? AND page != ? AND kind IN ('page', 'heading') AND status IN ('ok', 'missing-heading')`, p, p)
}

// Broken returns every link whose target is not found.
func (w *Wiki) Broken() ([]Link, error) {
	return w.links(`status NOT IN ('ok', '')`)
}

// Check re-examines every file and line link, since a file outside the wiki can
// change without any page changing, then returns every broken link.
func (w *Wiki) Check() ([]Link, error) {
	rows, err := w.db.Query(`SELECT rowid, kind, resolved, line_from, line_to, status FROM links WHERE kind IN ('file', 'line') AND status != ?`, StatusOutsideRepo)
	if err != nil {
		return nil, err
	}
	type update struct {
		rowid  int64
		status string
	}
	var updates []update
	lineCounts := map[string]int{}
	seen := map[classified]string{}
	for rows.Next() {
		var rowid int64
		var c classified
		if err := rows.Scan(&rowid, &c.kind, &c.resolved, &c.from, &c.to, &c.status); err != nil {
			rows.Close()
			return nil, err
		}
		old := c.status
		c.status = ""
		s, ok := seen[c]
		if !ok {
			s = fileStatus(filepath.Join(w.Repo, filepath.FromSlash(c.resolved)), c, lineCounts)
			seen[c] = s
		}
		if s != old {
			updates = append(updates, update{rowid, s})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(updates) > 0 {
		tx, err := w.db.Begin()
		if err != nil {
			return nil, err
		}
		for _, u := range updates {
			if _, err := tx.Exec(`UPDATE links SET status = ? WHERE rowid = ?`, u.status, u.rowid); err != nil {
				tx.Rollback()
				return nil, err
			}
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
	}
	return w.Broken()
}

// Orphans returns pages no other page links to.
func (w *Wiki) Orphans() ([]PageInfo, error) {
	rows, err := w.db.Query(`SELECT ` + pageColumns + ` FROM pages WHERE path NOT IN (
		SELECT resolved FROM links WHERE kind IN ('page', 'heading') AND status IN ('ok', 'missing-heading') AND resolved != page
	) ORDER BY path`)
	if err != nil {
		return nil, err
	}
	return scanPages(rows)
}

// Search returns pages matching query, best first. Every word must match and
// the last also by prefix. Among the best-scoring matches, a title containing
// the whole query ranks first, then BM25 with title, headings, tags and body
// weighted 10, 5, 3 and 1. Snippets mark matches with \x02 and \x03.
func (w *Wiki) Search(query string, limit int) ([]Hit, error) {
	match := search.Query(query)
	if match == "" {
		return []Hit{}, nil
	}
	if limit <= 0 {
		limit = -1
	}
	// Three steps, so the costly parts touch few rows: BM25 over the index
	// picks a pool; titles reorder the pool; snippets and tags are built for
	// the rows returned. A title that contains the whole query scores high on
	// title weight, so the pool holds it.
	pool := -1
	if limit > 0 {
		pool = max(200, limit*5)
	}
	rows, err := w.db.Query(`
		WITH pool AS (
			SELECT rowid AS num, bm25(pages_fts, 10.0, 5.0, 3.0, 1.0) AS score
			FROM pages_fts WHERE pages_fts MATCH ?1
			ORDER BY score LIMIT ?4
		), top AS (
			SELECT pool.num AS num, pool.score AS score, p.path AS path,
				instr(lower(p.title), lower(?2)) = 0 AS untitled
			FROM pool JOIN pages p ON p.num = pool.num
			ORDER BY untitled, score, path LIMIT ?3
		)
		SELECT `+pageColumnsFrom("p")+`, snippet(pages_fts, 3, char(2), char(3), '...', 16)
		FROM top JOIN pages_fts ON pages_fts.rowid = top.num JOIN pages p ON p.num = top.num
		WHERE pages_fts MATCH ?1
		ORDER BY top.untitled, top.score, top.path`, match, strings.TrimSpace(query), limit, pool)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Hit{}
	for rows.Next() {
		var h Hit
		var tags string
		if err := rows.Scan(&h.Path, &h.Title, &h.Type, &h.Status, &h.Priority, &h.Due, &tags, &h.Snippet); err != nil {
			return nil, err
		}
		h.Tags = strings.Fields(tags)
		if h.Tags == nil {
			h.Tags = []string{}
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func pageColumnsFrom(alias string) string {
	return fmt.Sprintf(`%[1]s.path, %[1]s.title, %[1]s.type, %[1]s.status, %[1]s.priority, %[1]s.due,
		coalesce((SELECT group_concat(tag, ' ') FROM (SELECT tag FROM tags WHERE page = %[1]s.path ORDER BY tag)), '')`, alias)
}

// TaskFilter narrows Tasks. Empty fields match everything.
type TaskFilter struct {
	Page   string
	Status string // open, doing or done
}

// Tasks lists checklist items and task pages, by page and line. A checklist
// item is open or done; a task page without a status is open.
func (w *Wiki) Tasks(f TaskFilter) ([]Task, error) {
	// Separate texts rather than "?1 = '' OR page = ?1", which stops SQLite
	// using the page index.
	items, pages := ``, `type = 'task'`
	args := []any{f.Status}
	if f.Page != "" {
		items, pages = `WHERE page = ?2`, `type = 'task' AND path = ?2`
		args = append(args, f.Page)
	}
	rows, err := w.db.Query(`
		SELECT * FROM (
			SELECT page, line, text, CASE done WHEN 1 THEN 'done' ELSE 'open' END AS status, '', due, box FROM checklist `+items+`
			UNION ALL
			SELECT path, 0, title, CASE status WHEN '' THEN 'open' ELSE status END, priority, due, 0 FROM pages WHERE `+pages+`
		) WHERE ?1 = '' OR status = ?1
		ORDER BY 1, 2`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Task{}
	for rows.Next() {
		var t Task
		if err := rows.Scan(&t.Page, &t.Line, &t.Text, &t.Status, &t.Priority, &t.Due, &t.Box); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Renames returns the renames refresh has detected, newest first.
func (w *Wiki) Renames() ([][2]string, error) {
	rows, err := w.db.Query(`SELECT old, new FROM renames ORDER BY rowid DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][2]string
	for rows.Next() {
		var r [2]string
		if err := rows.Scan(&r[0], &r[1]); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Dirs lists the directories that hold pages, for completion and listings.
func (w *Wiki) Dirs() ([]string, error) {
	pages, err := w.Pages("", "")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, p := range pages {
		for d := path.Dir(p.Path); d != "."; d = path.Dir(d) {
			seen[d] = true
		}
	}
	out := make([]string, 0, len(seen))
	for d := range seen {
		out = append(out, d)
	}
	sort.Strings(out)
	return out, nil
}
