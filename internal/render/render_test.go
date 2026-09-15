package render

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// marked styles make selection and breakage visible without colour.
func marked() Styles {
	s := Styles{}
	s.Selected = lipgloss.NewStyle().Transform(func(t string) string { return "<" + t + ">" })
	s.Broken = lipgloss.NewStyle().Transform(func(t string) string { return "!" + t })
	return s
}

func render(t *testing.T, src string, width int) *Doc {
	t.Helper()
	return Render([]byte(src), Options{Width: width, Styles: marked(), Selected: -1})
}

func TestBlocks(t *testing.T) {
	src := "---\ntitle: T\n---\n\n# Top\n\nOne *two* **three** `four` ~~five~~.\nSix.\n\n" +
		"- a\n- [ ] b\n- [x] c\n  1. d\n\n> e\n> f\n\n```\ng\n\th\n```\n\n| x | yy |\n|---|----|\n| 1 | 2 |\n\n---\n\nEnd[^1].\n\n[^1]: Note.\n"
	d := render(t, src, 40)
	want := []string{
		"# Top",
		"",
		"One two three four five. Six.",
		"",
		"- a",
		"- [ ] b",
		"- [x] c",
		"  1. d",
		"",
		"| e f",
		"",
		"  g",
		"      h",
		"",
		"x | yy",
		"--+---",
		"1 | 2",
		"",
		"----------------------------------------",
		"",
		"End[^1].",
		"",
		"[^1] Note.",
	}
	if got := strings.Join(d.Lines, "\n"); got != strings.Join(want, "\n") {
		t.Fatalf("got:\n%s\nwant:\n%s", got, strings.Join(want, "\n"))
	}
	if d.Source[0] != 5 || d.Source[2] != 7 || d.Source[5] != 11 || d.Source[18] != d.Source[17] {
		t.Fatalf("source lines = %v", d.Source)
	}
}

func TestWrapKeepsWidthAndWords(t *testing.T) {
	src := "Words that go on for a while and then " + strings.Repeat("x", 30) + " and wide \u4e16\u754c\u4e16\u754c\u4e16\u754c characters.\n\n" +
		"- a list item whose text also runs past the width of the screen\n"
	// Two columns is the least that holds a wide character.
	for _, width := range []int{2, 5, 12, 25} {
		d := render(t, src, width)
		for _, l := range d.Lines {
			if w := ansi.StringWidth(l); w > width {
				t.Fatalf("width %d: line %q is %d wide", width, l, w)
			}
		}
	}
	d := render(t, src, 25)
	if d.Lines[0] != "Words that go on for a" || d.Lines[len(d.Lines)-1] != "  width of the screen" {
		t.Fatalf("wrapped:\n%s", strings.Join(d.Lines, "\n"))
	}
}

func TestLinksAndHeadings(t *testing.T) {
	src := "# Intro\n\nSee [[Design sketch#Tokens|the tokens]] and\n[code](../src/lexer.go#L42) and <https://example.com>.\n\n## Intro\n\n![diagram](img.png)\n"
	d := Render([]byte(src), Options{Width: 30, Styles: marked(), Selected: 1, Broken: func(l Link) bool { return l.Target == "img.png" }})

	type got struct {
		form, target, anchor, label string
		line                        int
	}
	var links []got
	for _, l := range d.Links {
		links = append(links, got{string(l.Form), l.Target, l.Anchor, l.Label, l.Line})
	}
	want := []got{
		{"wiki", "Design sketch", "Tokens", "the tokens", 2},
		{"markdown", "../src/lexer.go", "L42", "code", 2},
		{"auto", "https://example.com", "", "https://example.com", 3},
		{"markdown", "img.png", "", "diagram", 7},
	}
	if len(links) != len(want) {
		t.Fatalf("links = %+v", links)
	}
	for i := range want {
		if links[i] != want[i] {
			t.Errorf("link %d = %+v, want %+v", i, links[i], want[i])
		}
	}
	text := strings.Join(d.Lines, "\n")
	if !strings.Contains(text, "<code>") || !strings.Contains(text, "![image: !diagram]") {
		t.Fatalf("selected or broken not drawn:\n%s", text)
	}
	if d.Headings["intro"] != 0 || d.Headings["intro-1"] != 5 {
		t.Fatalf("headings = %v", d.Headings)
	}
}

func TestControlCharactersAreReplaced(t *testing.T) {
	d := render(t, "# a\x1b]0;x\x07b\n\n```\n\x1b[2J\n```\n\n[l\x1bx](t)\n", 40)
	for _, l := range d.Lines {
		if strings.ContainsAny(l, "\x1b\x07") {
			t.Fatalf("control character drawn: %q", l)
		}
	}
}

func TestEmptyAndOddInput(t *testing.T) {
	for _, src := range []string{"", "\n\n", "---\ntitle: x\n---\n", "|a|\n|-|\n", "<div>\nx\n</div>\n", "  - \n", "[[", "*"} {
		d := render(t, src, 10)
		if len(d.Lines) != len(d.Source) {
			t.Fatalf("%q: %d lines, %d sources", src, len(d.Lines), len(d.Source))
		}
	}
}

func BenchmarkRender(b *testing.B) {
	var sb strings.Builder
	for i := range 40 {
		sb.WriteString("## Section\n\nThe lexer tokenizes input before the parser runs. See [[Page lexer]] and [the code](../src/lexer.go#L1).\n\n- [ ] follow up\n- [x] done\n\n")
		_ = i
	}
	src := []byte(sb.String())
	o := Options{Width: 80, Styles: DefaultStyles(), Selected: 3}
	b.ReportAllocs()
	for b.Loop() {
		Render(src, o)
	}
}
