// Package tui is the interactive interface: a two-pane browser with vim
// movement and a command line.
//
// It writes nothing itself. Every change goes through the session package, the
// same path the command line uses, so the two cannot diverge in what an
// operation means or what it records.
package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/shakfu/gnotes/internal/search"
	"github.com/shakfu/gnotes/internal/session"
	"github.com/shakfu/gnotes/internal/state"
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

	// input backs whichever line is being typed into.
	input input

	// pending is the prompt awaiting an answer, empty when none is.
	pending prompt

	// query filters the entry list; empty means show everything.
	query string

	// index is the search index, rebuilt whenever the log changes.
	index *search.Index

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

	// quitting suppresses a final redraw once the program is closing.
	quitting bool
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
		sess: s,
		now:  s.Now,
		// A fresh terminal reports its size immediately, but a value here
		// keeps the first frame from being computed against a zero height.
		width:  80,
		height: 24,
	}
	m.reindex()
	m.refresh()
	return m
}

// Init satisfies tea.Model. There is nothing to do asynchronously at startup:
// the log is already loaded.
func (m *Model) Init() tea.Cmd { return nil }

// reindex rebuilds the search index from the current tree.
func (m *Model) reindex() {
	m.index = search.Build(m.sess.State.List(state.Filter{}, state.OrderRank))
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
func (m *Model) refresh() {
	f := m.filter
	f.Now = m.now()

	if nb := m.currentNotebook(); nb != nil {
		f.Notebook = nb.ID
	}

	if m.query != "" {
		// A search spans the whole project rather than the selected notebook:
		// looking for something is precisely the case where you do not know
		// where it is.
		results := m.index.Search(m.query, 0)
		m.entries = m.entries[:0]
		for _, r := range results {
			if matchesFilter(r.Node, m.filter, f.Now) {
				m.entries = append(m.entries, r.Node)
			}
		}
	} else {
		m.entries = m.sess.State.List(f, m.order)
	}

	// Keep the cursor inside the list and the viewport around the cursor.
	if m.entry >= len(m.entries) {
		m.entry = max(0, len(m.entries)-1)
	}
	m.clampScroll()
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
	m.status, m.statusErr = err.Error(), true
}

// commit writes staged events, reindexes and refreshes. Every mutating action
// funnels through it so that none can forget a step.
func (m *Model) commit(describe string) {
	if err := m.sess.Commit(); err != nil {
		m.setError(err)
		return
	}
	m.reindex()
	m.refresh()
	if describe != "" {
		m.setStatus("%s", describe)
	}
}

// reload re-reads the log from disk, picking up another process's writes.
func (m *Model) reload() {
	if err := m.sess.Reload(); err != nil {
		m.setError(err)
		return
	}
	m.reindex()
	m.refresh()
}

// ask puts the interface into a one-off prompt.
func (m *Model) ask(label, initial string, action func(*Model, string) error) {
	m.mode = modePrompt
	m.pending = prompt{label: label, action: action}
	m.input.set(initial)
}

// truncate shortens a string to n display columns, ending in an ellipsis when
// it had to cut. Measured in runes, so a title with accents does not break the
// column alignment.
func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	if n == 1 {
		return "."
	}
	return string(runes[:n-1]) + "…"
}

// pad extends a plain string to n display columns. It must not be given a
// styled string: escape sequences would be counted as visible characters.
func pad(s string, n int) string {
	if d := n - len([]rune(s)); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

// clip shortens a possibly styled string to n visible columns.
//
// Escape sequences are copied through without counting towards the width, and
// a reset is appended so that a colour cut short does not bleed into the rest
// of the line. truncate would count every byte of an escape sequence as a
// character and cut a styled line to a fraction of its width.
func clip(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= n {
		return s
	}

	var b strings.Builder
	visible, escaping := 0, false

	for _, r := range s {
		switch {
		case r == 0x1b:
			escaping = true
			b.WriteRune(r)
		case escaping:
			b.WriteRune(r)
			// A CSI sequence ends at its first letter.
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				escaping = false
			}
		case visible < n-1:
			b.WriteRune(r)
			visible++
		default:
			b.WriteString("…\x1b[0m")
			return b.String()
		}
	}
	b.WriteString("\x1b[0m")
	return b.String()
}
