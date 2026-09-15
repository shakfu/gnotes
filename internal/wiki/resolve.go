package wiki

import (
	"bytes"
	"database/sql"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/shakfu/gnotes/internal/markdown"
)

// Link kinds.
const (
	KindPage     = "page"
	KindHeading  = "heading"
	KindFile     = "file"
	KindLine     = "line"
	KindExternal = "external"
)

// Link statuses.
const (
	StatusOK             = "ok"
	StatusMissingPage    = "missing-page"
	StatusAmbiguous      = "ambiguous"
	StatusMissingHeading = "missing-heading"
	StatusMissingFile    = "missing-file"
	StatusLineOutOfRange = "line-out-of-range"
	StatusOutsideRepo    = "outside-repo"
)

var (
	scheme    = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*:`)
	lineRange = regexp.MustCompile(`^L(\d+)(?:-L(\d+))?$`)
	escaped   = regexp.MustCompile(`\\([[:punct:]])`)
)

type classified struct {
	kind, resolved, status string
	from, to               int
}

// classify decides a link's kind from where it points. File and external links
// get their status here; page links are resolved against every page afterwards,
// by resolvePages.
func (w *Wiki) classify(page string, l markdown.Link, lineCounts map[string]int) classified {
	switch {
	case l.Form == markdown.FormAuto:
		return classified{kind: KindExternal, resolved: l.Target, status: StatusOK}
	case l.Form == markdown.FormWiki:
		if l.Anchor != "" {
			return classified{kind: KindHeading}
		}
		return classified{kind: KindPage}
	}

	target := l.Target
	switch {
	case scheme.MatchString(target) || strings.HasPrefix(target, "//"):
		return classified{kind: KindExternal, resolved: target, status: StatusOK}
	case target == "" && l.Anchor != "":
		return classified{kind: KindHeading, resolved: page}
	case target == "":
		return classified{kind: KindExternal, status: StatusOK}
	}

	target = escaped.ReplaceAllString(target, "$1")
	if i := strings.IndexByte(target, '?'); i >= 0 {
		target = target[:i]
	}
	if dec, err := url.PathUnescape(target); err == nil {
		target = dec
	}
	var abs string
	if strings.HasPrefix(target, "/") {
		abs = filepath.Join(w.Repo, filepath.FromSlash(target))
	} else {
		abs = filepath.Join(w.PagesPath(), filepath.FromSlash(path.Dir(page)), filepath.FromSlash(target))
	}
	rel, err := filepath.Rel(w.Repo, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return classified{kind: KindFile, resolved: target, status: StatusOutsideRepo}
	}

	if inPages, err := filepath.Rel(w.PagesPath(), abs); err == nil && !strings.HasPrefix(inPages, "..") && strings.HasSuffix(inPages, ".md") {
		c := classified{kind: KindPage, resolved: pageID(filepath.ToSlash(inPages))}
		if l.Anchor != "" {
			c.kind = KindHeading
		}
		return c
	}

	c := classified{kind: KindFile, resolved: filepath.ToSlash(rel)}
	if m := lineRange.FindStringSubmatch(l.Anchor); m != nil {
		c.kind = KindLine
		c.from, _ = strconv.Atoi(m[1])
		c.to = c.from
		if m[2] != "" {
			c.to, _ = strconv.Atoi(m[2])
		}
	}
	c.status = fileStatus(abs, c, lineCounts)
	return c
}

// fileStatus checks a file target. A directory is a valid target, as on GitHub.
func fileStatus(abs string, c classified, lineCounts map[string]int) string {
	info, err := os.Stat(abs)
	if err != nil {
		return StatusMissingFile
	}
	if c.kind != KindLine || info.IsDir() {
		return StatusOK
	}
	n, ok := lineCounts[abs]
	if !ok {
		raw, err := os.ReadFile(abs)
		if err != nil {
			return StatusMissingFile
		}
		n = bytes.Count(raw, []byte("\n"))
		if len(raw) > 0 && raw[len(raw)-1] != '\n' {
			n++
		}
		lineCounts[abs] = n
	}
	if max(c.from, c.to) > n || c.from < 1 {
		return StatusLineOutOfRange
	}
	return StatusOK
}

// index is every page, keyed the ways a link can name one. Headings are read
// per page when a link needs them.
type index struct {
	tx      *sql.Tx
	exact   map[string]bool     // path as stored
	byPath  map[string][]string // lowercase path
	byTitle map[string][]string // lowercase title
	byStem  map[string][]string // lowercase file name
	slugs   map[string]map[string]bool
}

// addEntry puts a page into the index by path and title, with no headings.
func (ix *index) addEntry(id, title string) {
	stem := strings.ToLower(path.Base(id))
	ix.exact[id] = true
	ix.byPath[strings.ToLower(id)] = append(ix.byPath[strings.ToLower(id)], id)
	ix.byTitle[strings.ToLower(title)] = append(ix.byTitle[strings.ToLower(title)], id)
	ix.byStem[stem] = append(ix.byStem[stem], id)
	if ix.slugs[id] == nil {
		ix.slugs[id] = map[string]bool{}
	}
}

// removeEntry takes a page out of the index.
func (ix *index) removeEntry(id, title string) {
	drop := func(m map[string][]string, k string) {
		m[k] = slices.DeleteFunc(slices.Clone(m[k]), func(v string) bool { return v == id })
	}
	delete(ix.exact, id)
	drop(ix.byPath, strings.ToLower(id))
	drop(ix.byTitle, strings.ToLower(title))
	drop(ix.byStem, strings.ToLower(path.Base(id)))
}

// add puts a page parsed in this refresh into the index, headings included.
func (ix *index) add(id string, pg *markdown.Page) {
	title := pg.Title
	if title == "" {
		title = path.Base(id)
	}
	stem := strings.ToLower(path.Base(id))
	ix.exact[id] = true
	ix.byPath[strings.ToLower(id)] = append(ix.byPath[strings.ToLower(id)], id)
	ix.byTitle[strings.ToLower(title)] = append(ix.byTitle[strings.ToLower(title)], id)
	ix.byStem[stem] = append(ix.byStem[stem], id)
	slugs := map[string]bool{}
	for _, h := range pg.Headings {
		slugs[h.Slug] = true
	}
	ix.slugs[id] = slugs
}

// resolve returns a page or heading link's target and status. resolved is
// the page path a markdown link names, empty for a wiki link.
func (ix *index) resolve(page, form, target, anchor, resolved string) (string, string, error) {
	status := StatusOK
	switch {
	case form == string(markdown.FormWiki) && target == "":
		resolved = page
	case form == string(markdown.FormWiki):
		resolved, status = ix.wiki(target)
	case !ix.exact[resolved]:
		status = StatusMissingPage
	}
	if status == StatusOK && anchor != "" {
		ok, err := ix.heading(resolved, anchor)
		if err != nil {
			return "", "", err
		}
		if !ok {
			status = StatusMissingHeading
		}
	}
	return resolved, status, nil
}

func loadIndex(tx *sql.Tx) (*index, error) {
	ix := &index{tx: tx, exact: map[string]bool{}, byPath: map[string][]string{}, byTitle: map[string][]string{},
		byStem: map[string][]string{}, slugs: map[string]map[string]bool{}}
	rows, err := tx.Query(`SELECT path, title, stem FROM pages`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p, title, stem string
		if err := rows.Scan(&p, &title, &stem); err != nil {
			return nil, err
		}
		ix.exact[p] = true
		ix.byPath[strings.ToLower(p)] = append(ix.byPath[strings.ToLower(p)], p)
		ix.byTitle[strings.ToLower(title)] = append(ix.byTitle[strings.ToLower(title)], p)
		ix.byStem[stem] = append(ix.byStem[stem], p)
	}
	return ix, rows.Err()
}

// wikiKeys normalises a [[wiki]] target for the three resolution rules: as
// written, lowercased, for a path or title; with spaces as hyphens, for a path;
// and the last element of that, for a file name.
func wikiKeys(target string) (asWritten, hyphened, stem string) {
	t := strings.TrimPrefix(strings.TrimSuffix(strings.ToLower(strings.TrimSpace(target)), ".md"), "/")
	h := strings.ReplaceAll(t, " ", "-")
	return t, h, path.Base(h)
}

// wiki resolves a [[wiki]] target: by path, then title, then file name, each
// case-insensitive. The first rule that matches anything decides; more than one
// match there is ambiguous.
func (ix *index) wiki(target string) (string, string) {
	t, h, stem := wikiKeys(target)
	for _, matches := range [][]string{
		union(ix.byPath[path.Clean(t)], ix.byPath[path.Clean(h)]),
		ix.byTitle[t],
		ix.byStem[stem],
	} {
		switch len(matches) {
		case 0:
			continue
		case 1:
			return matches[0], StatusOK
		default:
			return "", StatusAmbiguous
		}
	}
	return "", StatusMissingPage
}

// heading reports whether page has a heading an anchor names: its GitHub slug,
// or text that slugs to it.
func (ix *index) heading(page, anchor string) (bool, error) {
	s, ok := ix.slugs[page]
	if !ok && ix.tx == nil {
		return false, nil // an index loaded whole has every page's headings
	}
	if !ok {
		s = map[string]bool{}
		rows, err := ix.tx.Query(`SELECT slug FROM headings WHERE page = ?`, page)
		if err != nil {
			return false, err
		}
		for rows.Next() {
			var slug string
			if err := rows.Scan(&slug); err != nil {
				rows.Close()
				return false, err
			}
			s[slug] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return false, err
		}
		ix.slugs[page] = s
	}
	a := strings.ToLower(anchor)
	if dec, err := url.PathUnescape(a); err == nil {
		a = dec
	}
	return s[a] || s[markdown.Slug(anchor)], nil
}

func union(a, b []string) []string {
	out := slices.Clone(a)
	for _, x := range b {
		if !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	return out
}

// resolve re-resolves links on pages this refresh did not write whose target
// names a page it added, changed or removed, by path, title or file name. Links
// on written pages were resolved as they were inserted. Only rows whose result
// differs are written.
func (wr *writer) resolve() error {
	tx := wr.tx
	if _, err := tx.Exec(`CREATE TEMP TABLE IF NOT EXISTS affected (kind TEXT NOT NULL, k TEXT NOT NULL)`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM affected`); err != nil {
		return err
	}
	ins, err := tx.Prepare(`INSERT INTO affected (kind, k) VALUES (?, ?)`)
	if err != nil {
		return err
	}
	for kind, keys := range map[string]map[string]bool{"id": wr.ids, "path": wr.paths, "title": wr.titles, "stem": wr.stems} {
		for k := range keys {
			if _, err := ins.Exec(kind, k); err != nil {
				ins.Close()
				return err
			}
		}
	}
	ins.Close()

	rows, err := tx.Query(`SELECT rowid, page, form, target, anchor, resolved, status FROM links
		WHERE kind IN ('page', 'heading') AND page NOT IN (SELECT k FROM affected WHERE kind = 'id') AND (
			resolved IN (SELECT k FROM affected WHERE kind = 'id')
			OR key_path IN (SELECT k FROM affected WHERE kind IN ('path', 'title'))
			OR key_hyph IN (SELECT k FROM affected WHERE kind = 'path')
			OR key_stem IN (SELECT k FROM affected WHERE kind = 'stem'))`)
	if err != nil {
		return err
	}
	type link struct {
		rowid                                        int64
		page, form, target, anchor, resolved, status string
	}
	var links []link
	for rows.Next() {
		var l link
		if err := rows.Scan(&l.rowid, &l.page, &l.form, &l.target, &l.anchor, &l.resolved, &l.status); err != nil {
			rows.Close()
			return err
		}
		links = append(links, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(links) == 0 {
		return err
	}

	update, err := tx.Prepare(`UPDATE links SET resolved = ?, status = ? WHERE rowid = ?`)
	if err != nil {
		return err
	}
	defer update.Close()
	for _, l := range links {
		resolved := l.resolved
		if l.form == string(markdown.FormWiki) {
			resolved = ""
		}
		resolved, status, err := wr.ix.resolve(l.page, l.form, l.target, l.anchor, resolved)
		if err != nil {
			return err
		}
		if resolved != l.resolved || status != l.status {
			if _, err := update.Exec(resolved, status, l.rowid); err != nil {
				return err
			}
		}
	}
	return nil
}
