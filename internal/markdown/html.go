package markdown

import (
	"bytes"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// Target is where a link leads, as the caller resolves it.
type Target struct {
	// Href replaces the link's destination. An empty Href leaves the
	// destination as written.
	Href string

	// Broken marks a link whose target is missing, for the page to style.
	Broken bool

	// Title is the browser's tooltip.
	Title string
}

// HTML renders a page's body to HTML, with front matter left out. resolve
// rewrites each link's destination; a nil resolve leaves them as written.
//
// Raw HTML in a page is escaped, not passed through: pages come from a
// repository and an agent as well as from the person reading them.
func HTML(src []byte, resolve func(Link) Target) ([]byte, error) {
	_, bodyStart := splitFront(src)
	doc := src
	if bodyStart > 0 {
		doc = bytes.Clone(src)
		for i := 0; i < bodyStart; i++ {
			if doc[i] != '\n' {
				doc[i] = ' '
			}
		}
	}
	if resolve == nil {
		resolve = func(Link) Target { return Target{} }
	}
	md := goldmark.New(
		goldmark.WithExtensions(extension.GFM, extension.Footnote, wikiLinks{}),
		goldmark.WithParserOptions(parser.WithASTTransformers(
			util.Prioritized(&linkTransformer{resolve: resolve, src: doc}, 100))),
		goldmark.WithRendererOptions(renderer.WithNodeRenderers(
			util.Prioritized(&wikiRenderer{resolve: resolve}, 100))),
	)
	var buf bytes.Buffer
	if err := md.Convert(doc, &buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// linkTransformer rewrites markdown link destinations and gives headings the
// same anchors the wiki resolves against.
type linkTransformer struct {
	resolve func(Link) Target
	src     []byte
}

func (t *linkTransformer) Transform(doc *ast.Document, reader text.Reader, pc parser.Context) {
	slugs := map[string]int{}
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := n.(type) {
		case *ast.Heading:
			id := uniqueSlug(slugs, plainText(n, t.src))
			n.SetAttributeString("id", []byte(id))
		case *ast.Link:
			t.rewrite(n, n.Destination, plainText(n, t.src), false, func(d []byte) { n.Destination = d })
		case *ast.Image:
			t.rewrite(n, n.Destination, plainText(n, t.src), true, func(d []byte) { n.Destination = d })
		}
		return ast.WalkContinue, nil
	})
}

func (t *linkTransformer) rewrite(n ast.Node, dest []byte, label string, image bool, set func([]byte)) {
	target, anchor := string(dest), ""
	if i := strings.IndexByte(target, '#'); i >= 0 {
		target, anchor = target[:i], target[i+1:]
	}
	to := t.resolve(Link{Form: FormMarkdown, Image: image, Label: label, Target: target, Anchor: anchor})
	if to.Href != "" {
		set([]byte(to.Href))
	}
	if to.Broken {
		n.SetAttributeString("class", []byte("broken"))
	}
	if to.Title != "" {
		n.SetAttributeString("title", []byte(to.Title))
	}
}

// wikiRenderer draws [[wiki]] links as anchors.
type wikiRenderer struct {
	resolve func(Link) Target
}

func (r *wikiRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(KindWikiLink, r.render)
}

func (r *wikiRenderer) render(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkSkipChildren, nil
	}
	n := node.(*WikiLink)
	to := r.resolve(Link{Form: FormWiki, Label: n.Label, Target: n.Target, Anchor: n.Anchor})
	href := to.Href
	if href == "" {
		href = n.Target
		if n.Anchor != "" {
			href += "#" + n.Anchor
		}
	}
	w.WriteString(`<a href="`)
	w.Write(util.EscapeHTML([]byte(href)))
	w.WriteString(`"`)
	if to.Broken {
		w.WriteString(` class="broken"`)
	}
	if to.Title != "" {
		w.WriteString(` title="`)
		w.Write(util.EscapeHTML([]byte(to.Title)))
		w.WriteString(`"`)
	}
	w.WriteString(`>`)
	w.Write(util.EscapeHTML([]byte(n.Label)))
	w.WriteString(`</a>`)
	return ast.WalkSkipChildren, nil
}
