// Package markdown reads a wiki page: its front matter, title, headings, links
// and checklist items, each with its position in the source.
//
// Positions are byte offsets into the source as given, front matter included,
// so a caller can rewrite a link in place. Nothing is rendered here.
package markdown

import (
	"bytes"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unsafe"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
)

// Form is how a link is written.
type Form string

const (
	FormWiki     Form = "wiki"     // [[target#anchor|label]]
	FormMarkdown Form = "markdown" // [label](destination), ![alt](destination)
	FormAuto     Form = "auto"     // <https://...> or a bare URL
)

// Link is one link in a page.
type Link struct {
	Form  Form
	Image bool

	// Ref marks a reference-style link. Its destination lives in the
	// reference definition, which every use of that reference shares.
	Ref bool

	Label  string
	Target string // as written, before any '#'
	Anchor string // after '#', without it

	// Start and End bound the whole link; DestStart and DestEnd bound the
	// target and anchor, which is what a rename rewrites. Either pair is -1
	// when the position is not in the source: a reference use site, or text
	// under tab-expanded indentation.
	Start, End         int
	DestStart, DestEnd int

	// Line and Col are 1-based, at DestStart, or at Start when it is known and
	// DestStart is not.
	Line, Col int
}

// Heading is a section heading.
type Heading struct {
	Level int
	Text  string
	Slug  string // GitHub's anchor for the heading, unique within the page
	Line  int
}

// Task is a checklist item.
type Task struct {
	Line int
	Text string
	Done bool
	Due  string // from an inline due:YYYY-MM-DD

	// Box is the offset of the character between the brackets, the byte a
	// toggle rewrites.
	Box int
}

// Page is what Parse reads from one page.
type Page struct {
	Front     *Front // nil without front matter
	BodyStart int

	// Title is the front matter title, else the first level-one heading, else
	// empty; the caller falls back to the file name.
	Title string

	Headings []Heading
	Links    []Link
	Tasks    []Task
}

var md = goldmark.New(goldmark.WithExtensions(extension.GFM, extension.Footnote, wikiLinks{}))

var (
	taskLine = regexp.MustCompile(`^[ \t]*(?:[-*+]|\d+[.)])[ \t]+\[([ xX])\][ \t]?(.*)$`)
	dueWord  = regexp.MustCompile(`(?:^|\s)due:(\d{4}-\d{2}-\d{2})\b`)
)

// Parse reads a page. It does not fail: markdown has no invalid input.
func Parse(src []byte) *Page {
	p := &Page{}
	p.Front, p.BodyStart = splitFront(src)

	// Front matter is blanked rather than cut, so offsets and line numbers from
	// the parser are already offsets into src.
	doc := src
	if p.BodyStart > 0 {
		doc = bytes.Clone(src)
		for i := 0; i < p.BodyStart; i++ {
			if doc[i] != '\n' {
				doc[i] = ' '
			}
		}
	}
	lines := lineStarts(src)

	root := md.Parser().Parse(text.NewReader(doc))
	slugs := map[string]int{}
	ast.Walk(root, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := n.(type) {
		case *ast.Heading:
			h := Heading{Level: n.Level, Text: plainText(n, doc)}
			if n.Lines().Len() > 0 {
				h.Line = lineOf(lines, n.Lines().At(0).Start)
			}
			h.Slug = uniqueSlug(slugs, h.Text)
			p.Headings = append(p.Headings, h)
		case *WikiLink:
			l := Link{Form: FormWiki, Label: n.Label, Target: n.Target, Anchor: n.Anchor,
				Start: n.Start, End: n.End, DestStart: n.DestStart, DestEnd: n.DestEnd}
			p.Links = append(p.Links, located(l, lines))
			return ast.WalkSkipChildren, nil
		case *ast.Link:
			p.Links = append(p.Links, located(markdownLink(doc, n.Destination, plainText(n, doc), false), lines))
		case *ast.Image:
			p.Links = append(p.Links, located(markdownLink(doc, n.Destination, plainText(n, doc), true), lines))
		case *ast.AutoLink:
			url := string(n.URL(doc))
			l := Link{Form: FormAuto, Label: url, Target: url, Start: -1, End: -1, DestStart: -1, DestEnd: -1}
			if off, ok := offsetIn(doc, n.Label(doc)); ok {
				l.DestStart, l.DestEnd = off, off+len(n.Label(doc))
			}
			p.Links = append(p.Links, located(l, lines))
		case *extast.TaskCheckBox:
			if t, ok := task(src, lines, n); ok {
				p.Tasks = append(p.Tasks, t)
			}
		}
		return ast.WalkContinue, nil
	})

	switch {
	case p.Front != nil && p.Front.Title != "":
		p.Title = p.Front.Title
	default:
		for _, h := range p.Headings {
			if h.Level == 1 {
				p.Title = h.Text
				break
			}
		}
	}
	return p
}

// markdownLink positions a [label](destination) link from its destination,
// which goldmark returns as a slice of the source when it was not unescaped
// or tab-expanded.
func markdownLink(doc, dest []byte, label string, image bool) Link {
	target, anchor := string(dest), ""
	if i := strings.IndexByte(target, '#'); i >= 0 {
		target, anchor = target[:i], target[i+1:]
	}
	l := Link{Form: FormMarkdown, Image: image, Label: label, Target: target, Anchor: anchor,
		Start: -1, End: -1, DestStart: -1, DestEnd: -1}

	off, ok := offsetIn(doc, dest)
	if !ok {
		return l
	}
	l.DestStart, l.DestEnd = off, off+len(dest)

	// A reference definition reads "[ref]: destination", an inline link
	// "](destination)", optionally with the destination in angle brackets.
	before := bytes.TrimRight(doc[:off], " \t<")
	switch {
	case bytes.HasSuffix(before, []byte(":")):
		l.Ref = true
	case bytes.HasSuffix(before, []byte("](")):
		l.Start = openingBracket(doc, len(before)-2, image)
		l.End = closingParen(doc, l.DestEnd)
	}
	return l
}

// openingBracket finds the '[' matching the ']' at close, and the '!' before
// it for an image. It returns -1 when the brackets do not balance on the way.
func openingBracket(doc []byte, close int, image bool) int {
	depth := 0
	for i := close; i >= 0; i-- {
		if i > 0 && doc[i-1] == '\\' {
			continue
		}
		switch doc[i] {
		case ']':
			depth++
		case '[':
			depth--
			if depth == 0 {
				if image && i > 0 && doc[i-1] == '!' {
					return i - 1
				}
				return i
			}
		case '\n':
			if i > 0 && doc[i-1] == '\n' {
				return -1 // a label does not span a blank line
			}
		}
	}
	return -1
}

// closingParen returns the offset just past the ')' that ends an inline link
// whose destination ends at pos, stepping over an optional quoted title.
func closingParen(doc []byte, pos int) int {
	var quote byte
	for i := pos; i < len(doc); i++ {
		c := doc[i]
		switch {
		case quote != 0:
			if c == quote && doc[i-1] != '\\' {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '(':
			quote = ')'
		case c == ')':
			return i + 1
		}
	}
	return -1
}

// offsetIn returns b's offset within src when b is a slice of src's memory.
// Equal bytes elsewhere do not count: only aliasing proves the position.
func offsetIn(src, b []byte) (int, bool) {
	if len(b) == 0 || len(src) == 0 {
		return 0, false
	}
	base := uintptr(unsafe.Pointer(unsafe.SliceData(src)))
	p := uintptr(unsafe.Pointer(unsafe.SliceData(b)))
	if p < base || p+uintptr(len(b)) > base+uintptr(len(src)) {
		return 0, false
	}
	return int(p - base), true
}

// task reads a checklist item from the source line holding its checkbox.
func task(src []byte, lines []int, n *extast.TaskCheckBox) (Task, bool) {
	block, ok := n.Parent().(interface{ Lines() *text.Segments })
	if !ok || block.Lines().Len() == 0 {
		return Task{}, false
	}
	line := lineOf(lines, block.Lines().At(0).Start)
	start := lines[line-1]
	end, _ := nextLine(src, start)
	m := taskLine.FindSubmatchIndex(trimCR(end))
	if m == nil {
		return Task{}, false
	}
	raw := trimCR(end)
	t := Task{Line: line, Done: n.IsChecked, Text: strings.TrimSpace(string(raw[m[4]:m[5]])), Box: start + m[2]}
	if d := dueWord.FindStringSubmatch(t.Text); d != nil {
		t.Due = d[1]
	}
	return t, true
}

// plainText concatenates the text under a node.
func plainText(n ast.Node, src []byte) string {
	var b strings.Builder
	_ = ast.Walk(n, func(c ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch c := c.(type) {
		case *ast.Text:
			b.Write(c.Segment.Value(src))
			if c.SoftLineBreak() || c.HardLineBreak() {
				b.WriteByte(' ')
			}
		case *ast.String:
			b.Write(c.Value)
		case *WikiLink:
			b.WriteString(c.Label)
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})
	return strings.TrimSpace(b.String())
}

// Slug is GitHub's heading anchor: lowercase, letters, digits, spaces as
// hyphens, and hyphens and underscores kept; everything else dropped.
func Slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteByte('-')
		}
	}
	return b.String()
}

// uniqueSlug suffixes a repeated slug with -1, -2, as GitHub does.
func uniqueSlug(seen map[string]int, heading string) string {
	s := Slug(heading)
	n, dup := seen[s]
	seen[s] = n + 1
	if !dup {
		return s
	}
	return s + "-" + itoa(n)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for ; n > 0; n /= 10 {
		i--
		buf[i] = byte('0' + n%10)
	}
	return string(buf[i:])
}

// located fills in Line and Col from the link's first known offset.
func located(l Link, lines []int) Link {
	off := l.DestStart
	if off < 0 {
		off = l.Start
	}
	if off >= 0 {
		l.Line = lineOf(lines, off)
		l.Col = off - lines[l.Line-1] + 1
	}
	return l
}

// lineStarts returns the offset of every line's first byte.
func lineStarts(src []byte) []int {
	out := []int{0}
	for i, c := range src {
		if c == '\n' {
			out = append(out, i+1)
		}
	}
	return out
}

// lineOf returns the 1-based line holding offset.
func lineOf(lines []int, offset int) int {
	return sort.Search(len(lines), func(i int) bool { return lines[i] > offset })
}
