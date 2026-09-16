package lsp

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/shakfu/gwiki/internal/markdown"
	"github.com/shakfu/gwiki/internal/wiki"
)

// maxItems caps completion and symbol lists; the editor asks again as the user
// types.
const maxItems = 200

// linkAt is the link under an offset.
func linkAt(links []wiki.Link, off int) (wiki.Link, bool) {
	for _, l := range links {
		if l.Start >= 0 && off >= l.Start && off <= l.End {
			return l, true
		}
		if l.DestStart >= 0 && off >= l.DestStart && off <= l.DestEnd {
			return l, true
		}
	}
	return wiki.Link{}, false
}

// at decodes a position request into its document, offset and link.
func (s *Server) at(raw json.RawMessage) (*document, int, wiki.Link, bool, error) {
	var p textDocumentPosition
	if err := decode(raw, &p); err != nil {
		return nil, 0, wiki.Link{}, false, err
	}
	d, err := s.doc(p.TextDocument.URI)
	if err != nil {
		return nil, 0, wiki.Link{}, false, err
	}
	off := d.text.offset(p.Position)
	l, ok := linkAt(s.links(d), off)
	return d, off, l, ok, nil
}

// headings are a page's headings, from its open buffer when there is one.
func (s *Server) headings(page string) []markdown.Heading {
	if t, err := s.source(page); err == nil {
		return markdown.Parse(t.src).Headings
	}
	return nil
}

func headingLine(heads []markdown.Heading, anchor string) (int, bool) {
	a := strings.ToLower(anchor)
	for _, h := range heads {
		if h.Slug == a || h.Slug == markdown.Slug(anchor) {
			return h.Line, true
		}
	}
	return 0, false
}

var lineAnchor = regexp.MustCompile(`^L(\d+)(?:-L(\d+))?$`)

// ---------------------------------------------------------------- definition

func (s *Server) definition(raw json.RawMessage) (any, error) {
	_, _, l, ok, err := s.at(raw)
	if err != nil || !ok {
		return nil, err
	}
	loc, ok := s.target(l)
	if !ok {
		return nil, nil
	}
	return loc, nil
}

// target is where a link leads, when that is a page or a file.
func (s *Server) target(l wiki.Link) (Location, bool) {
	switch {
	case (l.Kind == wiki.KindPage || l.Kind == wiki.KindHeading) && (l.Status == wiki.StatusOK || l.Status == wiki.StatusMissingHeading):
		line := 0
		if l.Anchor != "" {
			if n, ok := headingLine(s.headings(l.Resolved), l.Anchor); ok {
				line = n - 1
			}
		}
		p := Position{Line: line}
		return Location{URI: fileURI(s.w.PageFile(l.Resolved)), Range: Range{p, p}}, true
	case (l.Kind == wiki.KindFile || l.Kind == wiki.KindLine) && (l.Status == wiki.StatusOK || l.Status == wiki.StatusLineOutOfRange):
		line := 0
		if m := lineAnchor.FindStringSubmatch(l.Anchor); m != nil {
			n, _ := strconv.Atoi(m[1])
			line = max(0, n-1)
		}
		p := Position{Line: line}
		return Location{URI: fileURI(filepath.Join(s.w.Repo, filepath.FromSlash(l.Resolved))), Range: Range{p, p}}, true
	}
	return Location{}, false
}

// ---------------------------------------------------------------- references

func (s *Server) references(raw json.RawMessage) (any, error) {
	var p struct {
		Context struct {
			IncludeDeclaration bool `json:"includeDeclaration"`
		} `json:"context"`
	}
	if err := decode(raw, &p); err != nil {
		return nil, err
	}
	d, _, l, onLink, err := s.at(raw)
	if err != nil {
		return nil, err
	}
	page := d.page
	if onLink && (l.Kind == wiki.KindPage || l.Kind == wiki.KindHeading) && l.Resolved != "" {
		page = l.Resolved
	}
	if page == "" {
		return []Location{}, nil
	}
	back, err := s.w.Backlinks(page)
	if err != nil {
		return nil, err
	}
	out := []Location{}
	if p.Context.IncludeDeclaration {
		out = append(out, Location{URI: fileURI(s.w.PageFile(page))})
	}
	for _, b := range back {
		t, err := s.source(b.Page)
		if err != nil {
			continue
		}
		// An open buffer may differ from the indexed page; its own links are
		// resolved again so the positions match what the editor shows.
		if uri := fileURI(s.w.PageFile(b.Page)); s.docs[uri] != nil {
			continue
		}
		out = append(out, Location{URI: fileURI(s.w.PageFile(b.Page)), Range: linkRange(t, b)})
	}
	for _, od := range s.sortedDocs() {
		if od.page == "" || od.page == page {
			continue
		}
		for _, bl := range s.links(od) {
			if (bl.Kind == wiki.KindPage || bl.Kind == wiki.KindHeading) && bl.Resolved == page && (bl.Status == wiki.StatusOK || bl.Status == wiki.StatusMissingHeading) {
				out = append(out, Location{URI: od.uri, Range: linkRange(od.text, bl)})
			}
		}
	}
	return out, nil
}

func (s *Server) sortedDocs() []*document {
	out := make([]*document, 0, len(s.docs))
	for _, d := range s.docs {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].uri < out[j].uri })
	return out
}

// ---------------------------------------------------------------- hover

func (s *Server) hover(raw json.RawMessage) (any, error) {
	d, _, l, ok, err := s.at(raw)
	if err != nil || !ok {
		return nil, err
	}
	var b strings.Builder
	switch {
	case l.Status != wiki.StatusOK:
		fmt.Fprintf(&b, "**%s**: %s", l.Status, problem(l))
		if offers, err := s.snap.Offers(l, d.text.src); err == nil && len(offers) > 0 {
			fmt.Fprintf(&b, "\n\n%d repairs as code actions", len(offers))
		}
	case l.Kind == wiki.KindExternal:
		b.WriteString(l.Resolved)
	case l.Kind == wiki.KindPage || l.Kind == wiki.KindHeading:
		title := l.Resolved
		for _, p := range s.snap.Pages() {
			if p.Path == l.Resolved {
				title = p.Title
			}
		}
		fmt.Fprintf(&b, "**%s**\n\n`%s`", title, l.Resolved)
		if l.Anchor != "" {
			fmt.Fprintf(&b, " # %s", l.Anchor)
		}
		if back, err := s.w.Backlinks(l.Resolved); err == nil {
			fmt.Fprintf(&b, "\n\n%d backlinks", len(back))
		}
	case l.Kind == wiki.KindFile || l.Kind == wiki.KindLine:
		fmt.Fprintf(&b, "`%s`", l.Resolved)
		if m := lineAnchor.FindStringSubmatch(l.Anchor); m != nil {
			from, _ := strconv.Atoi(m[1])
			to := from
			if m[2] != "" {
				to, _ = strconv.Atoi(m[2])
			}
			b.WriteString(excerpt(filepath.Join(s.w.Repo, filepath.FromSlash(l.Resolved)), from, to))
		}
	}
	return map[string]any{
		"contents": map[string]any{"kind": "markdown", "value": b.String()},
		"range":    linkRange(d.text, l),
	}, nil
}

// excerpt is a fenced block of a file's lines from..to, at most 20 of them.
func excerpt(file string, from, to int) string {
	raw, err := os.ReadFile(file)
	if err != nil {
		return ""
	}
	lines := strings.Split(string(raw), "\n")
	from = max(1, from)
	to = min(max(from, to), len(lines), from+19)
	if from > len(lines) {
		return ""
	}
	fence := "```"
	for strings.Contains(string(raw), fence) {
		fence += "`"
	}
	lang := strings.TrimPrefix(filepath.Ext(file), ".")
	return fmt.Sprintf("\n\n%s%s\n%s\n%s", fence, lang, strings.Join(lines[from-1:to], "\n"), fence)
}

// ---------------------------------------------------------------- symbols

type documentSymbol struct {
	Name           string           `json:"name"`
	Kind           int              `json:"kind"`
	Range          Range            `json:"range"`
	SelectionRange Range            `json:"selectionRange"`
	Children       []documentSymbol `json:"children,omitempty"`
}

const (
	symbolFile   = 1
	symbolString = 15
)

func (s *Server) documentSymbol(raw json.RawMessage) (any, error) {
	var p struct {
		TextDocument struct {
			URI string `json:"uri"`
		} `json:"textDocument"`
	}
	if err := decode(raw, &p); err != nil {
		return nil, err
	}
	d, err := s.doc(p.TextDocument.URI)
	if err != nil {
		return nil, err
	}
	heads := markdown.Parse(d.text.src).Headings
	lastLine := len(d.text.lines) - 1

	// Each heading's section runs to the next heading at its level or above.
	type node struct {
		sym   documentSymbol
		level int
		kids  []*node
	}
	root := &node{level: 0}
	stack := []*node{root}
	for i, h := range heads {
		end := lastLine
		for _, next := range heads[i+1:] {
			if next.Level <= h.Level {
				end = next.Line - 2
				break
			}
		}
		line := h.Line - 1
		sel := Range{Position{line, 0}, d.text.pos(d.text.offset(Position{line, 1 << 30}))}
		n := &node{level: h.Level, sym: documentSymbol{Name: h.Text, Kind: symbolString, SelectionRange: sel,
			Range: Range{sel.Start, d.text.pos(d.text.offset(Position{max(line, end), 1 << 30}))}}}
		for len(stack) > 1 && stack[len(stack)-1].level >= h.Level {
			stack = stack[:len(stack)-1]
		}
		parent := stack[len(stack)-1]
		parent.kids = append(parent.kids, n)
		stack = append(stack, n)
	}
	var build func(n *node) []documentSymbol
	build = func(n *node) []documentSymbol {
		out := []documentSymbol{}
		for _, k := range n.kids {
			k.sym.Children = build(k)
			out = append(out, k.sym)
		}
		return out
	}
	return build(root), nil
}

func (s *Server) workspaceSymbol(raw json.RawMessage) (any, error) {
	var p struct {
		Query string `json:"query"`
	}
	if err := decode(raw, &p); err != nil {
		return nil, err
	}
	q := strings.ToLower(p.Query)
	out := []map[string]any{}
	for _, pg := range s.snap.Pages() {
		if len(out) >= maxItems {
			break
		}
		if q != "" && !subsequence(strings.ToLower(pg.Title+" "+pg.Path), q) {
			continue
		}
		dir := path.Dir(pg.Path)
		if dir == "." {
			dir = ""
		}
		out = append(out, map[string]any{
			"name":          pg.Title,
			"kind":          symbolFile,
			"containerName": dir,
			"location":      Location{URI: fileURI(s.w.PageFile(pg.Path))},
		})
	}
	return out, nil
}

func subsequence(s, sub string) bool {
	for _, r := range sub {
		i := strings.IndexRune(s, r)
		if i < 0 {
			return false
		}
		s = s[i+len(string(r)):]
	}
	return true
}

// ---------------------------------------------------------------- completion

type completionItem struct {
	Label      string    `json:"label"`
	Kind       int       `json:"kind"`
	Detail     string    `json:"detail,omitempty"`
	FilterText string    `json:"filterText,omitempty"`
	SortText   string    `json:"sortText,omitempty"`
	TextEdit   *TextEdit `json:"textEdit,omitempty"`
}

const (
	itemFile      = 17
	itemReference = 18
	itemFolder    = 19
)

func (s *Server) completion(raw json.RawMessage) (any, error) {
	d, off, _, _, err := s.at(raw)
	if err != nil {
		return nil, err
	}
	if d.page == "" {
		return []completionItem{}, nil
	}
	before := d.text.lineBefore(off)

	if i := strings.LastIndex(before, "[["); i >= 0 && !strings.Contains(before[i:], "]]") && !strings.Contains(before[i:], "|") {
		partial := before[i+2:]
		start := off - len(partial)
		closing := ""
		if !strings.HasPrefix(d.text.lineAfter(off), "]]") {
			closing = "]]"
		}
		if target, anchor, found := strings.Cut(partial, "#"); found {
			page := d.page
			if strings.TrimSpace(target) != "" {
				var status string
				if page, status = s.snap.WikiTarget(target); status != wiki.StatusOK {
					return []completionItem{}, nil
				}
			}
			return s.headingItems(d, page, off-len(anchor), off, anchor, false, closing), nil
		}
		return s.pageItems(d, start, off, partial, closing), nil
	}

	if i := strings.LastIndex(before, "]("); i >= 0 && !strings.ContainsAny(before[i:], ") ") {
		partial := before[i+2:]
		if dest, anchor, found := strings.Cut(partial, "#"); found {
			page := d.page
			if dest != "" {
				file := filepath.Join(s.w.PagesPath(), filepath.FromSlash(path.Dir(d.page)), filepath.FromSlash(dest))
				if strings.HasPrefix(dest, "/") {
					file = filepath.Join(s.w.Repo, filepath.FromSlash(dest))
				}
				var ok bool
				if page, ok = s.w.PageOf(file); !ok {
					return []completionItem{}, nil
				}
			}
			return s.headingItems(d, page, off-len(anchor), off, anchor, true, ""), nil
		}
		return s.pathItems(d, off, partial), nil
	}
	return []completionItem{}, nil
}

func (s *Server) pageItems(d *document, start, end int, partial, closing string) []completionItem {
	q := strings.ToLower(strings.TrimSpace(partial))
	items := []completionItem{}
	for _, p := range s.snap.Pages() {
		if len(items) >= maxItems {
			break
		}
		if q != "" && !subsequence(strings.ToLower(p.Title+" "+p.Path), q) {
			continue
		}
		// The title reads better, but only the path is certain to name one
		// page.
		insert := p.Path
		if got, status := s.snap.WikiTarget(p.Title); status == wiki.StatusOK && got == p.Path {
			insert = p.Title
		}
		items = append(items, completionItem{
			Label: p.Title, Kind: itemReference, Detail: p.Path, FilterText: p.Title + " " + p.Path,
			TextEdit: &TextEdit{Range: d.text.rng(start, end), NewText: insert + closing},
		})
	}
	return items
}

func (s *Server) headingItems(d *document, page string, start, end int, partial string, slug bool, closing string) []completionItem {
	items := []completionItem{}
	heads := s.headings(page)
	if page == d.page {
		heads = markdown.Parse(d.text.src).Headings
	}
	for i, h := range heads {
		insert := h.Text
		if slug {
			insert = h.Slug
		}
		items = append(items, completionItem{
			Label: h.Text, Kind: itemReference, Detail: strings.Repeat("#", h.Level), FilterText: h.Text + " " + h.Slug,
			SortText: fmt.Sprintf("%05d", i),
			TextEdit: &TextEdit{Range: d.text.rng(start, end), NewText: insert + closing},
		})
	}
	return items
}

// pathItems lists the directory a partial markdown destination is in,
// relative to the page, or to the repository for a destination starting with
// '/'.
func (s *Server) pathItems(d *document, off int, partial string) []completionItem {
	dirPart, base := "", partial
	if i := strings.LastIndexByte(partial, '/'); i >= 0 {
		dirPart, base = partial[:i+1], partial[i+1:]
	}
	dir := filepath.Join(s.w.PagesPath(), filepath.FromSlash(path.Dir(d.page)), filepath.FromSlash(dirPart))
	if strings.HasPrefix(partial, "/") {
		dir = filepath.Join(s.w.Repo, filepath.FromSlash(dirPart))
	}
	items := []completionItem{}
	if rel, err := filepath.Rel(s.w.Repo, dir); err != nil || strings.HasPrefix(rel, "..") {
		return items
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return items
	}
	titles := map[string]string{}
	for _, p := range s.snap.Pages() {
		titles[p.Path] = p.Title
	}
	rng := d.text.rng(off-len(base), off)
	for _, e := range entries {
		name := e.Name()
		if len(items) >= maxItems || strings.HasPrefix(name, ".") || !strings.HasPrefix(strings.ToLower(name), strings.ToLower(base)) {
			continue
		}
		item := completionItem{Label: name, Kind: itemFile, TextEdit: &TextEdit{Range: rng, NewText: name}}
		if e.IsDir() {
			item.Label, item.Kind, item.TextEdit.NewText = name+"/", itemFolder, name+"/"
		} else if page, ok := s.w.PageOf(filepath.Join(dir, name)); ok {
			item.Detail = titles[page]
		}
		items = append(items, item)
	}
	return items
}

// ---------------------------------------------------------------- rename

func (s *Server) prepareRename(raw json.RawMessage) (any, error) {
	d, off, l, onLink, err := s.at(raw)
	if err != nil {
		return nil, err
	}
	if onLink && (l.Kind == wiki.KindPage || l.Kind == wiki.KindHeading) && l.Status != wiki.StatusMissingPage && l.Status != wiki.StatusAmbiguous && l.DestStart >= 0 {
		return map[string]any{"range": d.text.rng(l.DestStart, l.DestEnd), "placeholder": l.Resolved}, nil
	}
	if d.page == "" {
		return nil, errors.New("only a wiki page, or a link to one, can be renamed")
	}
	p := d.text.pos(off)
	return map[string]any{"range": Range{p, p}, "placeholder": d.page}, nil
}

// rename moves a page: the page a link under the cursor names, else the
// document's own page. The result renames the file and rewrites every link
// the move changes; the editor applies it.
func (s *Server) rename(raw json.RawMessage) (any, error) {
	var p struct {
		NewName string `json:"newName"`
	}
	if err := decode(raw, &p); err != nil {
		return nil, err
	}
	d, _, l, onLink, err := s.at(raw)
	if err != nil {
		return nil, err
	}
	from := d.page
	if onLink && (l.Kind == wiki.KindPage || l.Kind == wiki.KindHeading) && l.Resolved != "" && l.Status != wiki.StatusMissingPage {
		from = l.Resolved
	}
	if from == "" {
		return nil, errors.New("only a wiki page, or a link to one, can be renamed")
	}
	if !s.documentChanges || !s.renameFiles {
		return nil, errors.New("this editor cannot rename files through LSP; use 'gwiki mv'")
	}
	to := strings.TrimSpace(p.NewName)
	if strings.HasSuffix(to, "/") {
		to += path.Base(from)
	}
	plan, err := s.plan(from, to)
	if err != nil {
		return nil, err
	}
	changes := s.textEdits(plan)
	changes = append(changes, map[string]any{
		"kind":   "rename",
		"oldUri": fileURI(s.w.PageFile(plan.From)),
		"newUri": fileURI(s.w.PageFile(plan.To)),
	})
	return map[string]any{"documentChanges": changes}, nil
}

// willRenameFiles supplies the link edits for pages the editor is about to
// rename itself, such as from a file tree.
func (s *Server) willRenameFiles(raw json.RawMessage) (any, error) {
	var p struct {
		Files []struct {
			OldURI string `json:"oldUri"`
			NewURI string `json:"newUri"`
		} `json:"files"`
	}
	if err := decode(raw, &p); err != nil {
		return nil, err
	}
	var changes []map[string]any
	for _, f := range p.Files {
		oldFile, ok1 := uriPath(f.OldURI)
		newFile, ok2 := uriPath(f.NewURI)
		if !ok1 || !ok2 {
			continue
		}
		from, ok1 := s.w.PageOf(oldFile)
		to, ok2 := s.w.PageOf(newFile)
		if !ok1 || !ok2 {
			continue
		}
		plan, err := s.plan(from, to)
		if err != nil {
			return nil, err
		}
		changes = append(changes, s.textEdits(plan)...)
	}
	if len(changes) == 0 {
		return nil, nil
	}
	return map[string]any{"documentChanges": changes}, nil
}

// plan works out a move, refusing when a page it would edit has unsaved
// changes in the editor, since the edits are positioned in the saved file.
func (s *Server) plan(from, to string) (*wiki.MovePlan, error) {
	plan, err := s.w.PlanMove(from, to)
	if err != nil {
		return nil, err
	}
	pages := map[string]bool{plan.From: true}
	for _, e := range plan.Edits {
		pages[s.sourcePage(plan, e)] = true
	}
	for page := range pages {
		d, open := s.docs[fileURI(s.w.PageFile(page))]
		if !open {
			continue
		}
		saved, err := os.ReadFile(s.w.PageFile(page))
		if err != nil || string(saved) != string(d.text.src) {
			return nil, fmt.Errorf("%s has unsaved changes; save it and rename again", page)
		}
	}
	return plan, nil
}

// sourcePage is the page an edit applies to before the move.
func (s *Server) sourcePage(plan *wiki.MovePlan, e wiki.Edit) string {
	if e.Page == plan.To {
		return plan.From
	}
	return e.Page
}

// textEdits groups a plan's edits into one text document edit per page, in
// the pages' saved sources.
func (s *Server) textEdits(plan *wiki.MovePlan) []map[string]any {
	byPage := map[string][]TextEdit{}
	var order []string
	for _, e := range plan.Edits {
		page := s.sourcePage(plan, e)
		t, err := s.source(page)
		if err != nil {
			continue
		}
		if _, seen := byPage[page]; !seen {
			order = append(order, page)
		}
		byPage[page] = append(byPage[page], TextEdit{Range: t.rng(e.Start, e.End), NewText: e.New})
	}
	sort.Strings(order)
	out := []map[string]any{}
	for _, page := range order {
		out = append(out, map[string]any{
			"textDocument": map[string]any{"uri": fileURI(s.w.PageFile(page)), "version": nil},
			"edits":        byPage[page],
		})
	}
	return out
}

// ---------------------------------------------------------------- code actions

func (s *Server) codeAction(raw json.RawMessage) (any, error) {
	var p struct {
		TextDocument struct {
			URI string `json:"uri"`
		} `json:"textDocument"`
		Range   Range `json:"range"`
		Context struct {
			Diagnostics []json.RawMessage `json:"diagnostics"`
		} `json:"context"`
	}
	if err := decode(raw, &p); err != nil {
		return nil, err
	}
	d, err := s.doc(p.TextDocument.URI)
	if err != nil {
		return nil, err
	}
	start, end := d.text.offset(p.Range.Start), d.text.offset(p.Range.End)
	actions := []map[string]any{}
	for _, l := range s.links(d) {
		if l.Status == wiki.StatusOK || l.DestStart < 0 || l.DestEnd < start || l.DestStart > end {
			continue
		}
		offers, err := s.snap.Offers(l, d.text.src)
		if err != nil {
			return nil, err
		}
		rng := d.text.rng(l.DestStart, l.DestEnd)
		for i, o := range offers {
			actions = append(actions, map[string]any{
				"title":       fmt.Sprintf("Link to %s (%s)", o.New, o.Label),
				"kind":        "quickfix",
				"isPreferred": i == 0 && len(offers) == 1,
				"edit": map[string]any{"changes": map[string][]TextEdit{
					d.uri: {{Range: rng, NewText: wiki.FixText(l, d.text.src, o)}},
				}},
			})
		}
		if action, ok := s.createPage(d, l); ok {
			actions = append(actions, action)
		}
	}
	return actions, nil
}

// createPage is an action creating the page a missing-page link names.
func (s *Server) createPage(d *document, l wiki.Link) (map[string]any, bool) {
	if l.Status != wiki.StatusMissingPage || !s.documentChanges || !s.createFiles {
		return nil, false
	}
	var page, title string
	if l.Form == "wiki" {
		title = strings.TrimSpace(l.Target)
		dir := path.Dir(d.page)
		page = wiki.Slugify(title)
		if strings.Contains(l.Target, "/") {
			page = l.Target
			title = path.Base(l.Target)
		} else if dir != "." {
			page = dir + "/" + page
		}
	} else {
		page, title = l.Resolved, l.Label
	}
	page, err := wiki.CleanPath(page)
	if err != nil || page == "" || title == "" {
		return nil, false
	}
	if _, err := os.Stat(s.w.PageFile(page)); err == nil {
		return nil, false
	}
	uri := fileURI(s.w.PageFile(page))
	return map[string]any{
		"title": "Create page " + page,
		"kind":  "quickfix",
		"edit": map[string]any{"documentChanges": []any{
			map[string]any{"kind": "create", "uri": uri, "options": map[string]any{"ignoreIfExists": true}},
			map[string]any{
				"textDocument": map[string]any{"uri": uri, "version": nil},
				"edits":        []TextEdit{{NewText: "# " + title + "\n"}},
			},
		}},
	}, true
}
