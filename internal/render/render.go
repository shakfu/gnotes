// Package render draws a markdown page as styled terminal lines, wrapped to a
// width, and records where its links and headings land.
//
// It walks goldmark's tree rather than using glamour, which added 7.86 MB to
// the binary (docs/dev/wiki-design.md, section 17). Page text is untrusted:
// control characters are replaced before anything is drawn.
package render

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/yuin/goldmark/ast"
	extast "github.com/yuin/goldmark/extension/ast"

	"github.com/shakfu/gwiki/internal/display"
	"github.com/shakfu/gwiki/internal/markdown"
)

// Styles are the looks of each element.
type Styles struct {
	Heading  [3]lipgloss.Style // levels 1, 2, and 3 and deeper
	Strong   lipgloss.Style
	Emphasis lipgloss.Style
	Strike   lipgloss.Style
	Code     lipgloss.Style
	Quote    lipgloss.Style
	Marker   lipgloss.Style // list bullets, rules, table borders
	HTML     lipgloss.Style
	Link     lipgloss.Style
	Broken   lipgloss.Style
	Selected lipgloss.Style
}

// DefaultStyles use the terminal's own ANSI colours.
func DefaultStyles() Styles {
	return Styles{
		Heading: [3]lipgloss.Style{
			lipgloss.NewStyle().Bold(true).Underline(true).Foreground(lipgloss.Color("4")),
			lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("4")),
			lipgloss.NewStyle().Bold(true),
		},
		Strong:   lipgloss.NewStyle().Bold(true),
		Emphasis: lipgloss.NewStyle().Italic(true),
		Strike:   lipgloss.NewStyle().Faint(true),
		Code:     lipgloss.NewStyle().Foreground(lipgloss.Color("3")),
		Quote:    lipgloss.NewStyle().Faint(true),
		Marker:   lipgloss.NewStyle().Faint(true),
		HTML:     lipgloss.NewStyle().Faint(true),
		Link:     lipgloss.NewStyle().Underline(true).Foreground(lipgloss.Color("6")),
		Broken:   lipgloss.NewStyle().Underline(true).Foreground(lipgloss.Color("1")),
		Selected: lipgloss.NewStyle().Reverse(true),
	}
}

// Link is a link as drawn, in reading order.
type Link struct {
	Form   markdown.Form
	Image  bool
	Target string // as written, before any '#'
	Anchor string
	Label  string
	Line   int // the output line it starts on
}

// Doc is a rendered page.
type Doc struct {
	Lines []string

	// Source is the 1-based source line each output line came from: the first
	// line of its block.
	Source []int

	Links []Link

	// Headings maps each heading's slug to its output line.
	Headings map[string]int
}

// Options control a rendering.
type Options struct {
	Width  int
	Styles Styles

	// Selected is the index of the link drawn selected, or -1.
	Selected int

	// Broken reports whether a link is drawn broken. Nil draws none broken.
	Broken func(Link) bool
}

// Render draws a page's source.
func Render(src []byte, o Options) *Doc {
	root, doc := markdown.Tree(src)
	r := &renderer{
		doc:   doc,
		o:     o,
		out:   &Doc{Headings: map[string]int{}},
		lines: lineStarts(doc),
		width: max(1, o.Width),
	}
	for _, h := range markdown.Parse(src).Headings {
		r.slugs = append(r.slugs, h.Slug)
	}
	r.children(root, &margin{}, true)
	return r.out
}

type renderer struct {
	doc      []byte
	o        Options
	out      *Doc
	lines    []int
	width    int
	slugs    []string
	headings int
}

// margin is the prefix of each line of a block, nested inside its parents'.
// first is used once, on the block's first line; its width equals rest's.
type margin struct {
	parent      *margin
	first, rest string
	used        bool
}

func (m *margin) take() string {
	s := m.rest
	if !m.used {
		s, m.used = m.first, true
	}
	if m.parent != nil {
		return m.parent.take() + s
	}
	return s
}

func (m *margin) width() int {
	w := ansi.StringWidth(m.rest)
	if m.parent != nil {
		w += m.parent.width()
	}
	return w
}

func (r *renderer) emit(m *margin, line string, src int) {
	if n := len(r.out.Source); src == 0 && n > 0 {
		src = r.out.Source[n-1] // a block without lines, such as a rule
	}
	line = m.take() + line
	if ansi.StringWidth(line) > r.width {
		line = ansi.Truncate(line, r.width, "") // margins wider than the screen
	}
	r.out.Lines = append(r.out.Lines, strings.TrimRight(line, " "))
	r.out.Source = append(r.out.Source, src)
}

// children renders a container's blocks, with a blank line between them when
// spaced.
func (r *renderer) children(n ast.Node, m *margin, spaced bool) {
	first := true
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		if !first && spaced {
			r.emit(m, "", r.sourceLine(c))
		}
		first = false
		r.block(c, m)
	}
}

func (r *renderer) block(n ast.Node, m *margin) {
	src := r.sourceLine(n)
	switch n := n.(type) {
	case *ast.Paragraph, *ast.TextBlock:
		r.wrap(r.inlines(n, nil), m, src)

	case *ast.Heading:
		if r.headings < len(r.slugs) {
			if _, dup := r.out.Headings[r.slugs[r.headings]]; !dup {
				r.out.Headings[r.slugs[r.headings]] = len(r.out.Lines)
			}
		}
		r.headings++
		style := r.o.Styles.Heading[min(n.Level, 3)-1]
		prefix := []piece{{text: strings.Repeat("#", n.Level) + " ", style: &r.o.Styles.Marker, link: -1}}
		r.wrap(append(prefix, r.inlines(n, &style)...), m, src)

	case *ast.ThematicBreak:
		r.emit(m, r.o.Styles.Marker.Render(strings.Repeat("-", max(3, r.width-m.width()))), src)

	case *ast.CodeBlock, *ast.FencedCodeBlock:
		code := &margin{parent: m, first: "  ", rest: "  "}
		lines := n.Lines()
		for i := 0; i < lines.Len(); i++ {
			seg := lines.At(i)
			text := expandTabs(strings.TrimRight(string(seg.Value(r.doc)), "\n"))
			for _, part := range cut(display.Line(text), max(1, r.width-code.width())) {
				r.emit(code, r.o.Styles.Code.Render(part), r.lineOf(seg.Start))
			}
		}

	case *ast.HTMLBlock:
		lines := n.Lines()
		for i := 0; i < lines.Len(); i++ {
			seg := lines.At(i)
			text := display.Line(strings.TrimRight(string(seg.Value(r.doc)), "\n"))
			for _, part := range cut(text, max(1, r.width-m.width())) {
				r.emit(m, r.o.Styles.HTML.Render(part), r.lineOf(seg.Start))
			}
		}

	case *ast.Blockquote:
		bar := r.o.Styles.Quote.Render("| ")
		r.children(n, &margin{parent: m, first: bar, rest: bar}, true)

	case *ast.List:
		number := n.Start
		for item := n.FirstChild(); item != nil; item = item.NextSibling() {
			if item != n.FirstChild() && !n.IsTight {
				r.emit(m, "", r.sourceLine(item))
			}
			marker := "- "
			if n.IsOrdered() {
				marker = fmt.Sprintf("%d. ", number)
				number++
			}
			styled := r.o.Styles.Marker.Render(marker)
			r.children(item, &margin{parent: m, first: styled, rest: strings.Repeat(" ", len(marker))}, !n.IsTight)
		}

	case *extast.Table:
		r.table(n, m, src)

	case *extast.FootnoteList:
		for fn := n.FirstChild(); fn != nil; fn = fn.NextSibling() {
			marker := "[^?] "
			if f, ok := fn.(*extast.Footnote); ok {
				marker = fmt.Sprintf("[^%d] ", f.Index)
			}
			r.children(fn, &margin{parent: m, first: r.o.Styles.Marker.Render(marker), rest: strings.Repeat(" ", len(marker))}, true)
		}

	default:
		r.children(n, m, true)
	}
}

// piece is a run of text in one style. link is the index of the link it
// belongs to, or -1.
type piece struct {
	text  string
	style *lipgloss.Style
	link  int
	br    bool // a hard line break
}

// inlines collects a block's inline content.
func (r *renderer) inlines(n ast.Node, style *lipgloss.Style) []piece {
	var out []piece
	var walk func(n ast.Node, style *lipgloss.Style, link int)
	add := func(text string, style *lipgloss.Style, link int) {
		out = append(out, piece{text: display.Line(text), style: style, link: link})
	}
	walk = func(n ast.Node, style *lipgloss.Style, link int) {
		for c := n.FirstChild(); c != nil; c = c.NextSibling() {
			switch c := c.(type) {
			case *ast.Text:
				add(string(c.Segment.Value(r.doc)), style, link)
				switch {
				case c.HardLineBreak():
					out = append(out, piece{br: true, link: -1})
				case c.SoftLineBreak():
					add(" ", style, link)
				}
			case *ast.String:
				add(string(c.Value), style, link)
			case *ast.CodeSpan:
				var b strings.Builder
				for t := c.FirstChild(); t != nil; t = t.NextSibling() {
					if txt, ok := t.(*ast.Text); ok {
						b.Write(txt.Segment.Value(r.doc))
					}
				}
				add(b.String(), r.over(style, r.o.Styles.Code), link)
			case *ast.Emphasis:
				s := r.o.Styles.Emphasis
				if c.Level >= 2 {
					s = r.o.Styles.Strong
				}
				walk(c, r.over(style, s), link)
			case *extast.Strikethrough:
				walk(c, r.over(style, r.o.Styles.Strike), link)
			case *ast.Link:
				i, s := r.link(markdown.FormMarkdown, false, string(c.Destination), plainText(c, r.doc), style)
				walk(c, s, i)
			case *ast.Image:
				i, s := r.link(markdown.FormMarkdown, true, string(c.Destination), plainText(c, r.doc), style)
				add("[image: "+plainText(c, r.doc)+"]", s, i)
			case *ast.AutoLink:
				url := string(c.URL(r.doc))
				i, s := r.link(markdown.FormAuto, false, url, url, style)
				add(string(c.Label(r.doc)), s, i)
			case *markdown.WikiLink:
				dest := c.Target
				if c.Anchor != "" {
					dest += "#" + c.Anchor
				}
				i, s := r.link(markdown.FormWiki, false, dest, c.Label, style)
				add(c.Label, s, i)
			case *ast.RawHTML:
				var b strings.Builder
				for j := 0; j < c.Segments.Len(); j++ {
					seg := c.Segments.At(j)
					b.Write(seg.Value(r.doc))
				}
				add(b.String(), r.over(style, r.o.Styles.HTML), link)
			case *extast.TaskCheckBox:
				box := "[ ] "
				if c.IsChecked {
					box = "[x] "
				}
				add(box, &r.o.Styles.Marker, link)
			case *extast.FootnoteLink:
				add(fmt.Sprintf("[^%d]", c.Index), &r.o.Styles.Marker, link)
			case *extast.FootnoteBacklink:
			default:
				walk(c, style, link)
			}
		}
	}
	walk(n, style, -1)
	return out
}

// link records a link and returns its index and the style to draw it in.
func (r *renderer) link(form markdown.Form, image bool, dest, label string, base *lipgloss.Style) (int, *lipgloss.Style) {
	l := Link{Form: form, Image: image, Target: dest, Label: label, Line: -1}
	if form != markdown.FormAuto {
		if i := strings.IndexByte(dest, '#'); i >= 0 {
			l.Target, l.Anchor = dest[:i], dest[i+1:]
		}
	}
	i := len(r.out.Links)
	r.out.Links = append(r.out.Links, l)
	s := r.o.Styles.Link
	switch {
	case i == r.o.Selected:
		s = r.o.Styles.Selected
	case r.o.Broken != nil && r.o.Broken(l):
		s = r.o.Styles.Broken
	}
	return i, r.over(base, s)
}

// over is s with base's properties where s sets none.
func (r *renderer) over(base *lipgloss.Style, s lipgloss.Style) *lipgloss.Style {
	if base != nil {
		s = s.Inherit(*base)
	}
	return &s
}

// wrap fills lines to the width, breaking at spaces, and splits a word longer
// than a line.
func (r *renderer) wrap(pieces []piece, m *margin, src int) {
	avail := max(1, r.width-m.width())
	var line strings.Builder
	lineWidth := 0
	var lineLinks []int
	flush := func() {
		start := len(r.out.Lines)
		r.emit(m, line.String(), src)
		for _, i := range lineLinks {
			if r.out.Links[i].Line < 0 {
				r.out.Links[i].Line = start
			}
		}
		line.Reset()
		lineWidth, lineLinks = 0, nil
	}

	// A word is the pieces between spaces; it may change style midway.
	var word []piece
	wordWidth := 0
	space := false // a space is owed before the next word on this line
	place := func() {
		if len(word) == 0 {
			return
		}
		if lineWidth > 0 && lineWidth+1+wordWidth > avail {
			flush()
			space = false
		}
		if space && lineWidth > 0 {
			line.WriteByte(' ')
			lineWidth++
		}
		for _, p := range word {
			for _, part := range splitWidth(p.text, avail, lineWidth) {
				if part.newline {
					flush()
					continue
				}
				line.WriteString(p.draw(part.text))
				lineWidth += ansi.StringWidth(part.text)
				if p.link >= 0 {
					lineLinks = append(lineLinks, p.link)
				}
			}
		}
		word, wordWidth, space = nil, 0, false
	}

	for _, p := range pieces {
		if p.br {
			place()
			flush()
			continue
		}
		rest := p.text
		for rest != "" {
			i := strings.IndexByte(rest, ' ')
			if i < 0 {
				word = append(word, piece{text: rest, style: p.style, link: p.link})
				wordWidth += ansi.StringWidth(rest)
				break
			}
			if i > 0 {
				word = append(word, piece{text: rest[:i], style: p.style, link: p.link})
				wordWidth += ansi.StringWidth(rest[:i])
			}
			place()
			space = true
			rest = rest[i+1:]
		}
	}
	place()
	if lineWidth > 0 || len(pieces) == 0 {
		flush()
	}
}

func (p piece) draw(text string) string {
	if p.style == nil {
		return text
	}
	return p.style.Render(text)
}

type part struct {
	text    string
	newline bool
}

// splitWidth cuts text so it fits a line of avail columns that already holds
// used columns, with a newline between the parts.
func splitWidth(text string, avail, used int) []part {
	var out []part
	for text != "" {
		room := avail - used
		if room <= 0 {
			out = append(out, part{newline: true})
			used, room = 0, avail
		}
		if ansi.StringWidth(text) <= room {
			return append(out, part{text: text})
		}
		head := ansi.Truncate(text, room, "")
		if head == "" {
			// A character wider than the whole line.
			_, size := utf8.DecodeRuneInString(text)
			head = text[:size]
		}
		out = append(out, part{text: head}, part{newline: true})
		text, used = text[len(head):], 0
	}
	return out
}

// table draws a table with columns sized to their widest cell, shrinking the
// widest columns when it does not fit.
func (r *renderer) table(n *extast.Table, m *margin, src int) {
	type cell struct {
		pieces []piece
		width  int
	}
	var rows [][]cell
	var header int
	for row := n.FirstChild(); row != nil; row = row.NextSibling() {
		var cells []cell
		for c := row.FirstChild(); c != nil; c = c.NextSibling() {
			var style *lipgloss.Style
			if _, ok := row.(*extast.TableHeader); ok {
				style = &r.o.Styles.Strong
			}
			ps := r.inlines(c, style)
			w := 0
			for _, p := range ps {
				w += ansi.StringWidth(p.text)
			}
			cells = append(cells, cell{ps, w})
		}
		if _, ok := row.(*extast.TableHeader); ok {
			header = len(rows) + 1
		}
		rows = append(rows, cells)
	}

	cols := 0
	for _, row := range rows {
		cols = max(cols, len(row))
	}
	if cols == 0 {
		return
	}
	widths := make([]int, cols)
	for _, row := range rows {
		for i, c := range row {
			widths[i] = max(widths[i], c.width)
		}
	}
	avail := max(cols, r.width-m.width()-3*(cols-1))
	for total(widths) > avail {
		widest := 0
		for i := range widths {
			if widths[i] > widths[widest] {
				widest = i
			}
		}
		if widths[widest] <= 1 {
			break
		}
		widths[widest]--
	}

	sep := r.o.Styles.Marker.Render(" | ")
	for ri, row := range rows {
		var line strings.Builder
		start := len(r.out.Lines)
		for i := 0; i < cols; i++ {
			if i > 0 {
				line.WriteString(sep)
			}
			used := 0
			if i < len(row) {
				for _, p := range row[i].pieces {
					room := widths[i] - used
					if room <= 0 {
						break
					}
					text := p.text
					if ansi.StringWidth(text) > room {
						text = ansi.Truncate(text, room, "")
					}
					line.WriteString(p.draw(text))
					used += ansi.StringWidth(text)
					if p.link >= 0 && r.out.Links[p.link].Line < 0 {
						r.out.Links[p.link].Line = start
					}
				}
			}
			if i < cols-1 {
				line.WriteString(strings.Repeat(" ", widths[i]-used))
			}
		}
		r.emit(m, line.String(), src)
		if ri+1 == header {
			var rule []string
			for _, w := range widths {
				rule = append(rule, strings.Repeat("-", w))
			}
			r.emit(m, r.o.Styles.Marker.Render(strings.Join(rule, "-+-")), src)
		}
	}
}

func total(ws []int) int {
	t := 0
	for _, w := range ws {
		t += w
	}
	return t
}

// sourceLine is the first source line of a block, or of its first descendant
// that has lines.
func (r *renderer) sourceLine(n ast.Node) int {
	for c := n; c != nil; c = c.FirstChild() {
		if c.Type() == ast.TypeBlock && c.Lines().Len() > 0 {
			return r.lineOf(c.Lines().At(0).Start)
		}
	}
	return 0
}

func (r *renderer) lineOf(offset int) int {
	return sort.Search(len(r.lines), func(i int) bool { return r.lines[i] > offset })
}

func lineStarts(src []byte) []int {
	out := []int{0}
	for i, c := range src {
		if c == '\n' {
			out = append(out, i+1)
		}
	}
	return out
}

// cut splits a line of code into parts no wider than width.
func cut(s string, width int) []string {
	var out []string
	for ansi.StringWidth(s) > width {
		head := ansi.Truncate(s, width, "")
		if head == "" {
			_, size := utf8.DecodeRuneInString(s)
			head = s[:size]
		}
		out = append(out, head)
		s = s[len(head):]
	}
	return append(out, s)
}

func expandTabs(s string) string {
	if !strings.Contains(s, "\t") {
		return s
	}
	var b strings.Builder
	col := 0
	for _, r := range s {
		if r == '\t' {
			n := 4 - col%4
			b.WriteString(strings.Repeat(" ", n))
			col += n
			continue
		}
		b.WriteRune(r)
		col++
	}
	return b.String()
}

// plainText is the text under a node, for a link's label.
func plainText(n ast.Node, doc []byte) string {
	var b strings.Builder
	_ = ast.Walk(n, func(c ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch c := c.(type) {
		case *ast.Text:
			b.Write(c.Segment.Value(doc))
		case *ast.String:
			b.Write(c.Value)
		}
		return ast.WalkContinue, nil
	})
	return b.String()
}
