package wiki

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/shakfu/gwiki/internal/markdown"
)

// Edit is one link a move or fix rewrites.
type Edit struct {
	Page string `json:"page"` // the page edited, by its path after the move
	Line int    `json:"line"`
	Old  string `json:"old"`
	New  string `json:"new"`

	// Start and End bound Old in the page's source before the move.
	Start, End int `json:"-"`
}

// MovePlan is what renaming a page would change.
type MovePlan struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Edits []Edit `json:"edits"`

	// Unrewritten are links to or from the page that have no source position,
	// such as those under tab-indented list items. They are left as written.
	Unrewritten []Link `json:"unrewritten"`

	writes []fileWrite
}

// MoveResult is a completed move.
type MoveResult struct {
	MovePlan

	// Broken are links that were not broken before the move and are now, such
	// as a link whose target became ambiguous.
	Broken []Link `json:"broken"`
}

// pageSource is a page read for editing.
type pageSource struct {
	src   []byte
	hash  string
	spans []span
}

// PlanMove works out a rename of from to to without writing anything.
//
// Links to the page are rewritten in their own form. A wiki link that still
// resolves to the page, such as one by title, is left alone; otherwise it gets
// the page's new file name, or its path where the file name would be ambiguous,
// and a link that showed its target keeps showing the old text as its label. A
// markdown link gets the relative path from its page, and the moved page's own
// relative links are rewritten from its new directory.
func (w *Wiki) PlanMove(from, to string) (*MovePlan, error) {
	if _, err := w.Refresh(); err != nil {
		return nil, err
	}
	info, err := w.info(from)
	if err != nil {
		return nil, err
	}
	if to, err = CleanPath(to); err != nil {
		return nil, err
	}
	if to == from {
		return nil, errors.New("the page is already there")
	}
	if _, err := os.Stat(w.file(to)); err == nil {
		return nil, fmt.Errorf("%w: %s", ErrExists, to)
	}

	ix, err := w.readIndex()
	if err != nil {
		return nil, err
	}
	ix.removeEntry(from, info.Title)
	ix.addEntry(to, info.Title)
	ix.slugs[to] = ix.slugs[from]

	plan := &MovePlan{From: from, To: to, Edits: []Edit{}, Unrewritten: []Link{}}
	sources := map[string]*pageSource{}
	load := func(page string) (*pageSource, error) {
		if ps, ok := sources[page]; ok {
			return ps, nil
		}
		src, hash, err := w.readIndexed(page)
		if err != nil {
			return nil, err
		}
		ps := &pageSource{src: src, hash: hash}
		sources[page] = ps
		return ps, nil
	}
	seen := map[string]bool{}
	add := func(page, editPage string, l Link, ps *pageSource, repl string) {
		old := string(ps.src[l.DestStart:l.DestEnd])
		ps.spans = append(ps.spans, span{start: l.DestStart, end: l.DestEnd, old: old, new: repl})
		plan.Edits = append(plan.Edits, Edit{Page: editPage, Line: l.Line, Old: old, New: repl, Start: l.DestStart, End: l.DestEnd})
	}

	incoming, err := w.links(`resolved = ? AND kind IN ('page', 'heading') AND page != ?`, from, from)
	if err != nil {
		return nil, err
	}
	for _, l := range incoming {
		key := fmt.Sprint(l.Page, ":", l.DestStart)
		if seen[key] {
			continue
		}
		seen[key] = true
		if l.DestStart < 0 {
			plan.Unrewritten = append(plan.Unrewritten, l)
			continue
		}
		ps, err := load(l.Page)
		if err != nil {
			return nil, err
		}
		if err := checkDest(ps.src, l); err != nil {
			return nil, err
		}
		var repl string
		if l.Form == string(markdown.FormWiki) {
			var ok, changed bool
			repl, changed, ok = wikiRewrite(ix, ps.src, l, to)
			if !ok {
				plan.Unrewritten = append(plan.Unrewritten, l)
			}
			if !changed {
				continue
			}
		} else {
			repl = w.markdownDest(ps.src, l, l.Page, w.file(to))
		}
		add(l.Page, l.Page, l, ps, repl)
	}

	own, err := load(from)
	if err != nil {
		return nil, err
	}
	outgoing, err := w.links(`page = ? AND form = 'markdown' AND kind IN ('page', 'heading', 'file', 'line') AND status != ?`, from, StatusOutsideRepo)
	if err != nil {
		return nil, err
	}
	for _, l := range outgoing {
		key := fmt.Sprint(from, ":", l.DestStart)
		if seen[key] {
			continue
		}
		seen[key] = true
		if l.DestStart < 0 {
			plan.Unrewritten = append(plan.Unrewritten, l)
			continue
		}
		if err := checkDest(own.src, l); err != nil {
			return nil, err
		}
		if strings.HasPrefix(l.Target, "/") {
			continue // relative to the repository, not to the page
		}
		target := filepath.Join(w.Repo, filepath.FromSlash(l.Resolved))
		if l.Kind == KindPage || l.Kind == KindHeading {
			id := l.Resolved
			if id == from {
				id = to
			}
			target = w.file(id)
		}
		if repl := w.markdownDest(own.src, l, to, target); repl != string(own.src[l.DestStart:l.DestEnd]) {
			add(from, to, l, own, repl)
		}
	}

	for page, ps := range sources {
		out, err := applySpans(ps.src, ps.spans)
		if err != nil {
			return nil, err
		}
		if page == from {
			plan.writes = append(plan.writes, fileWrite{Page: to, Data: out}, fileWrite{Page: from, Base: ps.hash})
			continue
		}
		plan.writes = append(plan.writes, fileWrite{Page: page, Base: ps.hash, Data: out})
	}
	return plan, nil
}

// Move renames a page and rewrites the links to and from it; see PlanMove.
func (w *Wiki) Move(from, to string) (*MoveResult, error) {
	plan, err := w.PlanMove(from, to)
	if err != nil {
		return nil, err
	}
	info, err := w.info(plan.From)
	if err != nil {
		return nil, err
	}
	pages := []string{plan.To}
	for _, fw := range plan.writes {
		pages = append(pages, fw.Page)
	}
	before, err := w.brokenNear(pages, plan.From, plan.To, info.Title)
	if err != nil {
		return nil, err
	}
	if err := w.commit(plan.writes); err != nil {
		return nil, err
	}
	after, err := w.brokenNear(pages, plan.From, plan.To, info.Title)
	if err != nil {
		return nil, err
	}
	res := &MoveResult{MovePlan: *plan, Broken: []Link{}}
	was := map[string]int{}
	for _, l := range before {
		if l.Page == plan.From {
			l.Page = plan.To
		}
		was[l.Page+"\x00"+l.Written()+"\x00"+l.Status]++
	}
	for _, l := range after {
		k := l.Page + "\x00" + l.Written() + "\x00" + l.Status
		if was[k] > 0 {
			was[k]--
			continue
		}
		res.Broken = append(res.Broken, l)
	}
	return res, nil
}

// brokenNear returns the broken links a move between from and to can change:
// those on the pages written, and those whose target names either path or the
// page's title. It is the rule refresh uses to pick links to re-resolve, and
// reads far fewer rows than Broken in a wiki with many broken links.
func (w *Wiki) brokenNear(pages []string, from, to, title string) ([]Link, error) {
	list, err := json.Marshal(pages)
	if err != nil {
		return nil, err
	}
	lf, lt := strings.ToLower(from), strings.ToLower(to)
	return w.links(`status NOT IN ('ok', '') AND (page IN (SELECT value FROM json_each(?))
		OR kind IN ('page', 'heading') AND (resolved IN (?, ?)
			OR key_path IN (?, ?, ?) OR key_hyph IN (?, ?) OR key_stem IN (?, ?)))`,
		string(list), from, to, lf, lt, strings.ToLower(title), lf, lt, path.Base(lf), path.Base(lt))
}

// checkDest confirms a link's recorded offsets still hold its destination, so a
// stale cache cannot corrupt a page.
func checkDest(src []byte, l Link) error {
	if l.DestEnd > len(src) || l.DestStart > l.DestEnd {
		return fmt.Errorf("%s changed since it was indexed; run the command again", l.Page)
	}
	want := l.Target
	if l.Anchor != "" {
		want += "#" + l.Anchor
	}
	if got := string(src[l.DestStart:l.DestEnd]); strings.TrimSpace(got) != want {
		return fmt.Errorf("%s changed since it was indexed; run the command again", l.Page)
	}
	return nil
}

// wikiRewrite returns the replacement for a wiki link's destination so that it
// names to. changed is false when the link already resolves there; ok is false
// when no form of the target would resolve to it alone.
func wikiRewrite(ix *index, src []byte, l Link, to string) (repl string, changed, ok bool) {
	if got, status := ix.wiki(l.Target); status == StatusOK && got == to {
		return "", false, true
	}
	suffix := ""
	if strings.HasSuffix(strings.ToLower(l.Target), ".md") {
		suffix = ".md"
	}
	for _, c := range []string{path.Base(to) + suffix, to + suffix} {
		if got, status := ix.wiki(c); status == StatusOK && got == to {
			return wikiText(src, l, c), true, true
		}
	}
	return "", false, false
}

// wikiText is a wiki link destination of target, keeping the link's anchor,
// and keeping what it displayed when it had no label of its own.
func wikiText(src []byte, l Link, target string) string {
	text := target
	if l.Anchor != "" && !strings.Contains(target, "#") {
		text += "#" + l.Anchor
	}
	if l.DestEnd >= len(src) || src[l.DestEnd] != '|' {
		text += "|" + strings.TrimSpace(string(src[l.DestStart:l.DestEnd]))
	}
	return text
}

// markdownDest is a markdown link destination pointing at target, an absolute
// path, from a page, keeping the link's anchor and its repository-rooted style.
func (w *Wiki) markdownDest(src []byte, l Link, fromPage, target string) string {
	var dest string
	if strings.HasPrefix(l.Target, "/") {
		rel, _ := filepath.Rel(w.Repo, target)
		dest = "/" + filepath.ToSlash(rel)
	} else {
		rel, _ := filepath.Rel(filepath.Dir(w.file(fromPage)), target)
		dest = filepath.ToSlash(rel)
	}
	// Outside angle brackets a destination ends at a space or an unmatched
	// parenthesis.
	if l.DestStart == 0 || src[l.DestStart-1] != '<' {
		dest = strings.NewReplacer(" ", "%20", "(", "%28", ")", "%29").Replace(dest)
	}
	if l.Anchor != "" {
		dest += "#" + l.Anchor
	}
	return dest
}
