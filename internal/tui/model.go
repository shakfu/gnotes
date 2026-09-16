// Package tui is the interactive interface: a two-pane browser with vim
// movement and a command line.
//
// It writes nothing itself. Every change goes through the session package, the
// same path the command line uses, so the two cannot diverge in what an
// operation means or what it records.
package tui

import (
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/shakfu/gwiki/internal/session"
	"github.com/shakfu/gwiki/internal/state"
)

// pane names the focused column.
type pane int

const (
	paneNotebooks pane = iota
	paneEntries
)

// mode is the interface's input mode, in the vim sense: what a keypress means
// depends on which one is active.
type mode int

const (
	// modeNormal is movement and single-key commands.
	modeNormal mode = iota
	// modeCommand is the ':' line.
	modeCommand
	// modeSearch is the '/' line, which filters live as it is typed.
	modeSearch
	// modePrompt is a one-off question, such as a new entry's title.
	modePrompt
	// modeDetail shows one entry's body full-screen.
	modeDetail
	// modeHelp shows the key reference.
	modeHelp
)

// Model is the interface state.
type Model struct {
	sess *session.Session

	// width and height are the terminal size.
	width, height int

	mode  mode
	focus pane

	// notebook and entry are cursor positions within each pane.
	notebook int
	entry    int

	// scroll is the first visible entry row, and detailScroll the first
	// visible body line.
	scroll       int
	detailScroll int

	// nbScroll is the first visible notebook, and helpScroll the first
	// visible line of the key reference.
	nbScroll   int
	helpScroll int

	// input backs whichever line is being typed into.
	input input

	// pending is the prompt awaiting an answer, empty when none is.
	pending prompt

	// query filters the entry list; empty means show everything.
	query string

	// filter is the persistent filter set from the command line.
	filter state.Filter
	order  state.Order

	// entries is the currently displayed list, recomputed on any change.
	entries []*state.Node

	// status is the message on the bottom line, and statusErr styles it as a
	// failure.
	status    string
	statusErr bool

	// history is the command line's recall list, oldest first.
	history []string
	histPos int

	now func() time.Time

	// getenv reads the environment, for the editor setting; tests replace it.
	getenv func(string) string

	// quitting suppresses a final redraw once the program is closing.
	quitting bool

	// deleted holds the ids this interface deleted, newest last, for undo. Only
	// its own deletions: undo must not reach back into other people's.
	deleted []string

	// after is a command queued by a ':' command for Update to return.
	after tea.Cmd
}

// pollInterval is how often the logs are checked for other writers.
const pollInterval = time.Second

// pollMsg asks the model to check the logs for outside writes.
type pollMsg struct{}

func poll() tea.Cmd {
	return tea.Tick(pollInterval, func(time.Time) tea.Msg { return pollMsg{} })
}

// prompt is a question awaiting a typed answer.
type prompt struct {
	// label is shown to the left of the input.
	label string

	// action receives the answer. Returning an error shows it on the status
	// line and leaves the interface otherwise unchanged.
	action func(m *Model, answer string) error
}

// New builds a model over an open session.
func New(s *session.Session) *Model {
	m := &Model{
		sess:   s,
		now:    s.Now,
		getenv: os.Getenv,
		// A fresh terminal reports its size immediately, but a value here
		// keeps the first frame from being computed against a zero height.
		width:  80,
		height: 24,
	}
	m.refresh()
	return m
}

// Init satisfies tea.Model. The project is already loaded; it starts the poll that
// notices writes by the command line, the browser view or an agent.
func (m *Model) Init() tea.Cmd { return poll() }

// pollDisk reloads when another process has written. It waits while a command
// or prompt is being typed, since a prompt holds the node it will act on.
func (m *Model) pollDisk() {
	if m.mode == modeCommand || m.mode == modePrompt {
		return
	}
	changed, err := m.sess.Refresh()
	if err != nil {
		// Reported once rather than every second; the next poll retries.
		if !m.statusErr {
			m.setError(fmt.Errorf("could not read the database: %w", err))
		}
		return
	}
	if changed {
		m.refresh()
	}
}

// notebooks returns the notebook column's contents.
func (m *Model) notebooks() []*state.Node { return m.sess.State.Notebooks() }

// currentNotebook returns the selected notebook, or nil when none exists.
func (m *Model) currentNotebook() *state.Node {
	nbs := m.notebooks()
	if len(nbs) == 0 {
		return nil
	}
	if m.notebook >= len(nbs) {
		m.notebook = len(nbs) - 1
	}
	return nbs[m.notebook]
}

// currentEntry returns the selected note or task, or nil.
func (m *Model) currentEntry() *state.Node {
	if m.entry < 0 || m.entry >= len(m.entries) {
		return nil
	}
	return m.entries[m.entry]
}

// refresh recomputes the visible entry list from the notebook selection, the
// persistent filter and the live search query.
//
// It runs after every change rather than on demand, because every pane's
// contents derive from the tree and keeping a stale list would be the easiest
// way to show something that no longer exists.
//
// The selection follows the entry, not the row: after a filter, sort or
// reload moves it, the cursor moves with it, so the next key acts on what the
// user was looking at. An entry that left the list closes the detail view
// rather than silently showing its neighbour.
func (m *Model) refresh() {
	var keep string
	if n := m.currentEntry(); n != nil {
		keep = n.ID
	}

	f := m.filter
	f.Now = m.now()

	if nb := m.currentNotebook(); nb != nil {
		f.Notebook = nb.ID
	}

	if m.query != "" {
		// A search spans the whole project rather than the selected notebook:
		// looking for something is precisely the case where you do not know
		// where it is.
		results, err := m.sess.Search(m.query, 0, false)
		if err != nil {
			m.setError(err)
		}
		m.entries = m.entries[:0]
		for _, n := range results {
			if matchesFilter(n, m.filter, f.Now) {
				m.entries = append(m.entries, n)
			}
		}
	} else {
		m.entries = m.sess.State.List(f, m.order)
	}

	if i := indexOfNode(m.entries, keep); keep != "" && i >= 0 {
		m.entry = i
	} else if keep != "" && m.mode == modeDetail {
		m.mode = modeNormal
	}

	// Keep the cursor inside the list and the viewport around the cursor.
	if m.entry >= len(m.entries) {
		m.entry = max(0, len(m.entries)-1)
	}
	m.clampScroll()
}

// resetCursor puts the entry cursor back at the top of a list that is about to
// change, for the cases where following the old selection is not wanted.
func (m *Model) resetCursor() {
	m.entries, m.entry, m.scroll = nil, 0, 0
}

// selectID moves the entry cursor to a node, if it is listed.
func (m *Model) selectID(id string) {
	if i := indexOfNode(m.entries, id); i >= 0 {
		m.entry = i
		m.clampScroll()
	}
}

// matchesFilter applies the persistent filter to a search result, which
// bypasses State.List.
func matchesFilter(n *state.Node, f state.Filter, now time.Time) bool {
	if n.Deleted && !f.IncludeDeleted {
		return false
	}
	if len(f.Kinds) > 0 && !containsKind(f.Kinds, n.Kind) {
		return false
	}
	if f.Status != nil && (n.Kind != state.KindTask || n.Status != *f.Status) {
		return false
	}
	if f.Priority != nil && (n.Kind != state.KindTask || n.Priority != *f.Priority) {
		return false
	}
	if f.Overdue && !n.Overdue(now) {
		return false
	}
	for _, tag := range f.Tags {
		if !n.HasTag(state.NormalizeTag(tag)) {
			return false
		}
	}
	if f.Assignee != "" && !containsString(n.Assignees, f.Assignee) {
		return false
	}
	return true
}

func containsKind(kinds []state.Kind, k state.Kind) bool {
	for _, want := range kinds {
		if want == k {
			return true
		}
	}
	return false
}

func containsString(all []string, want string) bool {
	for _, s := range all {
		if s == want {
			return true
		}
	}
	return false
}

// listHeight is how many entry rows fit, once the header, the notebook column
// header and the status line are accounted for.
func (m *Model) listHeight() int {
	h := m.height - 4
	if h < 1 {
		return 1
	}
	return h
}

// clampScroll keeps the cursor within the visible window.
func (m *Model) clampScroll() {
	h := m.listHeight()
	if m.entry < m.scroll {
		m.scroll = m.entry
	}
	if m.entry >= m.scroll+h {
		m.scroll = m.entry - h + 1
	}
	if m.scroll < 0 {
		m.scroll = 0
	}
}

// setStatus shows a message on the bottom line.
func (m *Model) setStatus(format string, args ...any) {
	m.status, m.statusErr = fmt.Sprintf(format, args...), false
}

// setError shows a failure on the bottom line. Errors are reported here rather
// than raised, because a full-screen interface that exits on a bad command
// would lose whatever else was in flight.
func (m *Model) setError(err error) {
	// Every failed action and every failed commit lands here, which makes it
	// the one place that can guarantee a half-staged command is dropped before
	// the next keystroke commits it.
	m.sess.Rollback()
	// The rollback replaced the tree, so the list must be rebuilt from it or
	// it would keep showing changes that were never written.
	m.refresh()
	m.status, m.statusErr = err.Error(), true
}

// commit writes staged events, reindexes and refreshes. Every mutating action
// funnels through it so that none can forget a step.
func (m *Model) commit(describe string) {
	if err := m.sess.Commit(); err != nil {
		m.setError(err)
		return
	}
	m.refresh()
	if describe != "" {
		m.setStatus("%s", describe)
	}
}

// reload re-reads the database, picking up another process's writes. A
// failure is shown on the status line and returned, so the caller does not
// report success over it.
func (m *Model) reload() error {
	if err := m.sess.Reload(); err != nil {
		m.setError(err)
		return err
	}
	m.refresh()
	return nil
}

// ask puts the interface into a one-off prompt.
func (m *Model) ask(label, initial string, action func(*Model, string) error) {
	m.mode = modePrompt
	m.pending = prompt{label: label, action: action}
	m.input.set(initial)
}

// truncate shortens a string to n display columns, ending in an ellipsis when
// it had to cut. Columns, not runes or bytes: a wide character takes two, and
// escape sequences take none.
func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if ansi.StringWidth(s) <= n {
		return s
	}
	if n == 1 {
		return "."
	}
	return ansi.Truncate(s, n, "…")
}

// pad extends a string to n display columns.
func pad(s string, n int) string {
	if d := n - ansi.StringWidth(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

// clip shortens a possibly styled string to n visible columns. A reset is
// appended when it cuts, so a colour cut short does not bleed into the rest of
// the line.
func clip(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if ansi.StringWidth(s) <= n {
		return s
	}
	return ansi.Truncate(s, n, "…") + "\x1b[0m"
}
