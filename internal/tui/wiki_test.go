package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/shakfu/gwiki/internal/wiki"
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
	f.m.now = func() time.Time { return time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC) }
	// Most tests start in the reader; TestWikiOverview starts on the overview.
	f.m.screen, f.m.base = screenRead, screenRead
	return f
}

func (f *wikiFixture) write(page, body string) {
	f.t.Helper()
	file := filepath.Join(f.root, ".gwiki", "wiki", filepath.FromSlash(page)+".md")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *wikiFixture) source(page string) string {
	f.t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.root, ".gwiki", "wiki", filepath.FromSlash(page)+".md"))
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
		case "ctrl+n":
			msgs = []tea.KeyMsg{{Type: tea.KeyCtrlN}}
		case "ctrl+]":
			msgs = []tea.KeyMsg{{Type: tea.KeyCtrlCloseBracket}}
		case "ctrl+@":
			msgs = []tea.KeyMsg{{Type: tea.KeyCtrlAt}}
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
	if _, err := os.Stat(filepath.Join(f.root, ".gwiki", "wiki", "archive", "design-sketch.md")); err != nil {
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

	if err := os.Remove(filepath.Join(f.root, ".gwiki", "wiki", "index.md")); err != nil {
		t.Fatal(err)
	}
	f.m.Update(pollMsg{})
	if f.m.cur != nil || !strings.Contains(f.m.status, "index was removed") {
		t.Fatalf("after removal: %q", f.m.status)
	}
	f.view()
}

func TestWikiExternalEditorWritesAndKeepsAConflictingEdit(t *testing.T) {
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
	f.m.Update(f.press("E")())
	if f.source("index") != original+"Added.\n" || !strings.Contains(f.view(), "Added.") {
		t.Fatalf("after the edit: %q", f.source("index"))
	}

	// Someone saves while the editor is open.
	run := f.press("E")
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
		func() { f.press("esc", "O") },
		func() { f.m.screen, f.m.listed, f.m.listTitle = screenPages, f.m.pages, "all" },
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
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if v := stripANSI(m.View()); m.screen != screenHome || !strings.Contains(v, "no pages yet; n creates one") {
		t.Fatalf("empty wiki overview:\n%s", v)
	}
	m.screen, m.base = screenRead, screenRead
	for _, k := range []tea.KeyMsg{{Type: tea.KeyTab}, {Type: tea.KeyEnter}, {Type: tea.KeyRunes, Runes: []rune("e")}, {Type: tea.KeyBackspace}} {
		m.Update(k)
	}
	if v := stripANSI(m.View()); !strings.Contains(v, "no pages; n creates one") {
		t.Fatalf("empty wiki reader:\n%s", v)
	}
}

func TestWikiOverview(t *testing.T) {
	f := newWikiFixture(t)
	f.write("tasks/ship", "---\ntitle: Ship it\ntype: task\ndue: 2026-09-01\ntags: [release]\n---\n")
	f.write("later", "# Later\n\n- [ ] tidy up due:2026-09-20\n\n[[Design sketch]]\n")
	f.m.Update(pollMsg{})
	f.m.width = 120
	f.press("O")
	v := f.view()
	for _, want := range []string{
		"overview", "Recent changes", "Tasks  3 open, 1 overdue, 1 due within a week",
		"Ship it  tasks/ship  overdue 2026-09-01", "tidy up due:2026-09-20  later:3  due 2026-09-20",
		"Health", "broken links   1", "orphan pages", "dead ends", "Structure  7 pages", "lexer/  2", "#release  1",
		"most linked", "Design sketch  <- 2",
	} {
		if !strings.Contains(v, want) {
			t.Errorf("overview lacks %q", want)
		}
	}
	if t.Failed() {
		t.Fatalf("overview:\n%s", v)
	}

	// The first recent change opens its page.
	f.press("enter")
	if f.m.screen != screenRead || f.page() == "" {
		t.Fatalf("enter on a recent change: screen %d, page %q", f.m.screen, f.page())
	}

	// The health column: broken links, then orphans as a page list.
	f.press("O", "l", "enter")
	if f.m.screen != screenBroken || len(f.m.broken) != 1 {
		t.Fatalf("broken from the overview: screen %d", f.m.screen)
	}
	f.press("esc")
	if f.m.screen != screenHome {
		t.Fatalf("esc from a list opened on the overview went to %d", f.m.screen)
	}
	f.press("j", "enter")
	if f.m.screen != screenPages || !strings.Contains(f.view(), "orphan pages") {
		t.Fatalf("orphans:\n%s", f.view())
	}
	f.press("esc")

	// A tag lists its pages.
	for i := 0; i < 20 && !strings.Contains(stripANSI(f.m.homeColumns()[1][selectable(f.m.homeColumns()[1])[f.m.homeRow[1]]].text), "#release"); i++ {
		f.press("j")
	}
	f.press("enter")
	if f.m.screen != screenPages || len(f.m.listed) != 1 || f.m.listed[0].Path != "tasks/ship" {
		t.Fatalf("tag list = %+v", f.m.listed)
	}
	f.press("enter")
	if f.page() != "tasks/ship" {
		t.Fatalf("opened %q", f.page())
	}

	// Narrow: one column holding every section.
	f.press("O")
	f.m.width = 60
	if v := f.view(); !strings.Contains(v, "Recent changes") || !strings.Contains(v, "Health") {
		t.Fatalf("narrow overview:\n%s", v)
	}
}

func TestAgo(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	for d, want := range map[time.Duration]string{
		10 * time.Second: "just now", 5 * time.Minute: "5m ago", 3 * time.Hour: "3h ago",
		50 * time.Hour: "2d ago", 40 * 24 * time.Hour: "2026-08-07",
	} {
		if got := ago(now, now.Add(-d)); got != want {
			t.Errorf("ago(%v) = %q, want %q", d, got, want)
		}
	}
}

// typing sends each rune of a string as its own key.
func (f *wikiFixture) typing(text string) {
	f.t.Helper()
	for _, r := range text {
		f.press(string(r))
	}
}

func TestWikiEditorEditsAndSaves(t *testing.T) {
	f := newWikiFixture(t)
	f.m.focus = focusReader
	f.press("e")
	if f.m.screen != screenEdit || f.m.edit.page != "index" {
		t.Fatalf("e opened screen %d", f.m.screen)
	}
	v := f.view()
	if !strings.Contains(v, "NORMAL  index") || !strings.Contains(v, "   1 # Home") {
		t.Fatalf("editor:\n%s", v)
	}

	// vim keys edit the buffer.
	f.press("G", "o")
	f.typing("A new line.")
	f.press("esc")
	// o on a checklist item continues the list.
	if got := f.m.edit.ed.Text(); !strings.HasSuffix(got, "\n- [ ] A new line.\n") {
		t.Fatalf("after typing:\n%s", got)
	}
	if !f.m.edit.ed.Dirty || !strings.Contains(f.view(), "[+]") {
		t.Fatal("the editor does not show unsaved changes")
	}

	// :q refuses, :w writes, :q leaves.
	f.press(":")
	f.typing("q")
	f.press("enter")
	if f.m.screen != screenEdit || !strings.Contains(f.view(), "unsaved changes") {
		t.Fatalf(":q with changes:\n%s", f.view())
	}
	f.press(":")
	f.typing("w")
	f.press("enter")
	if !strings.HasSuffix(f.source("index"), "\n- [ ] A new line.\n") || f.m.edit.ed.Dirty {
		t.Fatalf("after :w:\n%s", f.source("index"))
	}
	f.press(":")
	f.typing("q")
	f.press("enter")
	if f.m.screen != screenRead || f.m.edit != nil {
		t.Fatalf(":q left screen %d", f.m.screen)
	}
	if !strings.Contains(f.view(), "A new line.") {
		t.Fatalf("the reader does not show the edit:\n%s", f.view())
	}
}

func TestWikiEditorDraftsAndConflicts(t *testing.T) {
	f := newWikiFixture(t)
	f.m.focus = focusReader

	// A page saved elsewhere while the buffer is clean reloads.
	f.press("e")
	f.write("index", "# Home\n\nRewritten elsewhere.\n")
	f.m.Update(pollMsg{})
	if !strings.Contains(f.m.edit.ed.Text(), "Rewritten elsewhere") {
		t.Fatalf("a clean buffer did not reload:\n%s", f.m.edit.ed.Text())
	}

	// With unsaved changes it warns, and :w refuses until forced.
	f.press("x")
	f.write("index", "# Home\n\nAnd again.\n")
	f.m.Update(pollMsg{})
	if !f.m.edit.outside || !strings.Contains(f.view(), "changed on disk") {
		t.Fatalf("no warning:\n%s", f.view())
	}
	f.press(":")
	f.typing("w")
	f.press("enter")
	if f.source("index") != "# Home\n\nAnd again.\n" {
		t.Fatalf(":w overwrote the other save:\n%s", f.source("index"))
	}
	if !strings.Contains(f.view(), ":w! overwrites") {
		t.Fatalf("conflict message:\n%s", f.view())
	}
	f.press(":")
	f.typing("w!")
	f.press("enter")
	if got := f.source("index"); got != f.m.edit.ed.Text() || strings.Contains(got, "And again") {
		t.Fatalf("after :w!:\n%s", got)
	}

	// A draft is written as the buffer changes, and offered on reopening.
	f.press("o")
	f.typing("draft text")
	f.press("esc")
	f.m.Update(pollMsg{})
	drafts, err := os.ReadDir(filepath.Join(f.root, ".gwiki", "drafts"))
	if err != nil || len(drafts) != 1 {
		t.Fatalf("drafts = %v, %v", drafts, err)
	}
	f.m.edit = nil
	f.m.screen = screenRead
	f.press("e")
	if f.m.prompt == nil || !strings.Contains(f.view(), "restore it? y/n") {
		t.Fatalf("no draft offer:\n%s", f.view())
	}
	f.typing("y")
	f.press("enter")
	if !strings.Contains(f.m.edit.ed.Text(), "draft text") {
		t.Fatalf("draft not restored:\n%s", f.m.edit.ed.Text())
	}
	f.press(":")
	f.typing("w")
	f.press("enter")
	if _, err := os.Stat(filepath.Join(f.root, ".gwiki", "drafts", draftName("index"))); !os.IsNotExist(err) {
		t.Fatal("the draft outlived the save")
	}
}

func TestWikiEditorLinksAndCompletion(t *testing.T) {
	f := newWikiFixture(t)
	f.m.focus = focusReader
	f.press("e")

	// Completion after [[ replaces what was typed.
	f.press("G", "o")
	f.typing("see [[desi")
	f.press("ctrl+n")
	if !strings.Contains(f.m.edit.ed.Text(), "see [[Design sketch") {
		t.Fatalf("completion:\n%s", f.m.edit.ed.Text())
	}
	f.typing("]]")
	f.press("esc")

	// ctrl-] follows the link under the cursor, after saving.
	f.press(":")
	f.typing("w")
	f.press("enter")
	f.press("0", "f", "D", "ctrl+]")
	if f.m.screen != screenRead || f.page() != "lexer/design-sketch" {
		t.Fatalf("ctrl-] reached %q on screen %d: %s", f.page(), f.m.screen, f.m.status)
	}

	// A broken link is underlined and named by :check.
	f.press("e")
	f.press("G", "o")
	f.typing("[[Nowhere at all]]")
	f.press("esc")
	f.m.checkLinks()
	if !f.m.edit.broken["[[Nowhere at all]]"] {
		t.Fatalf("broken = %v", f.m.edit.broken)
	}
	f.press(":")
	f.typing("check")
	f.press("enter")
	if !strings.Contains(f.view(), "1 broken link in this page") {
		t.Fatalf(":check:\n%s", f.view())
	}
}

func TestWikiEditorPreviewAndDisplay(t *testing.T) {
	f := newWikiFixture(t)
	f.m.focus = focusReader
	f.press("e")
	f.press(":")
	f.typing("preview")
	f.press("enter")
	v := f.view()
	if !strings.Contains(v, "PREVIEW") || strings.Contains(v, "   1 ") {
		t.Fatalf("preview:\n%s", v)
	}
	f.press("esc")
	if f.m.edit.preview {
		t.Fatal("esc did not leave the preview")
	}

	// Every screen size draws without overflowing.
	for _, size := range [][2]int{{100, 30}, {40, 8}, {12, 3}} {
		f.m.width, f.m.height = size[0], size[1]
		out := f.m.View()
		for _, l := range strings.Split(out, "\n") {
			if w := ansi.StringWidth(l); w > size[0] {
				t.Errorf("%dx%d: line %q is %d wide", size[0], size[1], stripANSI(l), w)
			}
		}
	}
}

// A burst of typing arrives as one message holding several runes.
func TestWikiEditorTakesAPaste(t *testing.T) {
	f := newWikiFixture(t)
	f.m.focus = focusReader
	f.press("e")
	f.press("G", "o")
	f.m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("pasted text")})
	f.press("esc")
	if !strings.HasSuffix(f.m.edit.ed.Text(), "pasted text\n") {
		t.Fatalf("after the paste:\n%s", f.m.edit.ed.Text())
	}
}
