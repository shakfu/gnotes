package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestTableLayout(t *testing.T) {
	tb := &table{
		cols: []column{{title: "TITLE", shrink: 1, min: 6}, {title: "PATH", shrink: 2, min: 4}, {title: "EMPTY", shrink: 3}, {title: "N", right: true}},
		rows: [][]cell{
			{text("Design sketch", stylePlain), text("lexer/design-sketch", styleDim), nil, text("3", stylePlain)},
			{text("Home", stylePlain), text("index", styleDim), nil, text("12", stylePlain)},
		},
	}

	tb.layout(80)
	if got := stripANSI(tb.header()); got != " TITLE          PATH                  N" {
		t.Errorf("header %q", got)
	}
	if got := stripANSI(tb.draw(tb.rows[1])); got != " Home           index                12" {
		t.Errorf("row %q", got)
	}

	// Too narrow: the path is cut before the title.
	tb.layout(30)
	for _, r := range tb.rows {
		if w := ansi.StringWidth(tb.draw(r)); w > 30 {
			t.Errorf("row is %d wide at 30", w)
		}
	}
	if !strings.HasPrefix(stripANSI(tb.draw(tb.rows[0])), " Design sketch  lexer/d") {
		t.Errorf("cut row %q", stripANSI(tb.draw(tb.rows[0])))
	}

	// Narrower than every minimum: columns stop at their minimums.
	tb.layout(10)
	if tb.widths[0] != 6 || tb.widths[1] != 4 {
		t.Errorf("widths at 10: %v", tb.widths)
	}
}

func TestSnippetDropsMarkdown(t *testing.T) {
	in := "## Tokens | lexer | Ada | ```go See [[lexer/grammar|the \x02grammar\x03]] and [code](x.go#L1). - [ ] **bold** - item 1. first"
	got := stripANSI(snippet(in, 200))
	if want := "Tokens lexer Ada See the grammar and code. bold item first"; got != want {
		t.Errorf("snippet = %q, want %q", got, want)
	}
}

// Task rows show the due date once, in its column, marked when past.
func TestWikiTaskRows(t *testing.T) {
	f := newWikiFixture(t)
	f.write("plan", "# Plan\n\n- [ ] late due:2026-09-01\n- [ ] soon due:2026-09-20\n- [ ] read [[Design sketch|the sketch]]\n")
	f.m.Update(pollMsg{})
	f.press("t")
	v := flat(f.view())
	for _, want := range []string{"☐ late plan:3 2026-09-01 ✗", "☐ soon plan:4 2026-09-20\n", "☐ read the sketch plan:5", "TASK WHERE DUE", "1/4"} {
		if !strings.Contains(v, want) {
			t.Errorf("tasks lack %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "due:") {
		t.Errorf("a due token is shown in the text:\n%s", v)
	}
}
