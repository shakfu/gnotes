package wiki

import (
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/shakfu/gwiki/internal/markdown"
)

// Offer is one way to repair a broken link.
type Offer struct {
	// Label says what the fix does.
	Label string `json:"label"`

	// New replaces the link's destination.
	New string `json:"new"`
}

const maxOffers = 5

// Offers returns ranked repairs for a broken link, best first. None is applied.
func (w *Wiki) Offers(l Link) ([]Offer, error) {
	all, err := w.OffersFor([]Link{l})
	if err != nil {
		return nil, err
	}
	return all[0], nil
}

// OffersFor returns the repairs for each of several broken links, reading the
// page index once rather than once per link.
func (w *Wiki) OffersFor(links []Link) ([][]Offer, error) {
	var ix *index
	var renames [][2]string
	out := make([][]Offer, len(links))
	for i, l := range links {
		if ix == nil && (l.Status == StatusMissingPage || l.Status == StatusAmbiguous) {
			var err error
			if ix, err = w.readIndex(); err != nil {
				return nil, err
			}
			if renames, err = w.Renames(); err != nil {
				return nil, err
			}
		}
		if l.DestStart < 0 {
			out[i] = []Offer{}
			continue
		}
		src, _, err := w.Read(l.Page)
		if err != nil {
			return nil, err
		}
		offers, err := w.offers(l, src, ix, renames)
		if err != nil {
			return nil, err
		}
		out[i] = offers
	}
	return out, nil
}

// offers is Offers for a link in src, with the index and renames loaded; ix is
// needed only for a missing or ambiguous page.
func (w *Wiki) offers(l Link, src []byte, ix *index, renames [][2]string) ([]Offer, error) {
	out := []Offer{}
	if l.DestStart < 0 {
		return out, nil
	}
	if err := checkDest(src, l); err != nil {
		return nil, err
	}
	wiki := l.Form == string(markdown.FormWiki)

	// pageOffer names a page in the link's own form.
	pageOffer := func(id, why string) Offer {
		if !wiki {
			return Offer{Label: why, New: w.markdownDest(src, l, l.Page, w.file(id), false)}
		}
		// The title when it names only this page, else the path.
		target := id
		if title := originalTitle(w, id); title != "" {
			if got, status := ix.wiki(title); status == StatusOK && got == id {
				target = title
			}
		}
		text := target
		if l.Anchor != "" {
			text += "#" + l.Anchor
		}
		return Offer{Label: why, New: text}
	}

	switch l.Status {
	case StatusMissingPage, StatusAmbiguous:
		var ids []string
		why := map[string]string{}
		note := func(id, reason string) {
			if _, ok := why[id]; !ok && ix.exact[id] {
				why[id] = reason
				ids = append(ids, id)
			}
		}

		if l.Status == StatusAmbiguous {
			for _, id := range ix.candidates(l.Target) {
				note(id, "the page at "+id)
			}
		}

		key := l.Resolved
		if wiki {
			key, _, _ = wikiKeys(l.Target)
		}
		for _, r := range renames {
			if r[0] == key || strings.ToLower(r[0]) == key || strings.ToLower(path.Base(r[0])) == path.Base(strings.ReplaceAll(key, " ", "-")) {
				note(r[1], "renamed from "+r[0])
			}
		}

		for _, id := range similarPages(ix, key) {
			note(id, "similar name: "+id)
		}
		for _, id := range ids {
			out = append(out, pageOffer(id, why[id]))
		}

	case StatusMissingHeading:
		target := src
		if l.Resolved != l.Page {
			var err error
			if target, _, err = w.Read(l.Resolved); err != nil {
				return out, nil
			}
		}
		want := markdown.Slug(l.Anchor)
		heads := markdown.Parse(target).Headings
		sort.SliceStable(heads, func(i, j int) bool {
			return editDistance(heads[i].Slug, want) < editDistance(heads[j].Slug, want)
		})
		base := l.Target
		for _, h := range heads {
			anchor := h.Slug
			if wiki {
				anchor = h.Text
			}
			out = append(out, Offer{Label: "heading " + h.Text, New: base + "#" + anchor})
		}

	case StatusMissingFile, StatusLineOutOfRange:
		if l.Status == StatusLineOutOfRange {
			out = append(out, Offer{Label: "drop the line anchor", New: l.Target})
			break
		}
		for _, found := range w.findFiles(path.Base(l.Resolved)) {
			out = append(out, Offer{Label: "file at " + found, New: w.markdownDest(src, l, l.Page, filepath.Join(w.Repo, filepath.FromSlash(found)), false)})
		}
	}

	if len(out) > maxOffers {
		out = out[:maxOffers]
	}
	return out, nil
}

// Fix rewrites a broken link's destination with an offer.
func (w *Wiki) Fix(l Link, o Offer) error {
	src, hash, err := w.Read(l.Page)
	if err != nil {
		return err
	}
	if err := checkDest(src, l); err != nil {
		return err
	}
	out, err := applySpans(src, []span{{start: l.DestStart, end: l.DestEnd, old: string(src[l.DestStart:l.DestEnd]), new: FixText(l, src, o)}})
	if err != nil {
		return err
	}
	return w.Write(l.Page, out, hash)
}

// FixText is what replaces a link's destination in src to apply an offer. A
// wiki link that showed its target keeps showing it, as its label.
func FixText(l Link, src []byte, o Offer) string {
	repl := o.New
	if l.Form == string(markdown.FormWiki) && (l.DestEnd >= len(src) || src[l.DestEnd] != '|') {
		repl += "|" + strings.TrimSpace(string(src[l.DestStart:l.DestEnd]))
	}
	return repl
}

// originalTitle returns a page's title as written.
func originalTitle(w *Wiki, id string) string {
	var title string
	if err := w.db.QueryRow(`SELECT title FROM pages WHERE path = ?`, id).Scan(&title); err != nil {
		return id
	}
	return title
}

// candidates returns the pages a wiki target could mean at the first rule that
// matches any.
func (ix *index) candidates(target string) []string {
	t, h, stem := wikiKeys(target)
	for _, matches := range [][]string{union(ix.byPath[path.Clean(t)], ix.byPath[path.Clean(h)]), ix.byTitle[t], ix.byStem[stem]} {
		if len(matches) > 0 {
			out := slices.Clone(matches)
			sort.Strings(out)
			return out
		}
	}
	return nil
}

// similarPages ranks pages whose path, title or file name is close to key.
func similarPages(ix *index, key string) []string {
	key = strings.ToLower(key)
	stem := path.Base(strings.ReplaceAll(key, " ", "-"))
	limit := max(2, len(stem)/4)
	best := map[string]int{}
	var buf []int
	consider := func(k string, ids []string) {
		d := limit + 1
		if strings.Contains(k, stem) || strings.Contains(stem, k) {
			d = limit
		}
		d = min(d, boundedDistance(k, key, limit, &buf))
		if base := path.Base(k); base != k || stem != key {
			d = min(d, boundedDistance(base, stem, limit, &buf))
		}
		if d > limit {
			return
		}
		for _, id := range ids {
			if old, ok := best[id]; !ok || d < old {
				best[id] = d
			}
		}
	}
	for k, ids := range ix.byStem {
		consider(k, ids)
	}
	for k, ids := range ix.byTitle {
		consider(k, ids)
	}
	out := make([]string, 0, len(best))
	for id := range best {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool {
		if best[out[i]] != best[out[j]] {
			return best[out[i]] < best[out[j]]
		}
		return out[i] < out[j]
	})
	return out
}

// findFiles returns repository-relative paths of files named base, skipping
// hidden directories. The walk stops after 50,000 entries.
func (w *Wiki) findFiles(base string) []string {
	var out []string
	seen := 0
	dirs := []string{w.Repo}
	for len(dirs) > 0 && seen < 50000 && len(out) < maxOffers {
		dir := dirs[len(dirs)-1]
		dirs = dirs[:len(dirs)-1]
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			seen++
			if strings.HasPrefix(e.Name(), ".") {
				continue
			}
			p := filepath.Join(dir, e.Name())
			if e.IsDir() {
				dirs = append(dirs, p)
			} else if e.Name() == base {
				if rel, err := filepath.Rel(w.Repo, p); err == nil {
					out = append(out, filepath.ToSlash(rel))
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// editDistance is the Levenshtein distance between two strings, by rune.
func editDistance(a, b string) int {
	return boundedDistance(a, b, max(len(a), len(b)), new([]int))
}

// boundedDistance is the edit distance between a and b when it is at most
// limit, and limit+1 otherwise. It compares bytes when both strings are ASCII
// and reuses buf between calls.
func boundedDistance(a, b string, limit int, buf *[]int) int {
	if !isASCII(a) || !isASCII(b) {
		ra, rb := []rune(a), []rune(b)
		return distance(len(ra), len(rb), func(i, j int) bool { return ra[i] == rb[j] }, limit, buf)
	}
	return distance(len(a), len(b), func(i, j int) bool { return a[i] == b[j] }, limit, buf)
}

func distance(la, lb int, same func(i, j int) bool, limit int, buf *[]int) int {
	// The distance is at least the difference in length.
	if la-lb > limit || lb-la > limit {
		return limit + 1
	}
	if cap(*buf) < 2*(lb+1) {
		*buf = make([]int, 2*(lb+1))
	}
	prev, cur := (*buf)[:lb+1], (*buf)[lb+1:2*(lb+1)]
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		cur[0] = i
		lowest := i
		for j := 1; j <= lb; j++ {
			cost := 1
			if same(i-1, j-1) {
				cost = 0
			}
			cur[j] = min(cur[j-1]+1, prev[j]+1, prev[j-1]+cost)
			lowest = min(lowest, cur[j])
		}
		// Every later row is at least this row's minimum.
		if lowest > limit {
			return limit + 1
		}
		prev, cur = cur, prev
	}
	return min(prev[lb], limit+1)
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}
