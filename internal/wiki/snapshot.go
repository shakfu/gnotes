package wiki

import (
	"path/filepath"
	"strings"

	"github.com/shakfu/gnotes/internal/markdown"
)

// Snapshot is the page index at one moment. It resolves links in text that is
// not saved, such as an editor's buffer, the way a refresh resolves a page. It
// is not safe for concurrent use.
type Snapshot struct {
	w       *Wiki
	ix      *index
	renames [][2]string
	pages   []PageInfo
}

// Snapshot loads the index.
func (w *Wiki) Snapshot() (*Snapshot, error) {
	ix, err := w.readIndex()
	if err != nil {
		return nil, err
	}
	renames, err := w.Renames()
	if err != nil {
		return nil, err
	}
	pages, err := w.Pages("", "")
	if err != nil {
		return nil, err
	}
	return &Snapshot{w: w, ix: ix, renames: renames, pages: pages}, nil
}

// Pages lists every page as the snapshot saw them.
func (s *Snapshot) Pages() []PageInfo { return s.pages }

// Links parses src as a page's source and resolves its links. The page's own
// headings are taken from src, so an anchor to a heading added in src is found.
func (s *Snapshot) Links(page string, src []byte) []Link {
	pg := markdown.Parse(src)
	slugs := map[string]bool{}
	for _, h := range pg.Headings {
		slugs[h.Slug] = true
	}
	saved, had := s.ix.slugs[page]
	s.ix.slugs[page] = slugs
	defer func() {
		if had {
			s.ix.slugs[page] = saved
		} else {
			delete(s.ix.slugs, page)
		}
	}()

	lineCounts := map[string]int{}
	out := make([]Link, 0, len(pg.Links))
	for _, l := range pg.Links {
		c := s.w.classify(page, l, lineCounts)
		if c.kind == KindPage || c.kind == KindHeading {
			// Every page's headings are loaded, so resolve cannot fail.
			c.resolved, c.status, _ = s.ix.resolve(page, string(l.Form), l.Target, l.Anchor, c.resolved)
		}
		out = append(out, Link{Page: page, Line: l.Line, Col: l.Col, Form: string(l.Form), Image: l.Image,
			Label: l.Label, Target: l.Target, Anchor: l.Anchor, Kind: c.kind, Resolved: c.resolved, Status: c.status,
			Ref: l.Ref, Start: l.Start, End: l.End, DestStart: l.DestStart, DestEnd: l.DestEnd})
	}
	return out
}

// Offers returns repairs for a broken link in src; see Wiki.Offers.
func (s *Snapshot) Offers(l Link, src []byte) ([]Offer, error) {
	return s.w.offers(l, src, s.ix, s.renames)
}

// WikiTarget resolves a [[wiki]] target to a page path and a status.
func (s *Snapshot) WikiTarget(target string) (string, string) { return s.ix.wiki(target) }

// PageFile is the file holding a page.
func (w *Wiki) PageFile(page string) string { return w.file(page) }

// PageOf returns the page a file holds, and false for a file that is not a
// page: outside the pages directory, not markdown, or under a hidden
// directory.
func (w *Wiki) PageOf(file string) (string, bool) {
	rel, err := filepath.Rel(w.PagesPath(), file)
	if err != nil || !strings.HasSuffix(rel, ".md") {
		return "", false
	}
	id, err := CleanPath(rel)
	if err != nil {
		return "", false
	}
	return id, true
}
