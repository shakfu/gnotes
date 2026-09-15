package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/shakfu/gnotes/internal/wiki"
)

type wikiFixture struct {
	t    *testing.T
	root string
	w    *wiki.Wiki
	m    *WikiModel
}

func newWikiFixture(t *testing.T) *wikiFixture {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	p, err := wiki.Init(root)
	if err != nil {
		t.Fatal(err)
	}
	f := &wikiFixture{t: t, root: root}
	filler := strings.Repeat("Filler line.\n\n", 30)
	for name, body := range map[string]string{
		"index":               "# Home\n\nStart at [[Design sketch]] or [the grammar](lexer/grammar.md#rules).\n\nSee [[Missing page]] and [code](../../main.go#L1).\n\n- [ ] write docs\n",
		"lexer/design-sketch": "---\ntitle: Design sketch\n---\n\nThe lexer tokenizes input.\n\n## Tokens\n\nBack to [[index]].\n",
		"lexer/grammar":       "# Grammar\n\n" + filler + "## Rules\n\nNothing yet.\n",
		"missing-pages":       "# Missing pages\n",
		"odd":                 "# odd\x1b]0;PWNED\x07 title\n",
	} {
		f.write(name, body)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if f.w, err = wiki.Open(p); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.w.Close() })
	if f.m, err = NewWiki(f.w); err != nil {
		t.Fatal(err)
	}
	f.m.getenv = func(string) string { return "" }
	// Programs run at once, without the terminal.
	f.m.exec = func(cmd *exec.Cmd, done tea.ExecCallback) tea.Cmd {
		return func() tea.Msg { return done(cmd.Run()) }
	}
	f.m.width, f.m.height = 100, 30
	return f
}

func (f *wikiFixture) write(page, body string) {
	f.t.Helper()
	file := filepath.Join(f.root, ".gnotes", "wiki", filepath.FromSlash(page)+".md")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *wikiFixture) source(page string) string {
	f.t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.root, ".gnotes", "wiki", filepath.FromSlash(page)+".md"))
	if err != nil {
		f.t.Fatal(err)
	}
	return string(raw)
}

// press sends keys: named keys, or text typed one rune at a time.
func (f *wikiFixture) press(keys ...string) tea.Cmd {
	f.t.Helper()
	var last tea.Cmd
	for _, k := range keys {
		var msgs []tea.KeyMsg
		switch k {
		case "enter":
			msgs = []tea.KeyMsg{{Type: tea.KeyEnter}}
		case "esc":
			msgs = []tea.KeyMsg{{Type: tea.KeyEsc}}
		case "tab":
			msgs = []tea.KeyMsg{{Type: tea.KeyTab}}
		case "shift+tab":
			msgs = []tea.KeyMsg{{Type: tea.KeyShiftTab}}
		case "backspace":
			msgs = []tea.KeyMsg{{Type: tea.KeyBackspace}}
		case "space":
			msgs = []tea.KeyMsg{{Type: tea.KeySpace}}
		case "ctrl+p":
			msgs = []tea.KeyMsg{{Type: tea.KeyCtrlP}}
		case "ctrl+u":
			msgs = []tea.KeyMsg{{Type: tea.KeyCtrlU}}
		case "down":
			msgs = []tea.KeyMsg{{Type: tea.KeyDown}}
		default:
			for _, r := range k {
				if r == ' ' {
					msgs = append(msgs, tea.KeyMsg{Type: tea.KeySpace})
				} else {
					msgs = append(msgs, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
				}
			}
		}
		for _, msg := range msgs {
			_, last = f.m.Update(msg)
		}
	}
	return last
}

func (f *wikiFixture) view() string {
	return stripANSI(f.m.View())
}

func (f *wikiFixture) page() string {
	if f.m.cur == nil {
		return ""
	}
	return f.m.cur.info.Path
}

func TestWikiOpensTheIndexWithTreeAndPanel(t *testing.T) {
	f := newWikiFixture(t)
	if f.page() != "index" {
		t.Fatalf("opened %q", f.page())
	}
	v := f.view()
	for _, want := range []string{"v lexer/", "Design sketch", "Grammar", "# Home", "Start at Design sketch or the grammar", "- [ ] write docs", "links 4  backlinks 1  broken 1", "1 broken"} {
		if !strings.Contains(v, want) {
			t.Errorf("view lacks %q:\n%s", want, v)
		}
	}
	if strings.Contains(f.m.View(), "\x1b]0;") || strings.Contains(f.m.View(), "\x07") {
		t.Fatal("a control sequence from a title reached the terminal")
	}
}

func TestWikiFollowsLinksAndGoesBack(t *testing.T) {
	f := newWikiFixture(t)
	f.m.focus = focusReader

	f.press("tab")
	if !strings.Contains(f.view(), "-> [[Design sketch]]  lexer/design-sketch") {
		t.Fatalf("panel after tab:\n%s", f.view())
	}
	f.press("enter")
	if f.page() != "lexer/design-sketch" {
		t.Fatalf("followed to %q", f.page())
	}
	f.press("backspace")
	if f.page() != "index" || f.m.cur.selected != 0 {
		t.Fatalf("back to %q, selected %d", f.page(), f.m.cur.selected)
	}

	// A heading anchor scrolls the target page to the heading.
	f.press("tab", "enter")
	if f.page() != "lexer/grammar" {
		t.Fatalf("followed to %q", f.page())
	}
	if line := f.m.doc().Headings["rules"]; f.m.cur.scroll != line || line == 0 || !strings.Contains(f.view(), "## Rules") {
		t.Fatalf("scroll = %d, heading at %d:\n%s", f.m.cur.scroll, line, f.view())
	}
	f.press("backspace")

	// A broken link says so; f lists repairs.
	f.press("tab", "enter")
	if !f.m.statusErr || !strings.Contains(f.m.status, "missing-page") {
		t.Fatalf("status after a broken link = %q", f.m.status)
	}
	f.press("f")
	if f.m.screen != screenOffers || len(f.m.offers) == 0 || f.m.offers[0].New != "Missing pages" {
		t.Fatalf("offers = %+v", f.m.offers)
	}
	f.press("esc")

	// A file link runs the editor at the line.
	f.press("tab", "enter")
	if !strings.Contains(f.m.status, "no editor") {
		t.Fatalf("without an editor: %q", f.m.status)
	}
	f.m.getenv = func(k string) string {
		if k == "EDITOR" {
			return "true"
		}
		return ""
	}
	if cmd := f.press("enter"); cmd == nil {
		t.Fatal("following a file link ran nothing")
	}

	// shift-tab wraps back to the last link.
	f.m.cur.selected = 0
	f.press("shift+tab")
	if f.m.cur.selected != 3 {
		t.Fatalf("shift-tab from the first link selected %d", f.m.cur.selected)
	}
}

func TestWikiBacklinksPanel(t *testing.T) {
	f := newWikiFixture(t)
	f.m.focus = focusReader
	f.press("tab", "enter", "b")
	if f.m.focus != focusPanel || !strings.Contains(f.view(), "<- index:3  [[Design sketch]]") {
		t.Fatalf("panel:\n%s", f.view())
	}
	f.press("enter")
	if f.page() != "index" || f.m.focus != focusReader {
		t.Fatalf("backlink opened %q", f.page())
	}
}

func TestWikiSearchAndQuickOpen(t *testing.T) {
	f := newWikiFixture(t)

	f.press("/", "tokeniz")
	if len(f.m.hits) != 1 || f.m.hits[0].Path != "lexer/design-sketch" || !strings.Contains(f.view(), "The lexer tokenizes input") {
		t.Fatalf("hits = %+v\n%s", f.m.hits, f.view())
	}
	f.press("enter")
	if f.page() != "lexer/design-sketch" || f.m.screen != screenRead {
		t.Fatalf("opened %q", f.page())
	}

	f.press("ctrl+p", "gram")
	if len(f.m.matches) != 1 || f.m.matches[0].Path != "lexer/grammar" {
		t.Fatalf("matches = %+v", f.m.matches)
	}
	f.press("backspace", "backspace", "backspace", "backspace", "mssng")
	if len(f.m.matches) != 1 || f.m.matches[0].Path != "missing-pages" {
		t.Fatalf("letters in order matched %+v", f.m.matches)
	}
	f.press("enter")
	if f.page() != "missing-pages" {
		t.Fatalf("opened %q", f.page())
	}
}

func TestWikiBrokenLinksAreRepaired(t *testing.T) {
	f := newWikiFixture(t)
	f.press("c")
	if f.m.screen != screenBroken || len(f.m.broken) != 1 || !strings.Contains(f.view(), "index:5  missing-page  [[Missing page]]") {
		t.Fatalf("broken:\n%s", f.view())
	}
	f.press("f", "enter")
	if f.m.screen != screenBroken || len(f.m.broken) != 0 || !strings.Contains(f.view(), "no broken links") {
		t.Fatalf("after the fix:\n%s", f.view())
	}
	if !strings.Contains(f.source("index"), "[[Missing pages|Missing page]]") {
		t.Fatalf("index:\n%s", f.source("index"))
	}
	f.press("esc")
	if f.m.cur.broken != 0 {
		t.Fatal("the reader still counts the repaired link")
	}
}

func TestWikiTasksToggle(t *testing.T) {
	f := newWikiFixture(t)
	f.press("t")
	if len(f.m.tasks) != 1 || !strings.Contains(f.view(), "[ ] write docs  index:7") {
		t.Fatalf("tasks:\n%s", f.view())
	}
	f.press("space")
	if !strings.Contains(f.source("index"), "- [x] write docs") || len(f.m.tasks) != 0 {
		t.Fatalf("after toggling:\n%s", f.source("index"))
	}
	f.press("a")
	if len(f.m.tasks) != 1 || !strings.Contains(f.view(), "[x] write docs") {
		t.Fatalf("all tasks:\n%s", f.view())
	}
	f.press("enter")
	if f.page() != "index" || f.m.screen != screenRead {
		t.Fatalf("opened %q", f.page())
	}
}

func TestWikiNewPageAndMove(t *testing.T) {
	f := newWikiFixture(t)
	f.m.focus = focusReader
	f.press("tab", "enter") // lexer/design-sketch

	f.press("n")
	if !strings.Contains(f.view(), "new page in lexer/, title:") {
		t.Fatalf("prompt:\n%s", f.view())
	}
	f.press("Token list", "enter")
	if f.page() != "lexer/token-list" || f.source("lexer/token-list") != "# Token list\n" {
		t.Fatalf("new page %q", f.page())
	}

	f.press("backspace", "r")
	if f.m.prompt == nil || f.m.input.String() != "lexer/design-sketch" {
		t.Fatalf("move prompt = %+v", f.m.prompt)
	}
	f.press("ctrl+u", "archive/", "enter")
	if f.m.prompt == nil || !strings.Contains(f.view(), "move to archive/design-sketch, rewriting 0 links in 0 pages? y/n") {
		t.Fatalf("confirmation:\n%s", f.view())
	}
	f.press("y", "enter")
	if f.page() != "archive/design-sketch" || !strings.Contains(f.m.status, "moved to archive/design-sketch") {
		t.Fatalf("after the move: %q, %q", f.page(), f.m.status)
	}
	if _, err := os.Stat(filepath.Join(f.root, ".gnotes", "wiki", "archive", "design-sketch.md")); err != nil {
		t.Fatal(err)
	}
	// The back stack follows the move.
	f.press("backspace", "backspace")
	if f.page() != "index" {
		t.Fatalf("back reached %q", f.page())
	}
	// Back restores the selection, [[Design sketch]].
	f.press("enter")
	if f.page() != "archive/design-sketch" {
		t.Fatalf("[[Design sketch]] now reaches %q", f.page())
	}

	// A refused move reports why and writes nothing.
	f.press("r", "ctrl+u", "index", "enter")
	if !f.m.statusErr || f.page() != "archive/design-sketch" {
		t.Fatalf("moving onto a page: %q", f.m.status)
	}
}

func TestWikiPollPicksUpOutsideEdits(t *testing.T) {
	f := newWikiFixture(t)
	f.m.focus = focusReader
	f.m.cur.scroll = 0
	f.write("index", "# Home\n\nRewritten in another editor, now longer than before.\n")
	f.write("new-page", "# Arrived\n")
	f.m.Update(pollMsg{})
	if v := f.view(); !strings.Contains(v, "Rewritten in another editor") || !strings.Contains(v, "Arrived") {
		t.Fatalf("after the poll:\n%s", v)
	}

	if err := os.Remove(filepath.Join(f.root, ".gnotes", "wiki", "index.md")); err != nil {
		t.Fatal(err)
	}
	f.m.Update(pollMsg{})
	if f.m.cur != nil || !strings.Contains(f.m.status, "index was removed") {
		t.Fatalf("after removal: %q", f.m.status)
	}
	f.view()
}

func TestWikiEditWritesAndKeepsAConflictingEdit(t *testing.T) {
	f := newWikiFixture(t)
	f.m.focus = focusReader
	script := filepath.Join(t.TempDir(), "ed.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf 'Added.\\n' >> \"$1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.m.getenv = func(k string) string {
		if k == "EDITOR" {
			return script
		}
		return ""
	}

	original := f.source("index")
	f.m.Update(f.press("e")())
	if f.source("index") != original+"Added.\n" || !strings.Contains(f.view(), "Added.") {
		t.Fatalf("after the edit: %q", f.source("index"))
	}

	// Someone saves while the editor is open.
	run := f.press("e")
	f.write("index", "# Home\n\nTheirs, saved first.\n")
	f.m.Update(run())
	if f.source("index") != "# Home\n\nTheirs, saved first.\n" {
		t.Fatalf("the other save was overwritten: %q", f.source("index"))
	}
	_, kept, found := strings.Cut(f.m.status, "your text is in ")
	if !f.m.statusErr || !found {
		t.Fatalf("status = %q", f.m.status)
	}
	defer os.Remove(kept)
	if raw, err := os.ReadFile(kept); err != nil || string(raw) != original+"Added.\nAdded.\n" {
		t.Fatalf("kept edit = %q, %v", raw, err)
	}
}

func TestWikiViewFitsTheTerminal(t *testing.T) {
	f := newWikiFixture(t)
	screens := []func(){
		func() { f.m.screen, f.m.focus = screenRead, focusTree },
		func() { f.m.screen, f.m.focus = screenRead, focusReader },
		func() { f.press("esc", "/", "lexer") },
		func() { f.press("esc", "ctrl+p") },
		func() { f.press("esc", "c") },
		func() { f.press("esc", "t") },
		func() { f.press("esc", "?") },
	}
	for _, size := range [][2]int{{100, 30}, {60, 10}, {30, 5}, {12, 3}, {1, 1}} {
		f.m.width, f.m.height = size[0], size[1]
		for i, set := range screens {
			set()
			out := f.m.View()
			lines := strings.Split(out, "\n")
			if len(lines) > size[1] {
				t.Errorf("%dx%d screen %d: %d lines", size[0], size[1], i, len(lines))
			}
			for _, l := range lines {
				if w := ansi.StringWidth(l); w > size[0] {
					t.Errorf("%dx%d screen %d: line %q is %d wide", size[0], size[1], i, stripANSI(l), w)
				}
			}
		}
	}
}

func TestWikiEmptyWiki(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	p, err := wiki.Init(root)
	if err != nil {
		t.Fatal(err)
	}
	w, err := wiki.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	m, err := NewWiki(w)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []tea.KeyMsg{{Type: tea.KeyTab}, {Type: tea.KeyEnter}, {Type: tea.KeyRunes, Runes: []rune("l")}, {Type: tea.KeyRunes, Runes: []rune("e")}, {Type: tea.KeyRunes, Runes: []rune("r")}, {Type: tea.KeyRunes, Runes: []rune("b")}, {Type: tea.KeyBackspace}} {
		m.Update(k)
	}
	if v := stripANSI(m.View()); !strings.Contains(v, "no pages; n creates one") {
		t.Fatalf("empty wiki:\n%s", v)
	}
}
