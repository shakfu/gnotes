package markdown

import (
	"strings"
	"sync"
	"testing"
)

// spans checks that each link's recorded offsets hold the text they claim.
func spans(t *testing.T, src string, l Link) {
	t.Helper()
	if l.Start >= 0 {
		raw := src[l.Start:l.End]
		if l.Form == FormWiki && !(strings.HasPrefix(raw, "[[") && strings.HasSuffix(raw, "]]")) {
			t.Errorf("wiki link span %q is not [[...]]", raw)
		}
	}
	if l.DestStart >= 0 {
		dest := src[l.DestStart:l.DestEnd]
		want := l.Target
		if l.Anchor != "" {
			want += "#" + l.Anchor
		}
		if strings.TrimSpace(dest) != want {
			t.Errorf("destination span %q, want %q", dest, want)
		}
	}
}

func TestWikiLinks(t *testing.T) {
	src := "See [[Design sketch]], [[lexer/grammar|the grammar]] and [[Design sketch#Tokens]].\n" +
		"Same page: [[#Usage]].\n"
	p := Parse([]byte(src))
	want := []Link{
		{Form: FormWiki, Target: "Design sketch", Label: "Design sketch"},
		{Form: FormWiki, Target: "lexer/grammar", Label: "the grammar"},
		{Form: FormWiki, Target: "Design sketch", Anchor: "Tokens", Label: "Design sketch#Tokens"},
		{Form: FormWiki, Anchor: "Usage", Label: "#Usage"},
	}
	if len(p.Links) != len(want) {
		t.Fatalf("got %d links: %+v", len(p.Links), p.Links)
	}
	for i, w := range want {
		l := p.Links[i]
		if l.Form != w.Form || l.Target != w.Target || l.Anchor != w.Anchor || l.Label != w.Label {
			t.Errorf("link %d = %+v, want %+v", i, l, w)
		}
		spans(t, src, l)
	}
	if l := p.Links[1]; src[l.DestStart:l.DestEnd] != "lexer/grammar" || l.Line != 1 {
		t.Errorf("labelled link destination %q on line %d", src[l.DestStart:l.DestEnd], l.Line)
	}
	if l := p.Links[3]; l.Line != 2 || l.Col != 14 {
		t.Errorf("second-line link at %d:%d, want 2:14", l.Line, l.Col)
	}
}

func TestMarkdownLinks(t *testing.T) {
	src := "Read [the sketch](design-sketch.md#tokens \"title\") and [code](../../src/lexer.go#L42-L60).\n" +
		"![flow](img/flow.png) <https://example.com> and [nested [brackets]](a.md).\n" +
		"Also [by reference][sketch].\n\n[sketch]: <design sketch.md>\n"
	p := Parse([]byte(src))
	if len(p.Links) != 6 {
		t.Fatalf("got %d links: %+v", len(p.Links), p.Links)
	}
	cases := []struct {
		form         Form
		image, ref   bool
		target, anch string
		whole        string
	}{
		{FormMarkdown, false, false, "design-sketch.md", "tokens", `[the sketch](design-sketch.md#tokens "title")`},
		{FormMarkdown, false, false, "../../src/lexer.go", "L42-L60", "[code](../../src/lexer.go#L42-L60)"},
		{FormMarkdown, true, false, "img/flow.png", "", "![flow](img/flow.png)"},
		{FormAuto, false, false, "https://example.com", "", ""},
		{FormMarkdown, false, false, "a.md", "", "[nested [brackets]](a.md)"},
		{FormMarkdown, false, true, "design sketch.md", "", ""},
	}
	for i, c := range cases {
		l := p.Links[i]
		if l.Form != c.form || l.Image != c.image || l.Ref != c.ref || l.Target != c.target || l.Anchor != c.anch {
			t.Errorf("link %d = %+v", i, l)
			continue
		}
		spans(t, src, l)
		if c.whole != "" && (l.Start < 0 || src[l.Start:l.End] != c.whole) {
			t.Errorf("link %d whole span = [%d:%d], want %q", i, l.Start, l.End, c.whole)
		}
	}
	if def := p.Links[5]; def.Line != 5 {
		t.Errorf("reference destination on line %d, want the definition on 5", def.Line)
	}
}

// Links in code are not links.
func TestCodeIsNotLinked(t *testing.T) {
	src := "Inline `[[not a link]]` and `[x](no.md)`.\n\n" +
		"```\n[[fenced]] [y](fenced.md)\n```\n\n" +
		"    [[indented]] [z](indented.md)\n\n" +
		"Escaped \\[[not either]].\n\n" +
		"[[real]]\n"
	p := Parse([]byte(src))
	if len(p.Links) != 1 || p.Links[0].Target != "real" {
		t.Fatalf("links = %+v, want only [[real]]", p.Links)
	}
}

func TestFrontMatterKeepsOffsets(t *testing.T) {
	src := "---\ntitle: Design sketch\ntags: [design, parser]\ntype: task\nstatus: doing\nassignees: sa\nextra: {nested: true}\n---\n\n" +
		"# Heading\n\nSee [[target]].\n"
	p := Parse([]byte(src))
	f := p.Front
	if f == nil || f.Title != "Design sketch" || f.Type != "task" || f.Status != "doing" ||
		strings.Join(f.Tags, ",") != "design,parser" || strings.Join(f.Assignees, ",") != "sa" || f.Raw["extra"] == nil {
		t.Fatalf("front = %+v", f)
	}
	if p.Title != "Design sketch" {
		t.Errorf("Title = %q, want the front matter title", p.Title)
	}
	if !strings.HasPrefix(src[p.BodyStart:], "\n# Heading") {
		t.Errorf("BodyStart = %d: %q", p.BodyStart, src[p.BodyStart:])
	}
	l := p.Links[0]
	spans(t, src, l)
	if l.Line != 12 || p.Headings[0].Line != 10 {
		t.Errorf("link line %d, heading line %d; want 12 and 10", l.Line, p.Headings[0].Line)
	}
}

// Only a closed block that decodes as a mapping is front matter.
func TestNotFrontMatter(t *testing.T) {
	for name, src := range map[string]string{
		"thematic break": "---\n\n# Title\n",
		"unclosed":       "---\ntitle: x\n# Title\n",
		"not a mapping":  "---\n- a\n- b\n---\n# Title\n",
		"not at start":   "\n---\ntitle: x\n---\n",
	} {
		if p := Parse([]byte(src)); p.Front != nil || p.BodyStart != 0 {
			t.Errorf("%s: front matter %+v at %d", name, p.Front, p.BodyStart)
		}
	}
	if p := Parse([]byte("\xef\xbb\xbf---\ntitle: bom\n---\nbody\n")); p.Front == nil || p.Front.Title != "bom" {
		t.Errorf("a leading BOM hid the front matter: %+v", p.Front)
	}
}

func TestHeadingsAndTitle(t *testing.T) {
	p := Parse([]byte("Intro\n\n## Tokens & `lexer`\n\n# The Title\n\n## Tokens & lexer\n\n### \u00dcber_cool - thing!\n"))
	want := []Heading{
		{2, "Tokens & lexer", "tokens--lexer", 3},
		{1, "The Title", "the-title", 5},
		{2, "Tokens & lexer", "tokens--lexer-1", 7},
		{3, "\u00dcber_cool - thing!", "\u00fcber_cool---thing", 9},
	}
	if len(p.Headings) != len(want) {
		t.Fatalf("headings = %+v", p.Headings)
	}
	for i, w := range want {
		if p.Headings[i] != w {
			t.Errorf("heading %d = %+v, want %+v", i, p.Headings[i], w)
		}
	}
	if p.Title != "The Title" {
		t.Errorf("Title = %q, want the first level-one heading", p.Title)
	}
}

func TestTasks(t *testing.T) {
	src := "- [ ] benchmark the lexer due:2026-08-21\n- [x] write the grammar\n  - [X] nested\n1. [ ] numbered\n- not a task\n\n```\n- [ ] in code\n```\n"
	p := Parse([]byte(src))
	want := []Task{
		{Line: 1, Text: "benchmark the lexer", Due: "2026-08-21"},
		{Line: 2, Text: "write the grammar", Done: true},
		{Line: 3, Text: "nested", Done: true},
		{Line: 4, Text: "numbered"},
	}
	if len(p.Tasks) != len(want) {
		t.Fatalf("tasks = %+v", p.Tasks)
	}
	for i, w := range want {
		got := p.Tasks[i]
		if got.Line != w.Line || got.Text != w.Text || got.Done != w.Done || got.Due != w.Due {
			t.Errorf("task %d = %+v, want %+v", i, got, w)
		}
		if c := src[got.Box]; (c == ' ') == got.Done || strings.IndexByte(" xX", c) < 0 {
			t.Errorf("task %d box points at %q", i, c)
		}
	}
}

// One parser serves concurrent callers. Run with -race.
func TestParseIsSafeConcurrently(t *testing.T) {
	src := []byte(benchPage(1))
	want := len(Parse(src).Links)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if n := len(Parse(src).Links); n != want {
				t.Errorf("got %d links, want %d", n, want)
			}
		}()
	}
	wg.Wait()
}

// benchPage is a page with front matter, headings, both link forms, a file
// link and checklist items, about 1.3 KB.
func benchPage(i int) string {
	var b strings.Builder
	b.WriteString("---\ntitle: Page " + itoa(i) + "\ntags: [bench, parser]\n---\n\n# Page " + itoa(i) + "\n\n")
	for s := 0; s < 4; s++ {
		b.WriteString("## Section " + itoa(s) + "\n\n")
		b.WriteString("The lexer tokenizes input before the parser runs. See [[Page " + itoa(i+s+1) + "]] and ")
		b.WriteString("[the lexer](../../src/lexer.go#L42), then [[area/page-" + itoa(i) + "#section-" + itoa(s) + "|this section]].\n")
		b.WriteString("Some `inline code` and **emphasis** to parse, with a [link](page-" + itoa(i+s) + ".md).\n\n")
		b.WriteString("- [ ] follow up on section " + itoa(s) + " due:2026-08-21\n- [x] done item\n\n")
	}
	return b.String()
}

func BenchmarkParse(b *testing.B) {
	src := []byte(benchPage(42))
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		Parse(src)
	}
}
