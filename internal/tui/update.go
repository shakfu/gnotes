package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/shakfu/gnotes/internal/rank"
	"github.com/shakfu/gnotes/internal/state"
)

// Update handles one message.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.clampScroll()
		return m, nil

	case tea.KeyMsg:
		return m.key(msg)
	}
	return m, nil
}

// key dispatches a keypress to the handler for the current mode. Which mode is
// active decides what a bare letter means, which is what makes single-key
// commands and typing a search coexist.
func (m *Model) key(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// ctrl-c always quits, in every mode. A full-screen program that can trap
	// the one key everybody reaches for is a program people distrust.
	if msg.Type == tea.KeyCtrlC {
		m.quitting = true
		return m, tea.Quit
	}

	switch m.mode {
	case modeCommand, modeSearch, modePrompt:
		return m.keyEditing(msg)
	case modeDetail:
		return m.keyDetail(msg)
	case modeHelp:
		m.mode = modeNormal
		return m, nil
	default:
		return m.keyNormal(msg)
	}
}

// keyNormal handles movement and single-key commands.
func (m *Model) keyNormal(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// A new keypress supersedes whatever the last one reported.
	m.status = ""

	switch msg.String() {
	case "q":
		m.quitting = true
		return m, tea.Quit

	// Movement, vim keys and arrows alike.
	case "j", "down":
		m.moveCursor(1)
	case "k", "up":
		m.moveCursor(-1)
	case "g", "home":
		m.setCursor(0)
	case "G", "end":
		m.setCursor(m.cursorMax())
	case "ctrl+d", "pgdown":
		m.moveCursor(m.listHeight() / 2)
	case "ctrl+u", "pgup":
		m.moveCursor(-m.listHeight() / 2)

	case "h", "left":
		m.focus = paneNotebooks
	case "l", "right":
		if len(m.entries) > 0 {
			m.focus = paneEntries
		}
	case "tab":
		if m.focus == paneNotebooks && len(m.entries) > 0 {
			m.focus = paneEntries
		} else {
			m.focus = paneNotebooks
		}

	case "enter":
		if m.focus == paneNotebooks {
			if len(m.entries) > 0 {
				m.focus = paneEntries
			}
			break
		}
		if m.currentEntry() != nil {
			m.mode, m.detailScroll = modeDetail, 0
		}

	// Creation. Lower case for the common kinds, upper for a notebook, which
	// is rarer.
	case "n":
		m.promptNew(false)
	case "t":
		m.promptNew(true)
	case "N":
		m.ask("notebook: ", "", func(m *Model, name string) error {
			nb, err := m.sess.NewNotebook(name)
			if err != nil {
				return err
			}
			m.commit("created notebook " + nb.Title)
			return nil
		})

	// Task status. Space toggles between open and done, which is the motion
	// people make most; 'x' and 's' are the explicit forms.
	case " ":
		m.toggleDone()
	case "x":
		m.setStatusOf(state.StatusDone)
	case "s":
		m.setStatusOf(state.StatusDoing)
	case "o":
		m.setStatusOf(state.StatusOpen)

	case "e":
		m.promptEdit()
	case "r":
		m.promptRename()
	case "d":
		m.promptDelete()
	case "u":
		m.undoDelete()

	case "J":
		m.reorder(1)
	case "K":
		m.reorder(-1)

	case ":":
		m.mode = modeCommand
		m.input.clear()
		m.histPos = len(m.history)
	case "/":
		m.mode = modeSearch
		m.input.set(m.query)
	case "?":
		m.mode = modeHelp

	case "esc":
		// One escape clears the search, a second clears the filter. Anything
		// that narrows the view should be one obvious keypress from gone.
		switch {
		case m.query != "":
			m.query = ""
			m.refresh()
			m.setStatus("search cleared")
		case !filterIsEmpty(m.filter):
			m.filter = state.Filter{}
			m.refresh()
			m.setStatus("filter cleared")
		}

	case "R":
		m.reload()
		m.setStatus("reloaded")
	}
	return m, nil
}

// keyEditing handles the command line, the search line and prompts, which
// share one editor.
func (m *Model) keyEditing(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		if m.mode == modeSearch {
			// Abandoning a search restores the list it was narrowing.
			m.query = ""
			m.refresh()
		}
		m.mode = modeNormal
		m.input.clear()
		return m, nil

	case tea.KeyEnter:
		return m.submit()

	case tea.KeyBackspace:
		m.input.backspace()
	case tea.KeyDelete:
		m.input.deleteForward()
	case tea.KeyLeft:
		m.input.left()
	case tea.KeyRight:
		m.input.right()
	case tea.KeyHome, tea.KeyCtrlA:
		m.input.home()
	case tea.KeyEnd, tea.KeyCtrlE:
		m.input.end()
	case tea.KeyCtrlW:
		m.input.deleteWord()
	case tea.KeyCtrlU:
		m.input.deleteToStart()

	case tea.KeyUp:
		if m.mode == modeCommand {
			m.historyPrev()
		}
	case tea.KeyDown:
		if m.mode == modeCommand {
			m.historyNext()
		}

	case tea.KeyTab:
		if m.mode == modeCommand {
			m.complete()
		}

	case tea.KeyRunes, tea.KeySpace:
		if msg.Type == tea.KeySpace {
			m.input.insert(' ')
		} else {
			m.input.insertString(string(msg.Runes))
		}
	}

	// The search line filters as it is typed, which is the point of having it
	// separate from the command line.
	if m.mode == modeSearch {
		m.query = m.input.String()
		m.entry, m.scroll = 0, 0
		m.refresh()
	}
	return m, nil
}

// keyDetail handles the full-screen body view.
func (m *Model) keyDetail(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc", "enter":
		m.mode = modeNormal
	case "j", "down":
		m.detailScroll++
	case "k", "up":
		m.detailScroll = max(0, m.detailScroll-1)
	case "g":
		m.detailScroll = 0
	case "ctrl+d", "pgdown":
		m.detailScroll += m.height / 2
	case "ctrl+u", "pgup":
		m.detailScroll = max(0, m.detailScroll-m.height/2)
	case "e":
		m.mode = modeNormal
		m.promptEdit()
	case " ", "x":
		m.toggleDone()
	}
	return m, nil
}

// submit acts on a completed line.
func (m *Model) submit() (tea.Model, tea.Cmd) {
	text := strings.TrimSpace(m.input.String())

	switch m.mode {
	case modeSearch:
		m.mode = modeNormal
		m.input.clear()
		if text == "" {
			m.query = ""
			m.refresh()
		}
		return m, nil

	case modePrompt:
		action := m.pending.action
		m.mode, m.pending = modeNormal, prompt{}
		m.input.clear()

		if text == "" {
			return m, nil
		}
		if err := action(m, text); err != nil {
			m.setError(err)
		}
		return m, nil

	case modeCommand:
		m.mode = modeNormal
		m.input.clear()
		m.pushHistory(text)
		return m.runCommand(text)
	}
	return m, nil
}

// cursorMax is the last index in the focused pane.
func (m *Model) cursorMax() int {
	if m.focus == paneNotebooks {
		return max(0, len(m.notebooks())-1)
	}
	return max(0, len(m.entries)-1)
}

// setCursor moves the focused pane's cursor to an absolute position.
func (m *Model) setCursor(i int) {
	i = max(0, min(i, m.cursorMax()))
	if m.focus == paneNotebooks {
		if i != m.notebook {
			m.notebook = i
			// A different notebook is a different list, so the entry cursor
			// starts over rather than landing somewhere arbitrary.
			m.entry, m.scroll = 0, 0
			m.refresh()
		}
		return
	}
	m.entry = i
	m.clampScroll()
}

// moveCursor moves the focused pane's cursor by a delta.
func (m *Model) moveCursor(delta int) {
	if m.focus == paneNotebooks {
		m.setCursor(m.notebook + delta)
		return
	}
	m.setCursor(m.entry + delta)
}

// promptNew asks for a title and creates a note or a task.
func (m *Model) promptNew(isTask bool) {
	label := "note: "
	if isTask {
		label = "task: "
	}

	m.ask(label, "", func(m *Model, title string) error {
		nb := m.currentNotebook()
		if nb == nil {
			created, err := m.sess.DefaultNotebook()
			if err != nil {
				return err
			}
			nb = created
		}

		var err error
		if isTask {
			_, err = m.sess.NewTask(nb.ID, title, "")
		} else {
			_, err = m.sess.NewNote(nb.ID, title, "")
		}
		if err != nil {
			return err
		}

		m.commit("created " + title)
		// Put the cursor on what was just created, which is almost always what
		// the next keypress is aimed at.
		m.focus = paneEntries
		m.selectTitle(title)
		return nil
	})
}

// selectTitle moves the entry cursor to the last entry with a given title.
func (m *Model) selectTitle(title string) {
	for i := len(m.entries) - 1; i >= 0; i-- {
		if m.entries[i].Title == title {
			m.entry = i
			m.clampScroll()
			return
		}
	}
}

// promptRename edits the selected item's title, prefilled so it can be
// adjusted rather than retyped.
func (m *Model) promptRename() {
	n := m.selected()
	if n == nil {
		return
	}
	m.ask("title: ", n.Title, func(m *Model, title string) error {
		if err := m.sess.SetTitle(n, title); err != nil {
			return err
		}
		m.commit("renamed")
		return nil
	})
}

// promptEdit edits the selected entry's body. The body is multi-line, which a
// single-line field cannot take, so this replaces the first line and keeps the
// rest; the full editor is the command line's ':edit'.
func (m *Model) promptEdit() {
	n := m.currentEntry()
	if n == nil {
		return
	}

	first, rest, _ := strings.Cut(n.Body, "\n")
	m.ask("body: ", first, func(m *Model, line string) error {
		body := line
		if rest != "" {
			body += "\n" + rest
		}
		if err := m.sess.SetBody(n, body); err != nil {
			return err
		}
		m.commit("saved")
		return nil
	})
}

// promptDelete asks before removing something, since a notebook takes its
// contents with it.
func (m *Model) promptDelete() {
	n := m.selected()
	if n == nil {
		return
	}

	label := "delete " + truncate(n.Title, 30) + "? (y/N) "
	if n.Kind == state.KindNotebook {
		label = "delete notebook " + truncate(n.Title, 24) + " and its contents? (y/N) "
	}

	m.ask(label, "", func(m *Model, answer string) error {
		if !strings.EqualFold(answer, "y") && !strings.EqualFold(answer, "yes") {
			m.setStatus("kept")
			return nil
		}
		if err := m.sess.Delete(n); err != nil {
			return err
		}
		m.commit("deleted " + n.Title + "  (press u to undo)")
		return nil
	})
}

// undoDelete restores the most recently deleted node.
func (m *Model) undoDelete() {
	var newest *state.Node
	for _, n := range m.sess.State.List(state.Filter{IncludeDeleted: true, Kinds: everyKind}, state.OrderUpdated) {
		if n.Deleted {
			// OrderUpdated is newest first, so the first hit is the one.
			newest = n
			break
		}
	}
	if newest == nil {
		m.setStatus("nothing to undo")
		return
	}
	if err := m.sess.Restore(newest.ID); err != nil {
		m.setError(err)
		return
	}
	m.commit("restored " + newest.Title)
}

var everyKind = []state.Kind{state.KindNotebook, state.KindNote, state.KindTask}

// selected returns whatever the focused pane points at.
func (m *Model) selected() *state.Node {
	if m.focus == paneNotebooks {
		return m.currentNotebook()
	}
	return m.currentEntry()
}

// toggleDone flips a task between open and done.
func (m *Model) toggleDone() {
	n := m.currentEntry()
	if n == nil {
		return
	}
	if n.Kind != state.KindTask {
		m.setStatus("%q is a note, not a task", truncate(n.Title, 40))
		return
	}

	want := state.StatusDone
	if n.Status == state.StatusDone {
		want = state.StatusOpen
	}
	if err := m.sess.SetStatus(n, want); err != nil {
		m.setError(err)
		return
	}
	m.commit(want.String() + ": " + truncate(n.Title, 40))
}

// setStatusOf sets an explicit status on the selected task.
func (m *Model) setStatusOf(want state.Status) {
	n := m.currentEntry()
	if n == nil {
		return
	}
	if n.Kind != state.KindTask {
		m.setStatus("%q is a note, not a task", truncate(n.Title, 40))
		return
	}
	if err := m.sess.SetStatus(n, want); err != nil {
		m.setError(err)
		return
	}
	m.commit(want.String() + ": " + truncate(n.Title, 40))
}

// reorder moves the selected entry up or down among its siblings.
//
// It is disabled while a search or a sort is active: the displayed order would
// not be the stored one, so moving "down one" on screen would put the entry
// somewhere the user did not point at.
func (m *Model) reorder(delta int) {
	if m.query != "" || m.order != state.OrderRank {
		m.setStatus("clear the search and sort by rank to reorder")
		return
	}
	n := m.selected()
	if n == nil {
		return
	}

	var siblings []*state.Node
	if m.focus == paneNotebooks {
		siblings = m.notebooks()
	} else {
		siblings = m.entries
	}

	at := indexOfNode(siblings, n.ID)
	target := at + delta
	if at < 0 || target < 0 || target >= len(siblings) {
		return
	}

	pos := rank.After(siblings[target].ID)
	if delta < 0 {
		pos = rank.Before(siblings[target].ID)
	}
	if err := m.sess.Move(n, "", pos); err != nil {
		m.setError(err)
		return
	}

	m.commit("")
	if m.focus == paneNotebooks {
		m.notebook = target
		m.refresh()
	} else {
		m.entry = target
		m.clampScroll()
	}
}

func indexOfNode(nodes []*state.Node, id string) int {
	for i, n := range nodes {
		if n.ID == id {
			return i
		}
	}
	return -1
}

// filterIsEmpty reports whether a filter narrows anything.
func filterIsEmpty(f state.Filter) bool {
	return len(f.Kinds) == 0 && len(f.Tags) == 0 && f.Status == nil &&
		f.Priority == nil && f.Assignee == "" && !f.Overdue && !f.IncludeDeleted
}
