package tui

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/shakfu/gwiki/internal/vim"
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
	f.m.copy = func(string) {}
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
		case "ctrl+o":
			msgs = []tea.KeyMsg{{Type: tea.KeyCtrlO}}
		case "ctrl+@":
			msgs = []tea.KeyMsg{{Type: tea.KeyCtrlAt}}
		case "down":
			msgs = []tea.KeyMsg{{Type: tea.KeyDown}}
		case "left":
			msgs = []tea.KeyMsg{{Type: tea.KeyLeft}}
		case "right":
			msgs = []tea.KeyMsg{{Type: tea.KeyRight}}
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

// flat is a view with runs of spaces collapsed, so a test can name a row
// without its column widths.
func flat(s string) string {
	return regexp.MustCompile(` +`).ReplaceAllString(s, " ")
}

func (f *wikiFixture) page() string {
	if f.m.cur == nil {
		return ""
	}
	return f.m.cur.info.Path
}

// cursorTo puts the buffer's cursor at the first occurrence of text in a
// 1-based line.
func (f *wikiFixture) cursorTo(line int, text string) {
	f.t.Helper()
	l := f.m.edit.ed.Buf.LineString(line - 1)
	col := strings.Index(l, text)
	if col < 0 {
		f.t.Fatalf("line %d of %s lacks %q: %q", line, f.page(), text, l)
	}
	f.m.edit.ed.SetCursor(vim.Pos{Line: line - 1, Col: len([]rune(l[:col]))})
}

// message is the buffer's message, where a hook's error lands.
func (f *wikiFixture) message() string {
	if f.m.edit == nil {
		return ""
	}
	return f.m.edit.ed.Message
}

func TestWikiOpensTheIndexWithTreeAndPanel(t *testing.T) {
	f := newWikiFixture(t)
	if f.page() != "index" {
		t.Fatalf("opened %q", f.page())
	}
	v := f.view()
	for _, want := range []string{"▾ lexer/", "Design sketch", "Grammar", "   1 # Home", "Start at [[Design sketch]]", "- [ ] write docs", "✗ 1 broken  4 links  1 backlink", "── 1 backlink from 1 page ──"} {
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
	f.m.focus = focusContent

	// ctrl-] follows the link under the cursor; ctrl-o comes back to it.
	f.cursorTo(3, "Design")
	f.press("ctrl+]")
	if f.page() != "lexer/design-sketch" || f.m.focus != focusContent {
		t.Fatalf("followed to %q: %s", f.page(), f.message())
	}
	f.press("ctrl+o")
	if f.page() != "index" || f.m.edit.ed.Cursor.Line != 2 {
		t.Fatalf("back to %q at %+v", f.page(), f.m.edit.ed.Cursor)
	}

	// A heading anchor puts the cursor on the heading.
	f.cursorTo(3, "the grammar")
	f.press("ctrl+]")
	if f.page() != "lexer/grammar" {
		t.Fatalf("followed to %q", f.page())
	}
	if line := f.m.edit.ed.Buf.LineString(f.m.edit.ed.Cursor.Line); line != "## Rules" || !strings.Contains(f.view(), "## Rules") {
		t.Fatalf("cursor on %q:\n%s", line, f.view())
	}
	f.press("ctrl+o")

	// A broken link says so; :fix lists repairs.
	f.cursorTo(5, "Missing")
	f.press("ctrl+]")
	if !strings.Contains(f.message(), "missing-page") {
		t.Fatalf("message after a broken link = %q", f.message())
	}
	f.press(":fix", "enter")
	if f.m.screen != screenOffers || len(f.m.offers) == 0 || f.m.offers[0].New != "Missing pages" {
		t.Fatalf("offers = %+v", f.m.offers)
	}
	f.press("esc")

	// A file link runs the editor at the line.
	f.cursorTo(5, "code")
	f.press("ctrl+]")
	if !strings.Contains(f.message(), "no editor") {
		t.Fatalf("without an editor: %q", f.message())
	}
	f.m.getenv = func(k string) string {
		if k == "EDITOR" {
			return "true"
		}
		return ""
	}
	if cmd := f.press("ctrl+]"); cmd == nil {
		t.Fatal("following a file link ran nothing")
	}

	// Undoing every change leaves nothing unsaved.
	f.press("x", "u")
	if f.m.edit.ed.Dirty {
		t.Fatal("undo back to the saved text still counts as unsaved")
	}

	// A page with unsaved changes is not left.
	f.cursorTo(1, "Home")
	f.press("x")
	f.cursorTo(3, "Design")
	f.press("ctrl+]")
	if f.page() != "index" || !strings.Contains(f.message(), "unsaved changes") {
		t.Fatalf("left a modified page for %q: %q", f.page(), f.message())
	}
}

// < and > jump between links, wrapping; enter follows the one under the
// cursor; [ and ] move half a screen; VISUAL > still indents.
func TestWikiLinkKeys(t *testing.T) {
	f := newWikiFixture(t)
	f.m.focus = focusContent
	at := func(want string) {
		t.Helper()
		ed := f.m.edit.ed
		line := []rune(ed.Buf.LineString(ed.Cursor.Line))
		if got := string(line[ed.Cursor.Col:]); !strings.HasPrefix(got, want) {
			t.Fatalf("cursor at %q, want %q", got, want)
		}
	}

	f.press(">")
	at("[[Design sketch]]")
	if status := flat(stripANSI(f.m.viewWikiStatus())); !strings.Contains(status, "NORMAL index → [[Design sketch]] lexer/design-sketch") {
		t.Errorf("status on a link: %q", status)
	}
	f.press(">")
	at("[the grammar]")
	f.press(">")
	at("[[Missing page]]")
	if status := flat(stripANSI(f.m.viewWikiStatus())); !strings.Contains(status, "→ [[Missing page]] missing page") {
		t.Errorf("status on a broken link: %q", status)
	}
	f.press(">", ">")
	at("[[Design sketch]]")
	if !strings.Contains(f.message(), "wrapped") {
		t.Errorf("no message on wrapping: %q", f.message())
	}
	f.press("<")
	at("[code]")
	f.press("<", "<", "enter")
	if f.page() != "lexer/grammar" || f.m.edit.ed.Buf.LineString(f.m.edit.ed.Cursor.Line) != "## Rules" {
		t.Fatalf("enter on [the grammar] reached %q", f.page())
	}

	// ] and [ move half a screen.
	f.press("g", "g", "]")
	if line := f.m.edit.ed.Cursor.Line; line == 0 {
		t.Fatal("] did not move down")
	}
	f.press("[")
	if line := f.m.edit.ed.Cursor.Line; line != 0 {
		t.Fatalf("[ left the cursor on line %d", line)
	}

	// enter off a link moves down a line.
	f.press("enter")
	if f.page() != "lexer/grammar" || f.m.edit.ed.Cursor.Line != 1 {
		t.Fatalf("enter off a link: %q line %d", f.page(), f.m.edit.ed.Cursor.Line)
	}

	f.press("ctrl+p", "missing", "enter", ">")
	if f.message() != "no links on this page" {
		t.Errorf("> without links: %q", f.message())
	}

	// In VISUAL mode > indents, as in vim.
	f.press("V", ">")
	if got := f.m.edit.ed.Buf.LineString(0); got != "  # Missing pages" {
		t.Errorf("V > gave %q", got)
	}
}

// A directory's README is drawn on the directory's row: enter opens it, and
// space folds the directory.
func TestWikiTreeDirectoryPage(t *testing.T) {
	f := newWikiFixture(t)
	f.write("lexer/README", "# The lexer\n")
	f.write("about", "# About\n")
	f.m.Update(pollMsg{})
	if r := f.m.rows[0]; r.dir != "" || f.m.pages[r.page].Path != "index" {
		t.Fatalf("the tree does not start with the home page: %+v", r)
	}
	f.m.screen, f.m.focus = screenRead, focusTree

	row := -1
	for i, r := range f.m.rows {
		if r.dir == "lexer" {
			row = i
		}
		if r.dir == "" && f.m.pages[r.page].Path == "lexer/README" {
			t.Fatal("the README has a row of its own")
		}
	}
	if row < 0 || !f.m.rows[row].hasPage || !strings.Contains(f.view(), "▾ The lexer") {
		t.Fatalf("no directory row with its README:\n%s", f.view())
	}

	// left folds, right unfolds and then steps in, left goes back up.
	f.m.treeCursor = row
	f.press("left")
	if !f.m.collapsed["lexer"] || f.m.treeCursor != row {
		t.Fatalf("left did not fold the directory:\n%s", f.view())
	}
	f.press("right")
	if f.m.collapsed["lexer"] || f.m.treeCursor != row {
		t.Fatalf("right did not unfold the directory:\n%s", f.view())
	}
	f.press("right")
	if f.m.treeCursor != row+1 || f.m.rows[row+1].depth != 1 {
		t.Fatalf("right on an unfolded directory went to row %d", f.m.treeCursor)
	}
	f.press("h")
	if f.m.treeCursor != row || f.m.collapsed["lexer"] {
		t.Fatalf("h on a page went to row %d", f.m.treeCursor)
	}
	f.press("j", "l")
	if f.m.focus != focusContent || !strings.HasPrefix(f.page(), "lexer/") {
		t.Fatalf("l on a page opened %q", f.page())
	}
	f.m.screen, f.m.focus = screenRead, focusTree
	f.m.treeCursor = row

	f.press("space")
	for _, r := range f.m.rows {
		if r.dir == "" && strings.HasPrefix(f.m.pages[r.page].Path, "lexer/") {
			t.Fatalf("space did not fold:\n%s", f.view())
		}
	}
	if !f.m.collapsed["lexer"] || !strings.Contains(f.view(), "▸ The lexer") {
		t.Fatalf("the folded row:\n%s", f.view())
	}
	f.press("enter")
	if f.page() != "lexer/README" || f.m.focus != focusContent {
		t.Fatalf("enter on the directory opened %q", f.page())
	}
	f.press(":", "w", "enter") // nothing to write; stays on the page
	if !strings.Contains(f.view(), "▾ The lexer") && !strings.Contains(f.view(), "▸ The lexer") {
		t.Fatalf("the directory row lost its title:\n%s", f.view())
	}
}

func TestWikiBacklinksPanel(t *testing.T) {
	f := newWikiFixture(t)
	f.press("ctrl+p", "design", "enter", "tab")
	if f.m.focus != focusPanel || !strings.Contains(flat(f.view()), "← Home index") {
		t.Fatalf("panel:\n%s", f.view())
	}
	f.press("enter")
	if f.page() != "index" || f.m.focus != focusContent {
		t.Fatalf("backlink opened %q", f.page())
	}
	// tab goes round the panes: page, panel, tree.
	f.press("tab", "tab")
	if f.m.focus != focusTree {
		t.Fatalf("two tabs from the page reached %d", f.m.focus)
	}
	f.press("shift+tab")
	if f.m.focus != focusPanel {
		t.Fatalf("shift-tab from the tree reached %d", f.m.focus)
	}
}

// The backlinks panel appears only for a page something links to, with a row
// per linking page; a page's front matter shows above it.
func TestWikiPanelAndFrontMatter(t *testing.T) {
	f := newWikiFixture(t)
	f.write("tasks/ship", "---\ntitle: Ship it\ntype: task\nstatus: open\npriority: high\ndue: 2026-09-01\ntags: [release]\n---\n\nSee [[Grammar]] and [[Grammar#Rules]].\n")
	f.m.Update(pollMsg{})
	f.m.focus = focusContent

	f.press("ctrl+p", "ship", "enter")
	v := flat(f.view())
	if strings.Contains(v, "│ task open") || f.m.contentHeight() != f.m.bodyHeight() {
		t.Errorf("a front matter line beside the front matter itself:\n%s", v)
	}
	f.press(":preview", "enter")
	v = flat(f.view())
	if !strings.Contains(v, "│ task open !high due 2026-09-01 ✗ #release") {
		t.Errorf("no front matter line over the preview:\n%s", v)
	}
	f.press("esc")
	if strings.Contains(v, "backlink from") || f.m.contentHeight() != f.m.bodyHeight() {
		t.Errorf("a page nothing links to has a panel (page %d of %d):\n%s", f.m.contentHeight(), f.m.bodyHeight(), v)
	}
	f.press(":backlinks", "enter")
	if f.m.focus == focusPanel || f.message() != "no page links here" {
		t.Errorf(":backlinks without backlinks: focus %d, message %q", f.m.focus, f.message())
	}

	f.press("ctrl+p", "grammar", "enter")
	v = flat(f.view())
	for _, want := range []string{"── 3 backlinks from 2 pages ──", "← Ship it tasks/ship ×2", "← Home index"} {
		if !strings.Contains(v, want) {
			t.Errorf("panel lacks %q:\n%s", want, v)
		}
	}
	if f.m.panelHeight() != 3 {
		t.Errorf("panel height %d, want a title and 2 rows", f.m.panelHeight())
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
	if f.m.screen != screenBroken || len(f.m.broken) != 1 || !strings.Contains(flat(f.view()), "[[Missing page]] missing page index:5") {
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
	if len(f.m.tasks) != 1 || !strings.Contains(flat(f.view()), "☐ write docs index:7") {
		t.Fatalf("tasks:\n%s", f.view())
	}
	f.press("space")
	if !strings.Contains(f.source("index"), "- [x] write docs") || len(f.m.tasks) != 0 {
		t.Fatalf("after toggling:\n%s", f.source("index"))
	}
	f.press("a")
	if len(f.m.tasks) != 1 || !strings.Contains(flat(f.view()), "☑ write docs") {
		t.Fatalf("all tasks:\n%s", f.view())
	}
	f.press("enter")
	if f.page() != "index" || f.m.screen != screenRead {
		t.Fatalf("opened %q", f.page())
	}
}

func TestWikiNewPageAndMove(t *testing.T) {
	f := newWikiFixture(t)
	f.m.focus = focusContent
	f.cursorTo(3, "Design")
	f.press("ctrl+]") // lexer/design-sketch

	f.press(":new", "enter")
	if !strings.Contains(f.view(), "new page in lexer/, title:") {
		t.Fatalf("prompt:\n%s", f.view())
	}
	f.press("Token list", "enter")
	if f.page() != "lexer/token-list" || f.source("lexer/token-list") != "# Token list\n" {
		t.Fatalf("new page %q", f.page())
	}
	f.press(":new Tokens again", "enter")
	if f.page() != "lexer/tokens-again" {
		t.Fatalf(":new with a title opened %q", f.page())
	}

	f.press("ctrl+o", "ctrl+o", ":mv", "enter")
	if f.page() != "lexer/design-sketch" || f.m.prompt == nil || f.m.input.String() != "lexer/design-sketch" {
		t.Fatalf("move prompt on %q = %+v", f.page(), f.m.prompt)
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
	// The back stack follows the move, and restores the cursor on the link.
	f.press("ctrl+o")
	if f.page() != "index" {
		t.Fatalf("back reached %q", f.page())
	}
	f.press("ctrl+]")
	if f.page() != "archive/design-sketch" {
		t.Fatalf("[[Design sketch]] now reaches %q: %s", f.page(), f.message())
	}

	// A refused move reports why and writes nothing.
	f.press(":mv index", "enter")
	if !f.m.statusErr || f.page() != "archive/design-sketch" {
		t.Fatalf("moving onto a page: %q", f.m.status)
	}
}

func TestWikiPollPicksUpOutsideEdits(t *testing.T) {
	f := newWikiFixture(t)
	f.m.focus = focusContent
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
	f.m.focus = focusContent
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
	f.m.Update(f.press(":external", "enter")())
	if f.source("index") != original+"Added.\n" || !strings.Contains(f.view(), "Added.") {
		t.Fatalf("after the edit: %q", f.source("index"))
	}

	// Someone saves while the editor is open.
	run := f.press(":external", "enter")
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
	fromTree := func(keys ...string) func() {
		return func() {
			f.m.screen, f.m.focus, f.m.prompt = screenRead, focusTree, nil
			f.press(keys...)
		}
	}
	screens := []func(){
		func() { f.m.screen, f.m.focus = screenRead, focusTree },
		func() { f.m.screen, f.m.focus = screenRead, focusContent },
		func() { f.m.screen, f.m.focus = screenRead, focusPanel },
		fromTree("/", "lexer"),
		fromTree("ctrl+p"),
		fromTree("c"),
		fromTree("t"),
		fromTree("?"),
		fromTree("O"),
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

// Without colour, as under NO_COLOR, the selection, a selected link and the
// editor's cursor still show.
func TestWikiSelectionShowsWithoutColour(t *testing.T) {
	r := lipgloss.NewRenderer(io.Discard)
	r.SetColorProfile(termenv.Ascii)
	setTheme(r)
	t.Cleanup(func() { setTheme(term) })
	reverse := regexp.MustCompile(`\x1b\[(?:[0-9;]*;)?7m`)

	f := newWikiFixture(t)
	startsWith := func(what, mark string) {
		t.Helper()
		for _, l := range strings.Split(f.view(), "\n") {
			if strings.HasPrefix(l, mark) {
				return
			}
		}
		t.Errorf("%s: no row starts with %q:\n%s", what, mark, f.view())
	}

	f.m.screen, f.m.focus = screenRead, focusTree
	startsWith("tree", markSelected)
	if lines := strings.Split(f.m.View(), "\n"); !reverse.MatchString(lines[0]) || !reverse.MatchString(lines[len(lines)-1]) {
		t.Error("the header and status bars are not drawn in reverse video")
	}
	f.m.focus = focusContent
	startsWith("tree behind the page", markInactive)
	if !reverse.MatchString(strings.Join(strings.Split(f.m.View(), "\n")[1:3], "\n")) {
		t.Error("the buffer's cursor is not drawn in reverse video")
	}
	f.m.focus = focusTree
	f.press("t")
	startsWith("tasks", markSelected)
	// The active tab is bold and out of the bar's reverse video.
	header := strings.Split(f.m.View(), "\n")[0]
	active := regexp.MustCompile(`\x1b\[([0-9;]*)m tasks `).FindStringSubmatch(header)
	if active == nil || strings.Contains(";"+active[1]+";", ";7;") || !strings.Contains(";"+active[1]+";", ";1;") {
		t.Errorf("the active tab is not set apart without colour: %q", header)
	}
	f.press("O")
	startsWith("overview", markSelected)
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
	if !isTab(m.screen) {
		t.Fatalf("keys on an empty overview left it for screen %d", m.screen)
	}
	m.showTab(screenLatest)
	if v := stripANSI(m.View()); !strings.Contains(v, "no pages yet; n creates one") {
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

// The overview is three tabs, latest, tasks and stats, that tab and shift-tab
// move between.
func TestWikiOverview(t *testing.T) {
	f := newWikiFixture(t)
	f.write("tasks/ship", "---\ntitle: Ship it\ntype: task\ndue: 2026-09-01\ntags: [release]\n---\n")
	f.write("later", "# Later\n\n- [ ] tidy up due:2026-09-20\n\n[[Design sketch]]\n")
	f.m.Update(pollMsg{})
	f.m.width = 120
	has := func(tab string, wants ...string) {
		t.Helper()
		v := flat(f.view())
		for _, want := range wants {
			if !strings.Contains(v, want) {
				t.Errorf("%s lacks %q", tab, want)
			}
		}
		if t.Failed() {
			t.Fatalf("%s:\n%s", tab, f.view())
		}
	}

	f.press("O")
	has("latest", " latest tasks stats ", "7 pages", "PAGE PATH CHANGED", "LATEST", "1/7")
	f.press("enter")
	if f.m.screen != screenRead || f.page() == "" {
		t.Fatalf("enter on a recent change: screen %d, page %q", f.m.screen, f.page())
	}

	f.press(":overview", "enter", "tab")
	if f.m.screen != screenTasks {
		t.Fatalf("tab from latest reached screen %d", f.m.screen)
	}
	has("tasks", "3 open · 1 overdue · 1 due within a week", "☐ Ship it tasks/ship 2026-09-01 ✗", "☐ tidy up later:3 2026-09-20", "TASKS")

	f.press("tab")
	has("stats", "Health", "✗ broken links 1", "orphan pages", "dead ends", "Directories 7 pages", "lexer/ 2", "#release 1", "Most linked", "Design sketch ← 2", "STATS")
	f.press("tab")
	if f.m.screen != screenLatest {
		t.Fatalf("tab from stats reached screen %d", f.m.screen)
	}
	f.press("shift+tab")
	if f.m.screen != screenStats {
		t.Fatalf("shift-tab from latest reached screen %d", f.m.screen)
	}

	// Health opens its lists, and esc comes back to stats.
	f.press("g", "enter")
	if f.m.screen != screenBroken || len(f.m.broken) != 1 {
		t.Fatalf("broken from stats: screen %d", f.m.screen)
	}
	f.press("esc")
	if f.m.screen != screenStats {
		t.Fatalf("esc from a list opened on stats went to %d", f.m.screen)
	}
	f.press("j", "enter")
	if f.m.screen != screenPages || !strings.Contains(f.view(), "orphan pages") {
		t.Fatalf("orphans:\n%s", f.view())
	}
	f.press("esc")

	// A tag, in the right column, lists its pages.
	f.press("l")
	for i := 0; i < 20 && !strings.Contains(stripANSI(f.m.statsColumns()[1][selectable(f.m.statsColumns()[1])[f.m.homeRow[1]]].text), "#release"); i++ {
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

	// :overview returns to the last tab; narrow, stats is one column.
	f.press(":overview", "enter")
	f.m.width = 60
	if f.m.screen != screenStats {
		t.Fatalf(":overview reached screen %d", f.m.screen)
	}
	has("narrow stats", "Health", "Directories", "Tags")
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
	f.m.focus = focusContent
	v := f.view()
	if !strings.Contains(v, "NORMAL  index") || !strings.Contains(v, "   1 # Home") {
		t.Fatalf("buffer:\n%s", v)
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
		t.Fatal("the status bar does not show unsaved changes")
	}

	// :q refuses, :w writes, :q quits gwiki.
	f.press(":q", "enter")
	if f.m.quitting || !strings.Contains(f.view(), "unsaved changes") {
		t.Fatalf(":q with changes:\n%s", f.view())
	}
	f.press(":w", "enter")
	if !strings.HasSuffix(f.source("index"), "\n- [ ] A new line.\n") || f.m.edit.ed.Dirty {
		t.Fatalf("after :w:\n%s", f.source("index"))
	}
	f.press(":q", "enter")
	if !f.m.quitting {
		t.Fatal(":q did not quit")
	}
}

func TestWikiEditorDraftsAndConflicts(t *testing.T) {
	f := newWikiFixture(t)
	f.m.focus = focusContent

	// A page saved elsewhere while the buffer is clean reloads.
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
	if err := f.m.load("index"); err != nil {
		t.Fatal(err)
	}
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
	f.m.focus = focusContent

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
		t.Fatalf("ctrl-] reached %q on screen %d: %s", f.page(), f.m.screen, f.message())
	}

	// A broken link is underlined and named by :check.
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
	f.m.focus = focusContent
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
// The editor's status bar keeps the mode, the page, the unsaved mark and the
// position while a message shows.
func TestWikiEditorStatusKeepsItsFields(t *testing.T) {
	f := newWikiFixture(t)
	f.press("ctrl+p", "index", "enter", "i", "x")
	f.m.edit.ed.Message, f.m.edit.ed.Err = "nothing to complete", true
	line := stripANSI(f.m.viewWikiStatus())
	for _, want := range []string{"INSERT", "index", "[+]", "nothing to complete", "1:2"} {
		if !strings.Contains(line, want) {
			t.Errorf("status lacks %q: %q", want, line)
		}
	}
}

// "+y puts text in the system clipboard.
func TestWikiEditorCopiesToTheClipboard(t *testing.T) {
	f := newWikiFixture(t)
	var copied string
	f.m.copy = func(text string) { copied = text }
	f.m.focus = focusContent
	f.press("\"+yy")
	if copied != "# Home\n" {
		t.Fatalf("copied %q", copied)
	}
}

// The preview draws a broken link as broken.
func TestWikiPreviewMarksBrokenLinks(t *testing.T) {
	r := lipgloss.NewRenderer(io.Discard)
	r.SetColorProfile(termenv.ANSI256)
	r.SetHasDarkBackground(true)
	setTheme(r)
	t.Cleanup(func() { setTheme(term) })

	f := newWikiFixture(t)
	f.m.focus = focusContent
	f.press(":preview", "enter")
	v := f.m.View()
	broken := regexp.MustCompile(`38;5;217[0-9;]*mM`)
	working := regexp.MustCompile(`38;5;117[0-9;]*mD`)
	if !broken.MatchString(v) || !working.MatchString(v) {
		t.Fatalf("preview does not tell broken links from working ones:\n%q", v)
	}
}

func TestWikiEditorTakesAPaste(t *testing.T) {
	f := newWikiFixture(t)
	f.m.focus = focusContent
	f.press("G", "o")
	f.m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("pasted text")})
	f.press("esc")
	if !strings.HasSuffix(f.m.edit.ed.Text(), "pasted text\n") {
		t.Fatalf("after the paste:\n%s", f.m.edit.ed.Text())
	}
}
