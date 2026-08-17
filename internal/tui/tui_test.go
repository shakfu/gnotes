package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/shakfu/gnotes/internal/session"
	"github.com/shakfu/gnotes/internal/state"
	"github.com/shakfu/gnotes/internal/store"
	"github.com/shakfu/gnotes/internal/ulid"
)

var clock = func() time.Time { return time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC) }

// newModel builds a model over a real session on a temporary project, so the
// tests exercise the same path the program does rather than a stub.
func newModel(t *testing.T) *Model {
	t.Helper()

	p, err := store.Init(t.TempDir(), "demo", clock())
	if err != nil {
		t.Fatal(err)
	}
	s, err := session.OpenProject(p, store.Actor{ID: ulid.NewGenerator().New(), Name: "sa"})
	if err != nil {
		t.Fatal(err)
	}
	s.SetClock(clock)

	if err := s.Init("demo"); err != nil {
		t.Fatal(err)
	}

	m := New(s)
	m.now = clock
	m.width, m.height = 100, 24
	return m
}

// withContent returns a model holding two notebooks and a few entries.
func withContent(t *testing.T) *Model {
	t.Helper()
	m := newModel(t)

	work, err := m.sess.NewNotebook("work")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.sess.NewNotebook("personal"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.sess.NewNote(work.ID, "design sketch", "The lexer tokenizes input."); err != nil {
		t.Fatal(err)
	}
	if _, err := m.sess.NewTask(work.ID, "fix the lexer", ""); err != nil {
		t.Fatal(err)
	}
	m.commit("")
	m.status = ""
	return m
}

// press feeds keystrokes, one per character of a plain string or one per named
// key, and returns the model.
func press(t *testing.T, m *Model, keys ...string) *Model {
	t.Helper()

	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		case "tab":
			msg = tea.KeyMsg{Type: tea.KeyTab}
		case "space":
			msg = tea.KeyMsg{Type: tea.KeySpace}
		case "backspace":
			msg = tea.KeyMsg{Type: tea.KeyBackspace}
		case "up":
			msg = tea.KeyMsg{Type: tea.KeyUp}
		case "down":
			msg = tea.KeyMsg{Type: tea.KeyDown}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		updated, _ := m.Update(msg)
		m = updated.(*Model)
	}
	return m
}

// typeText feeds a string one rune at a time, as real typing arrives.
func typeText(t *testing.T, m *Model, text string) *Model {
	t.Helper()
	for _, r := range text {
		if r == ' ' {
			m = press(t, m, "space")
			continue
		}
		m = press(t, m, string(r))
	}
	return m
}

func TestViewRendersWithoutPanicking(t *testing.T) {
	m := withContent(t)

	for _, mode := range []mode{modeNormal, modeCommand, modeSearch, modeDetail, modeHelp} {
		m.mode = mode
		if out := m.View(); out == "" {
			t.Errorf("mode %d rendered nothing", mode)
		}
	}
}

// A tiny terminal must degrade rather than wrap into nonsense.
func TestViewSurvivesATinyTerminal(t *testing.T) {
	m := withContent(t)

	for _, size := range [][2]int{{20, 5}, {10, 3}, {1, 1}, {200, 60}} {
		m.width, m.height = size[0], size[1]
		out := m.View()
		if out == "" && size[0] > 1 {
			t.Errorf("%dx%d rendered nothing", size[0], size[1])
		}
		for _, line := range strings.Split(out, "\n") {
			// Styling adds invisible escape bytes, so the check allows for
			// them while still catching a line that genuinely overflows.
			if len([]rune(stripANSI(line))) > size[0] {
				t.Errorf("%dx%d produced an overlong line (%d cols): %q",
					size[0], size[1], len([]rune(stripANSI(line))), line)
			}
		}
	}
}

// The two panes must line up on every row, or the column separator wanders.
// Padding after styling would count escape sequences as visible characters,
// which is exactly the mistake this catches.
func TestPaneColumnsAlign(t *testing.T) {
	m := withContent(t)
	m.focus = paneNotebooks

	for _, focus := range []pane{paneNotebooks, paneEntries} {
		m.focus = focus
		lines := strings.Split(m.viewPanes(), "\n")

		var want = -1
		for i, line := range lines {
			plain := stripANSI(line)
			col := strings.IndexByte(plain, '|')
			if col < 0 {
				continue
			}
			if want < 0 {
				want = col
				continue
			}
			if col != want {
				t.Fatalf("focus %d: separator on line %d is at column %d, want %d\n%q",
					focus, i, col, want, plain)
			}
		}
		if want != notebookWidth {
			t.Fatalf("focus %d: separator at column %d, want %d", focus, want, notebookWidth)
		}
	}
}

func TestNotebooksAndEntriesAppear(t *testing.T) {
	m := withContent(t)
	out := m.View()

	for _, want := range []string{"work", "personal", "design sketch", "fix the lexer"} {
		if !strings.Contains(out, want) {
			t.Errorf("the view is missing %q:\n%s", want, out)
		}
	}
}

func TestMovementBetweenPanesAndRows(t *testing.T) {
	m := withContent(t)

	if m.focus != paneNotebooks {
		t.Fatal("focus should start on the notebooks")
	}

	m = press(t, m, "j")
	if m.notebook != 1 {
		t.Fatalf("j did not move the notebook cursor: %d", m.notebook)
	}
	// A different notebook shows a different list, so the entry cursor resets.
	if m.entry != 0 {
		t.Fatal("changing notebook left the entry cursor where it was")
	}

	m = press(t, m, "k")
	if m.notebook != 0 {
		t.Fatal("k did not move back")
	}

	m = press(t, m, "l")
	if m.focus != paneEntries {
		t.Fatal("l did not move to the entries")
	}
	m = press(t, m, "j")
	if m.entry != 1 {
		t.Fatalf("j did not move the entry cursor: %d", m.entry)
	}
	m = press(t, m, "G")
	if m.entry != len(m.entries)-1 {
		t.Fatal("G did not go to the last entry")
	}
	m = press(t, m, "g")
	if m.entry != 0 {
		t.Fatal("g did not go to the first entry")
	}
	m = press(t, m, "h")
	if m.focus != paneNotebooks {
		t.Fatal("h did not move back to the notebooks")
	}
}

// The cursor must not run off either end.
func TestCursorStaysInRange(t *testing.T) {
	m := withContent(t)
	m.focus = paneEntries

	for i := 0; i < 50; i++ {
		m = press(t, m, "j")
	}
	if m.entry != len(m.entries)-1 {
		t.Fatalf("entry cursor ran past the end: %d of %d", m.entry, len(m.entries))
	}
	for i := 0; i < 50; i++ {
		m = press(t, m, "k")
	}
	if m.entry != 0 {
		t.Fatalf("entry cursor ran past the start: %d", m.entry)
	}
}

func TestCreateANoteThroughThePrompt(t *testing.T) {
	m := withContent(t)

	m = press(t, m, "n")
	if m.mode != modePrompt {
		t.Fatal("n did not open a prompt")
	}

	m = typeText(t, m, "a new note")
	m = press(t, m, "enter")

	if m.mode != modeNormal {
		t.Fatal("the prompt did not close")
	}
	if got := m.sess.State.List(state.Filter{Text: "a new note"}, state.OrderRank); len(got) != 1 {
		t.Fatalf("the note was not created")
	}
	// The cursor lands on what was just created.
	if n := m.currentEntry(); n == nil || n.Title != "a new note" {
		t.Fatalf("the cursor is not on the new note: %+v", n)
	}
}

func TestCreateATask(t *testing.T) {
	m := withContent(t)

	m = press(t, m, "t")
	m = typeText(t, m, "a new task")
	m = press(t, m, "enter")

	n := m.currentEntry()
	if n == nil || n.Kind != state.KindTask {
		t.Fatalf("t did not create a task: %+v", n)
	}
}

// Abandoning a prompt must not write anything.
func TestEscapingAPromptCreatesNothing(t *testing.T) {
	m := withContent(t)
	before := len(m.sess.Log())

	m = press(t, m, "n")
	m = typeText(t, m, "never mind")
	m = press(t, m, "esc")

	if m.mode != modeNormal {
		t.Fatal("escape did not close the prompt")
	}
	if len(m.sess.Log()) != before {
		t.Fatal("escaping the prompt still wrote an event")
	}
}

// An empty answer is a cancellation, not an entry with a blank title.
func TestEmptyPromptAnswerCreatesNothing(t *testing.T) {
	m := withContent(t)
	before := len(m.sess.Log())

	m = press(t, m, "n", "enter")

	if len(m.sess.Log()) != before {
		t.Fatal("an empty prompt created something")
	}
}

func TestSpaceTogglesATaskDone(t *testing.T) {
	m := withContent(t)
	m.focus = paneEntries
	m.entry = indexOfTitle(m, "fix the lexer")

	m = press(t, m, "space")
	if m.sess.State.Get(m.entries[m.entry].ID).Status != state.StatusDone {
		t.Fatal("space did not mark the task done")
	}

	m = press(t, m, "space")
	if m.sess.State.Get(m.entries[m.entry].ID).Status != state.StatusOpen {
		t.Fatal("space did not toggle back to open")
	}
}

// A note has no status, and saying so is more use than doing nothing.
func TestSpaceOnANoteExplainsItself(t *testing.T) {
	m := withContent(t)
	m.focus = paneEntries
	m.entry = indexOfTitle(m, "design sketch")

	m = press(t, m, "space")
	if !strings.Contains(m.status, "note") {
		t.Fatalf("status = %q, want an explanation", m.status)
	}
}

func TestExplicitStatusKeys(t *testing.T) {
	m := withContent(t)
	m.focus = paneEntries
	m.entry = indexOfTitle(m, "fix the lexer")
	id := m.entries[m.entry].ID

	for key, want := range map[string]state.Status{
		"s": state.StatusDoing,
		"x": state.StatusDone,
		"o": state.StatusOpen,
	} {
		m = press(t, m, key)
		if got := m.sess.State.Get(id).Status; got != want {
			t.Errorf("%q gave status %v, want %v", key, got, want)
		}
	}
}

func TestSearchNarrowsAsItIsTyped(t *testing.T) {
	m := withContent(t)
	all := len(m.entries)

	m = press(t, m, "/")
	if m.mode != modeSearch {
		t.Fatal("/ did not open the search line")
	}

	// "sketch" appears in one title only; "lexer" is in the other entry's body
	// too, so it would legitimately match both.
	m = typeText(t, m, "sketch")
	if len(m.entries) == 0 {
		t.Fatal("the search found nothing")
	}
	if len(m.entries) >= all {
		t.Fatalf("the search did not narrow: %d of %d", len(m.entries), all)
	}

	// Escape restores the full list.
	m = press(t, m, "esc")
	if m.query != "" || len(m.entries) != all {
		t.Fatalf("escape did not clear the search: %d entries, query %q", len(m.entries), m.query)
	}
}

// A search spans the project, since not knowing where something is the reason
// to search for it.
func TestSearchCrossesNotebooks(t *testing.T) {
	m := withContent(t)

	personal := m.notebooks()[1]
	if _, err := m.sess.NewNote(personal.ID, "hidden treasure", ""); err != nil {
		t.Fatal(err)
	}
	m.commit("")

	// Selected notebook is "work", but the note is in "personal".
	m.notebook = 0
	m.refresh()

	m = press(t, m, "/")
	m = typeText(t, m, "treasure")

	if len(m.entries) != 1 || m.entries[0].Title != "hidden treasure" {
		t.Fatalf("the search did not cross notebooks: %v", titlesOf(m.entries))
	}
}

func TestBackspaceEditsTheSearch(t *testing.T) {
	m := withContent(t)

	m = press(t, m, "/")
	m = typeText(t, m, "sketchx")
	if len(m.entries) != 0 {
		t.Fatalf("expected no matches for a nonsense query: %v", titlesOf(m.entries))
	}

	m = press(t, m, "backspace")
	if len(m.entries) == 0 {
		t.Fatal("backspace did not re-widen the search")
	}
}

func TestCommandLineCreatesEntries(t *testing.T) {
	m := withContent(t)

	m = press(t, m, ":")
	if m.mode != modeCommand {
		t.Fatal(": did not open the command line")
	}
	m = typeText(t, m, "task from the command line")
	m = press(t, m, "enter")

	got := m.sess.State.List(state.Filter{Text: "from the command line"}, state.OrderRank)
	if len(got) != 1 || got[0].Kind != state.KindTask {
		t.Fatalf("the command did not create a task: %v", titlesOf(got))
	}
}

func TestUnknownCommandIsReportedNotFatal(t *testing.T) {
	m := withContent(t)

	m = press(t, m, ":")
	m = typeText(t, m, "nonsense")
	m = press(t, m, "enter")

	if !m.statusErr || !strings.Contains(m.status, "unknown") {
		t.Fatalf("status = %q, want an unknown-command message", m.status)
	}
	if m.mode != modeNormal {
		t.Fatal("the interface did not return to normal mode")
	}
}

func TestCommandFilterAndClear(t *testing.T) {
	m := withContent(t)
	all := len(m.entries)

	m = press(t, m, ":")
	m = typeText(t, m, "filter kind task")
	m = press(t, m, "enter")

	if len(m.entries) >= all {
		t.Fatalf("the filter did not narrow: %d of %d", len(m.entries), all)
	}
	for _, n := range m.entries {
		if n.Kind != state.KindTask {
			t.Fatalf("a %s survived a task filter", n.Kind)
		}
	}

	// Escape clears the filter once the search is already clear.
	m = press(t, m, "esc")
	if len(m.entries) != all {
		t.Fatalf("escape did not clear the filter: %d of %d", len(m.entries), all)
	}
}

func TestCommandSort(t *testing.T) {
	m := withContent(t)

	m = press(t, m, ":")
	m = typeText(t, m, "sort title")
	m = press(t, m, "enter")

	if m.order != state.OrderTitle {
		t.Fatalf("order = %v", m.order)
	}
	for i := 1; i < len(m.entries); i++ {
		if strings.ToLower(m.entries[i-1].Title) > strings.ToLower(m.entries[i].Title) {
			t.Fatalf("not sorted by title: %v", titlesOf(m.entries))
		}
	}
}

func TestCommandTagAndUntag(t *testing.T) {
	m := withContent(t)
	m.focus = paneEntries
	m.entry = indexOfTitle(m, "fix the lexer")
	id := m.entries[m.entry].ID

	m = press(t, m, ":")
	m = typeText(t, m, "tag bug")
	m = press(t, m, "enter")

	if !m.sess.State.Get(id).HasTag("bug") {
		t.Fatal("the tag was not applied")
	}

	m = press(t, m, ":")
	m = typeText(t, m, "untag bug")
	m = press(t, m, "enter")

	if m.sess.State.Get(id).HasTag("bug") {
		t.Fatal("the tag was not removed")
	}
}

func TestCommandHistoryRecall(t *testing.T) {
	m := withContent(t)

	m = press(t, m, ":")
	m = typeText(t, m, "sort title")
	m = press(t, m, "enter")

	m = press(t, m, ":")
	m = press(t, m, "up")
	if got := m.input.String(); got != "sort title" {
		t.Fatalf("history recall gave %q", got)
	}
	m = press(t, m, "down")
	if got := m.input.String(); got != "" {
		t.Fatalf("walking forward past the end gave %q, want empty", got)
	}
}

func TestCommandCompletion(t *testing.T) {
	m := withContent(t)

	m = press(t, m, ":")
	m = typeText(t, m, "note")
	m = press(t, m, "tab")

	// "note" is an alias of "new" and the only command starting that way, so
	// completion should settle on a single unambiguous name.
	if got := m.input.String(); !strings.HasPrefix(got, "no") {
		t.Fatalf("completion produced %q", got)
	}

	// A prefix shared by several commands completes only as far as they agree.
	m = press(t, m, "esc", ":")
	m = typeText(t, m, "t")
	m = press(t, m, "tab")
	if got := m.input.String(); !strings.HasPrefix("task", strings.TrimSpace(got)) {
		t.Fatalf("completion produced %q", got)
	}
}

func TestDeleteAsksBeforeActing(t *testing.T) {
	m := withContent(t)
	m.focus = paneEntries
	m.entry = indexOfTitle(m, "design sketch")
	id := m.entries[m.entry].ID

	m = press(t, m, "d")
	if m.mode != modePrompt {
		t.Fatal("d did not ask for confirmation")
	}

	// Anything but yes keeps it.
	m = typeText(t, m, "n")
	m = press(t, m, "enter")
	if m.sess.State.Get(id).Deleted {
		t.Fatal("the entry was deleted despite a negative answer")
	}

	m = press(t, m, "d")
	m = typeText(t, m, "y")
	m = press(t, m, "enter")
	if !m.sess.State.Get(id).Deleted {
		t.Fatal("the entry was not deleted")
	}
}

func TestUndoRestoresTheLastDeletion(t *testing.T) {
	m := withContent(t)
	m.focus = paneEntries
	m.entry = indexOfTitle(m, "design sketch")
	id := m.entries[m.entry].ID

	m = press(t, m, "d")
	m = typeText(t, m, "y")
	m = press(t, m, "enter")

	m = press(t, m, "u")
	if m.sess.State.Get(id).Deleted {
		t.Fatalf("undo did not restore the entry: %s", m.status)
	}
}

func TestUndoWithNothingDeleted(t *testing.T) {
	m := withContent(t)
	m = press(t, m, "u")

	if !strings.Contains(m.status, "nothing") {
		t.Fatalf("status = %q, want an explanation", m.status)
	}
}

func TestRenamePrefillsTheCurrentTitle(t *testing.T) {
	m := withContent(t)
	m.focus = paneEntries
	m.entry = indexOfTitle(m, "design sketch")
	id := m.entries[m.entry].ID

	m = press(t, m, "r")
	if m.input.String() != "design sketch" {
		t.Fatalf("the rename prompt is not prefilled: %q", m.input.String())
	}

	m.input.set("revised sketch")
	m = press(t, m, "enter")

	if got := m.sess.State.Get(id).Title; got != "revised sketch" {
		t.Fatalf("Title = %q", got)
	}
}

func TestReorderMovesAnEntry(t *testing.T) {
	m := withContent(t)
	m.focus = paneEntries
	m.entry = 0
	first := m.entries[0].ID

	m = press(t, m, "J")

	if m.entries[1].ID != first {
		t.Fatalf("J did not move the entry down: %v", titlesOf(m.entries))
	}
	// The cursor follows what it moved.
	if m.entry != 1 {
		t.Fatalf("the cursor did not follow: %d", m.entry)
	}

	m = press(t, m, "K")
	if m.entries[0].ID != first {
		t.Fatalf("K did not move it back: %v", titlesOf(m.entries))
	}
}

// While a search is active the displayed order is not the stored one, so
// moving "down one" would land somewhere the user did not point at.
func TestReorderIsRefusedDuringASearch(t *testing.T) {
	m := withContent(t)
	m.focus = paneEntries

	m = press(t, m, "/")
	m = typeText(t, m, "e")
	m = press(t, m, "enter")

	m = press(t, m, "J")
	if !strings.Contains(m.status, "search") {
		t.Fatalf("status = %q, want a refusal explaining why", m.status)
	}
}

func TestDetailViewShowsTheBody(t *testing.T) {
	m := withContent(t)
	m.focus = paneEntries
	m.entry = indexOfTitle(m, "design sketch")

	m = press(t, m, "enter")
	if m.mode != modeDetail {
		t.Fatal("enter did not open the detail view")
	}

	out := m.View()
	if !strings.Contains(out, "tokenizes") {
		t.Fatalf("the detail view does not show the body:\n%s", out)
	}

	m = press(t, m, "q")
	if m.mode != modeNormal {
		t.Fatal("q did not leave the detail view")
	}
}

func TestHelpOpensAndCloses(t *testing.T) {
	m := withContent(t)

	m = press(t, m, "?")
	if m.mode != modeHelp {
		t.Fatal("? did not open help")
	}
	out := m.View()
	for _, want := range []string{"new note", "search", ":filter"} {
		if !strings.Contains(out, want) {
			t.Errorf("help is missing %q", want)
		}
	}

	m = press(t, m, "j")
	if m.mode != modeHelp-modeHelp+modeNormal {
		t.Fatal("any key should close help")
	}
}

// Every listed command must actually be reachable, or the help lies.
func TestEveryListedCommandDispatches(t *testing.T) {
	for _, c := range tuiCommands {
		if c.name == "quit" {
			continue // tested separately, since it ends the program
		}
		t.Run(c.name, func(t *testing.T) {
			m := withContent(t)
			m.focus = paneEntries

			if _, ok := tuiByName[c.name]; !ok {
				t.Fatalf("%q is not in the dispatch table", c.name)
			}
			for _, alias := range c.aliases {
				if tuiByName[alias] != c {
					t.Errorf("alias %q does not resolve to %q", alias, c.name)
				}
			}

			// Running with no arguments must report a problem rather than
			// panic or corrupt anything.
			_, _ = m.runCommand(c.name)
			if m.mode == modeNormal || m.mode == modePrompt || m.mode == modeHelp {
				return
			}
			t.Fatalf("%q left the interface in mode %d", c.name, m.mode)
		})
	}
}

func TestQuitCommand(t *testing.T) {
	m := withContent(t)

	m = press(t, m, ":")
	m = typeText(t, m, "q")
	m = press(t, m, "enter")

	if !m.quitting {
		t.Fatal(":q did not quit")
	}
	if m.View() != "" {
		t.Fatal("a quitting model still rendered a frame")
	}
}

// Every action commits as it goes, so nothing is lost if the program ends
// unexpectedly.
func TestActionsCommitImmediately(t *testing.T) {
	m := withContent(t)

	m = press(t, m, "n")
	m = typeText(t, m, "persisted")
	m = press(t, m, "enter")

	if m.sess.Pending() != 0 {
		t.Fatalf("%d events left uncommitted", m.sess.Pending())
	}

	// Another session sees it on disk.
	other, err := session.OpenProject(m.sess.Project, m.sess.Actor)
	if err != nil {
		t.Fatal(err)
	}
	if got := other.State.List(state.Filter{Text: "persisted"}, state.OrderRank); len(got) != 1 {
		t.Fatal("the entry did not reach disk")
	}
}

// Another process may have written to the log while the interface was open.
func TestReloadPicksUpOutsideWrites(t *testing.T) {
	m := withContent(t)

	other, err := session.OpenProject(m.sess.Project, store.Actor{ID: ulid.NewGenerator().New(), Name: "bob"})
	if err != nil {
		t.Fatal(err)
	}
	other.SetClock(clock)
	if _, err := other.NewNote(m.notebooks()[0].ID, "from elsewhere", ""); err != nil {
		t.Fatal(err)
	}
	if err := other.Commit(); err != nil {
		t.Fatal(err)
	}

	if indexOfTitle(m, "from elsewhere") >= 0 {
		t.Fatal("the outside write was visible before reloading")
	}

	m = press(t, m, "R")
	if indexOfTitle(m, "from elsewhere") < 0 {
		t.Fatalf("reload did not pick up the outside write: %v", titlesOf(m.entries))
	}
}

func TestEmptyProjectExplainsWhatToDo(t *testing.T) {
	m := newModel(t)

	out := m.View()
	if !strings.Contains(out, "no notebooks yet") {
		t.Fatalf("an empty project does not explain itself:\n%s", out)
	}

	// Creating a note with no notebook makes one rather than failing.
	m = press(t, m, "n")
	m = typeText(t, m, "first ever")
	m = press(t, m, "enter")

	if len(m.notebooks()) != 1 {
		t.Fatalf("no notebook was created: %s", m.status)
	}
}

func TestCtrlCAlwaysQuits(t *testing.T) {
	for _, start := range []mode{modeNormal, modeCommand, modeSearch, modePrompt, modeDetail, modeHelp} {
		m := withContent(t)
		m.mode = start

		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
		if !updated.(*Model).quitting {
			t.Errorf("ctrl-c did not quit from mode %d", start)
		}
	}
}

func TestWindowResizeIsHandled(t *testing.T) {
	m := withContent(t)

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 40, Height: 10})
	m = updated.(*Model)

	if m.width != 40 || m.height != 10 {
		t.Fatalf("size = %dx%d", m.width, m.height)
	}
	if m.View() == "" {
		t.Fatal("nothing rendered after a resize")
	}
}

// ---------------------------------------------------------------- input

func TestInputEditing(t *testing.T) {
	var in input
	in.set("hello world")

	if in.String() != "hello world" {
		t.Fatalf("set = %q", in.String())
	}

	in.backspace()
	if in.String() != "hello worl" {
		t.Fatalf("backspace = %q", in.String())
	}

	in.deleteWord()
	if in.String() != "hello " {
		t.Fatalf("deleteWord = %q", in.String())
	}

	in.home()
	in.deleteForward()
	if in.String() != "ello " {
		t.Fatalf("deleteForward at the start = %q", in.String())
	}

	in.end()
	in.insertString("nd")
	if in.String() != "ello nd" {
		t.Fatalf("insertString = %q", in.String())
	}

	in.deleteToStart()
	if !in.empty() {
		t.Fatalf("deleteToStart left %q", in.String())
	}
}

// The cursor indexes characters, not bytes, so a multi-byte character takes
// one keypress to cross.
func TestInputHandlesMultiByteCharacters(t *testing.T) {
	var in input
	in.set("naïve café")

	in.backspace()
	if got := in.String(); got != "naïve caf" {
		t.Fatalf("backspace over a multi-byte string gave %q", got)
	}

	in.home()
	for i := 0; i < 3; i++ {
		in.right()
	}
	in.insert('X')
	if got := in.String(); got != "naïXve caf" {
		t.Fatalf("insert after a multi-byte character gave %q", got)
	}
}

func TestInputCursorStopsAtBothEnds(t *testing.T) {
	var in input
	in.set("ab")

	for i := 0; i < 10; i++ {
		in.left()
	}
	if in.cursor != 0 {
		t.Fatalf("cursor = %d, want 0", in.cursor)
	}
	in.backspace() // must be a no-op, not a panic
	if in.String() != "ab" {
		t.Fatalf("backspace at the start changed the text: %q", in.String())
	}

	for i := 0; i < 10; i++ {
		in.right()
	}
	if in.cursor != 2 {
		t.Fatalf("cursor = %d, want 2", in.cursor)
	}
	in.deleteForward() // also a no-op
	if in.String() != "ab" {
		t.Fatalf("deleteForward at the end changed the text: %q", in.String())
	}
}

// A line longer than the field must scroll to keep the cursor visible.
func TestInputRenderFollowsTheCursor(t *testing.T) {
	var in input
	in.set(strings.Repeat("x", 100) + "END")

	got := in.render(":", 20)
	if !strings.Contains(got, "END") {
		t.Fatalf("the render does not show the cursor's neighbourhood: %q", got)
	}
	if len([]rune(got)) > 21 {
		t.Fatalf("the render is %d wide, want at most 21: %q", len([]rune(got)), got)
	}
}

func TestTruncateAndPad(t *testing.T) {
	if got := truncate("hello", 10); got != "hello" {
		t.Errorf("truncate short = %q", got)
	}
	if got := truncate("hello world", 8); len([]rune(got)) != 8 {
		t.Errorf("truncate = %q, want 8 runes", got)
	}
	if got := truncate("hello", 0); got != "" {
		t.Errorf("truncate to zero = %q", got)
	}
	if got := pad("ab", 5); got != "ab   " {
		t.Errorf("pad = %q", got)
	}
	if got := pad("abcdef", 3); got != "abcdef" {
		t.Errorf("pad must not shorten: %q", got)
	}
}

func TestCommonPrefix(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{nil, ""},
		{[]string{"task"}, "task"},
		{[]string{"tag", "task"}, "ta"},
		{[]string{"tag", "untag"}, ""},
	}
	for _, c := range cases {
		if got := commonPrefix(c.in); got != c.want {
			t.Errorf("commonPrefix(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// ---------------------------------------------------------------- helpers

// stripANSI removes escape sequences so a rendered line can be measured in
// visible columns.
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			i++
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func indexOfTitle(m *Model, title string) int {
	for i, n := range m.entries {
		if n.Title == title {
			return i
		}
	}
	return -1
}

func titlesOf(nodes []*state.Node) []string {
	out := make([]string, len(nodes))
	for i, n := range nodes {
		out[i] = n.Title
	}
	return out
}
