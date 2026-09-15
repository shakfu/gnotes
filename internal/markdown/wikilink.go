package markdown

// The inline parser is adapted from gopherwiki's renderer.WikiLinkExtension;
// see LICENSE.gopherwiki. It differs in recording source offsets and splitting
// the heading from the target.

import (
	"bytes"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// KindWikiLink is the node kind of a [[wiki]] link.
var KindWikiLink = ast.NewNodeKind("WikiLink")

// WikiLink is a [[target#anchor|label]] link. Start and End bound the whole
// link, DestStart and DestEnd the target and anchor, in the source.
type WikiLink struct {
	ast.BaseInline
	Target, Anchor, Label string
	Start, End            int
	DestStart, DestEnd    int
}

// Kind implements ast.Node.
func (n *WikiLink) Kind() ast.NodeKind { return KindWikiLink }

// Dump implements ast.Node.
func (n *WikiLink) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, map[string]string{"Target": n.Target, "Anchor": n.Anchor, "Label": n.Label}, nil)
}

// wikiLinks adds the [[wiki]] link parser to a goldmark instance.
type wikiLinks struct{}

func (wikiLinks) Extend(m goldmark.Markdown) {
	// Ahead of the standard link parser (200), which would otherwise read the
	// outer brackets as a link label.
	m.Parser().AddOptions(parser.WithInlineParsers(util.Prioritized(wikiLinkParser{}, 199)))
}

type wikiLinkParser struct{}

func (wikiLinkParser) Trigger() []byte { return []byte{'['} }

func (wikiLinkParser) Parse(parent ast.Node, block text.Reader, pc parser.Context) ast.Node {
	line, seg := block.PeekLine()
	if len(line) < 5 || line[0] != '[' || line[1] != '[' {
		return nil
	}
	end := bytes.Index(line[2:], []byte("]]"))
	if end <= 0 {
		return nil
	}
	inner := line[2 : 2+end]
	if bytes.ContainsAny(inner, "[\n") {
		return nil
	}

	// A padded segment begins with spaces that are not in the source; only an
	// unpadded one has offsets to record.
	abs := func(i int) int { return -1 }
	if seg.Padding == 0 {
		abs = func(i int) int { return seg.Start + i }
	}

	dest := inner
	label := ""
	if i := bytes.IndexByte(inner, '|'); i >= 0 {
		dest, label = inner[:i], strings.TrimSpace(string(inner[i+1:]))
	}
	target, anchor := string(dest), ""
	if i := strings.IndexByte(target, '#'); i >= 0 {
		target, anchor = target[:i], target[i+1:]
	}
	target, anchor = strings.TrimSpace(target), strings.TrimSpace(anchor)
	if target == "" && anchor == "" {
		return nil
	}
	if label == "" {
		label = strings.TrimSpace(string(dest))
	}

	block.Advance(end + 4)
	return &WikiLink{
		Target: target, Anchor: anchor, Label: label,
		Start: abs(0), End: abs(end + 4),
		DestStart: abs(2), DestEnd: abs(2 + len(dest)),
	}
}
