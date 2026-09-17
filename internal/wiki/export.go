package wiki

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/shakfu/gwiki/internal/markdown"
)

// ExportManifest is the file in an export directory that lists what the export
// wrote, so the next export can remove what it no longer writes.
const ExportManifest = ".gwiki-export"

// ErrExportDir means the export directory holds files but no earlier export.
var ErrExportDir = errors.New("the directory is not empty and holds no earlier gwiki export")

// Exported is what an export wrote.
type Exported struct {
	Dir   string `json:"dir"`
	Pages int    `json:"pages"`
	Files int    `json:"files"` // files under the pages directory that are not pages
	Links int    `json:"links"` // links rewritten

	// Removed are files an earlier export wrote that this one did not.
	Removed []string `json:"removed"`

	// Skipped are wiki links left as written: broken, ambiguous, or without a
	// source position.
	Skipped []Link `json:"skipped"`
}

// Export copies the pages directory into dir with every resolving [[wiki]]
// link rewritten as a relative markdown link, which GitHub and other markdown
// hosts render. A wiki link keeps the text it displayed. Relative links to
// files outside the pages directory are re-pointed from dir: relative when dir
// is inside the repository, else rooted at it, as /src/lexer.go. The pages
// themselves are not changed.
//
// dir must be empty, missing, or an earlier export; files an earlier export
// wrote and this one does not are removed, and no others.
func (w *Wiki) Export(dir string) (*Exported, error) {
	if _, err := w.Refresh(); err != nil {
		return nil, err
	}
	out, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	pages := w.PagesPath()
	if within(out, pages) || within(pages, out) {
		return nil, fmt.Errorf("cannot export to %s, which holds or is inside the pages", dir)
	}
	previous, err := readManifest(out)
	if err != nil {
		return nil, err
	}

	files, err := exportFiles(pages)
	if err != nil {
		return nil, err
	}
	pagesRel, err := filepath.Rel(w.Repo, pages)
	if err != nil {
		return nil, err
	}
	res := &Exported{Dir: out, Removed: []string{}, Skipped: []Link{}}
	inRepo := within(out, w.Repo)
	written := map[string]bool{}
	for _, rel := range files {
		data, err := os.ReadFile(filepath.Join(pages, filepath.FromSlash(rel)))
		if err != nil {
			return nil, err
		}
		if strings.HasSuffix(rel, ".md") {
			page := strings.TrimSuffix(rel, ".md")
			fromDir := filepath.Join(out, filepath.FromSlash(path.Dir(rel)))
			var n int
			var skipped []Link
			if data, n, skipped, err = w.exportPage(page, data, fromDir, filepath.ToSlash(pagesRel), inRepo); err != nil {
				return nil, err
			}
			res.Pages++
			res.Links += n
			res.Skipped = append(res.Skipped, skipped...)
		} else {
			res.Files++
		}
		file := filepath.Join(out, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(file, data, 0o644); err != nil {
			return nil, err
		}
		written[rel] = true
	}

	for _, rel := range previous {
		if written[rel] {
			continue
		}
		if err := os.Remove(filepath.Join(out, filepath.FromSlash(rel))); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		res.Removed = append(res.Removed, rel)
	}
	manifest := strings.Join(files, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(out, ExportManifest), []byte(manifest), 0o644); err != nil {
		return nil, err
	}
	return res, nil
}

// exportPage rewrites one page's links for an export written in fromDir.
func (w *Wiki) exportPage(page string, src []byte, fromDir, pagesRel string, inRepo bool) ([]byte, int, []Link, error) {
	links, err := w.Links(page)
	if err != nil {
		return nil, 0, nil, err
	}
	var spans []span
	var skipped []Link
	seen := map[int]bool{}
	for _, l := range links {
		switch {
		case l.Form == string(markdown.FormWiki):
			if l.Status != StatusOK || l.Start < 0 || l.DestStart < 0 {
				skipped = append(skipped, l)
				continue
			}
			if err := checkDest(src, l); err != nil {
				return nil, 0, nil, err
			}
			dest, ok, err := w.wikiAsMarkdown(l)
			if err != nil {
				return nil, 0, nil, err
			}
			if !ok {
				skipped = append(skipped, l)
				continue
			}
			spans = append(spans, span{start: l.Start, end: l.End, old: string(src[l.Start:l.End]),
				new: "[" + escapeLabel(l.Label) + "](" + dest + ")"})

		case l.Kind == KindFile || l.Kind == KindLine:
			// A rooted link, and a file copied with the pages, read the same
			// from the export.
			if l.DestStart < 0 || seen[l.DestStart] || l.Status == StatusOutsideRepo ||
				strings.HasPrefix(l.Target, "/") || strings.HasPrefix(l.Resolved+"/", pagesRel+"/") {
				continue
			}
			seen[l.DestStart] = true
			if err := checkDest(src, l); err != nil {
				return nil, 0, nil, err
			}
			rooted := l
			if !inRepo {
				rooted.Target = "/" + l.Target
			}
			target := filepath.Join(w.Repo, filepath.FromSlash(l.Resolved))
			dest := w.markdownDestFrom(src, rooted, fromDir, target, strings.HasSuffix(l.Target, "/"))
			if old := string(src[l.DestStart:l.DestEnd]); dest != old {
				spans = append(spans, span{start: l.DestStart, end: l.DestEnd, old: old, new: dest})
			}
		}
	}
	out, err := applySpans(src, spans)
	return out, len(spans), skipped, err
}

// exportFiles lists the files under the pages directory, skipping hidden
// entries as the cache does, as slash-separated relative paths in order.
func exportFiles(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p != root && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() {
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

// readManifest returns the files an earlier export into dir wrote. A missing
// or empty dir has none; a non-empty dir without a manifest is refused. A
// listed path that would leave dir is refused rather than removed later.
func readManifest(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(filepath.Join(dir, ExportManifest))
	if os.IsNotExist(err) {
		if len(entries) == 0 {
			return nil, nil
		}
		return nil, fmt.Errorf("%w: %s", ErrExportDir, dir)
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(string(raw), "\n") {
		if line == "" {
			continue
		}
		if !filepath.IsLocal(filepath.FromSlash(line)) {
			return nil, fmt.Errorf("%s lists %q, outside the directory", ExportManifest, line)
		}
		out = append(out, line)
	}
	return out, nil
}

// within reports whether path is dir or below it.
func within(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// wikiAsMarkdown is the markdown destination of a resolving wiki link: the
// relative path to its page, with the heading's GitHub slug. ok is false when
// the heading is not found.
func (w *Wiki) wikiAsMarkdown(l Link) (string, bool, error) {
	anchor := ""
	if l.Anchor != "" {
		heads, err := w.Headings(l.Resolved)
		if err != nil {
			return "", false, err
		}
		want, slug := strings.ToLower(l.Anchor), markdown.Slug(l.Anchor)
		for _, h := range heads {
			if h.Slug == want || h.Slug == slug {
				anchor = h.Slug
				break
			}
		}
		if anchor == "" {
			return "", false, nil
		}
	}
	if l.Resolved == l.Page && l.Target == "" {
		return "#" + anchor, true, nil
	}
	rel, _ := filepath.Rel(filepath.Dir(w.file(l.Page)), w.file(l.Resolved))
	dest := strings.NewReplacer(" ", "%20", "(", "%28", ")", "%29").Replace(filepath.ToSlash(rel))
	if anchor != "" {
		dest += "#" + anchor
	}
	return dest, true, nil
}

// escapeLabel escapes the characters that would end or nest a link's text.
func escapeLabel(s string) string {
	return strings.NewReplacer(`\`, `\\`, `[`, `\[`, `]`, `\]`).Replace(s)
}
