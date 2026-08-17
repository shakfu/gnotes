package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/shakfu/gnotes/internal/state"
	"github.com/shakfu/gnotes/internal/ulid"
)

// The palette uses the terminal's own ANSI colours rather than fixed RGB, so
// the interface takes on whatever theme the user has already chosen instead of
// fighting it.
var (
	styleDim      = lipgloss.NewStyle().Faint(true)
	styleBold     = lipgloss.NewStyle().Bold(true)
	styleSelected = lipgloss.NewStyle().Reverse(true)
	styleHeader   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("4"))
	styleTag      = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	styleDue      = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	styleOverdue  = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	// Faint alone rather than struck through: lipgloss re-emits a strikethrough
	// around every run between spaces, which turns one styled title into dozens
	// of escape sequences for no visible gain.
	styleDone     = lipgloss.NewStyle().Faint(true)
	styleDoing    = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	styleOK       = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	styleError    = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	stylePriority = lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true)
)

// notebookWidth is the width of the left column, wide enough for a readable
// notebook name and narrow enough to leave the entry list room.
const notebookWidth = 22

// minTwoPaneWidth is the narrowest terminal that still gets two columns. Below
// it the notebook column is dropped rather than squeezed, because an entry
// list truncated to a few characters is useless while a list of titles is not.
const minTwoPaneWidth = 50

// View renders the whole screen.
func (m *Model) View() string {
	if m.quitting {
		// Leaving the terminal with a blank final frame rather than a stale
		// copy of the interface.
		return ""
	}

	switch m.mode {
	case modeHelp:
		return m.viewHelp()
	case modeDetail:
		return m.viewDetail()
	}

	var b strings.Builder
	b.WriteString(clip(m.viewHeader(), m.width))
	b.WriteByte('\n')
	b.WriteString(m.viewPanes())
	b.WriteString(clip(m.viewStatus(), m.width))
	return b.String()
}

// viewHeader is the title bar: the project, then what is narrowing the view.
func (m *Model) viewHeader() string {
	left := styleHeader.Render(m.sess.Project.Config.Name)

	var right []string
	if m.query != "" {
		right = append(right, "/"+m.query)
	}
	if !filterIsEmpty(m.filter) {
		right = append(right, "filtered")
	}
	if m.order != state.OrderRank {
		right = append(right, "sorted")
	}
	if n := m.sess.Pending(); n > 0 {
		right = append(right, fmt.Sprintf("%d unsaved", n))
	}

	c := m.sess.State.Summary(m.now())
	right = append(right, fmt.Sprintf("%d open", c.Open))
	if c.Overdue > 0 {
		right = append(right, styleOverdue.Render(fmt.Sprintf("%d overdue", c.Overdue)))
	}

	info := styleDim.Render(strings.Join(right, "  "))
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(info)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + info
}

// viewPanes renders the notebook column beside the entry list, or the entry
// list alone when the terminal is too narrow for both.
func (m *Model) viewPanes() string {
	h := m.listHeight()

	var b strings.Builder

	if m.width < minTwoPaneWidth {
		// One column. The header already names the project, and the selected
		// notebook is the one whose entries are shown, so nothing essential is
		// lost by dropping the left column.
		for _, line := range m.renderEntries(h, m.width) {
			b.WriteString(clip(line, m.width))
			b.WriteByte('\n')
		}
		return b.String()
	}

	notebooks := m.renderNotebooks(h)
	entries := m.renderEntries(h, m.width-notebookWidth-1)

	for i := 0; i < h; i++ {
		// renderNotebooks already pads to the column width, so nothing is
		// added here: doing so would count escape sequences as characters.
		b.WriteString(notebooks[i])
		b.WriteString(styleDim.Render("|"))
		b.WriteString(clip(entries[i], m.width-notebookWidth-1))
		b.WriteByte('\n')
	}
	return b.String()
}

// renderNotebooks returns exactly h lines for the left column.
func (m *Model) renderNotebooks(h int) []string {
	lines := make([]string, h)
	nbs := m.notebooks()
	blank := strings.Repeat(" ", notebookWidth)

	for i := 0; i < h; i++ {
		if i >= len(nbs) {
			lines[i] = blank
			continue
		}
		nb := nbs[i]

		// The open-task count is the reason to glance at this column, so it
		// earns its space even in a narrow one.
		open := 0
		for _, k := range m.sess.State.Children(nb.ID) {
			if k.Kind == state.KindTask && k.Status != state.StatusDone {
				open++
			}
		}
		count := ""
		if open > 0 {
			count = fmt.Sprintf(" %d", open)
		}

		label := truncate(nb.Title, notebookWidth-len(count)-2)

		// Padded to the full column width before any styling is applied.
		// Padding afterwards would measure the escape sequences as though they
		// were characters and leave the column ragged.
		line := pad(" "+label, notebookWidth-len(count)) + count

		switch {
		case i == m.notebook && m.focus == paneNotebooks:
			line = styleSelected.Render(line)
		case i == m.notebook:
			line = styleBold.Render(line)
		}
		lines[i] = line
	}
	return lines
}

// renderEntries returns exactly h lines for the entry list.
func (m *Model) renderEntries(h, width int) []string {
	lines := make([]string, h)

	if len(m.entries) == 0 {
		lines[0] = styleDim.Render("  " + m.emptyMessage())
		return lines
	}

	for row := 0; row < h; row++ {
		i := m.scroll + row
		if i >= len(m.entries) {
			break
		}
		selected := i == m.entry && m.focus == paneEntries
		lines[row] = m.renderEntry(m.entries[i], width, selected)
	}
	return lines
}

// emptyMessage explains an empty list in terms of why it is empty, which is
// more use than a bare "nothing here".
func (m *Model) emptyMessage() string {
	switch {
	case m.query != "":
		return fmt.Sprintf("nothing matches %q  (esc to clear)", m.query)
	case !filterIsEmpty(m.filter):
		return "nothing matches the filter  (esc to clear)"
	case len(m.notebooks()) == 0:
		return "no notebooks yet  (N to create one)"
	default:
		return "empty notebook  (n for a note, t for a task)"
	}
}

// renderEntry draws one row: marker, title, then the metadata that is set.
//
// The metadata is right-aligned and dropped entirely when the terminal is too
// narrow, so a small window degrades to a readable list of titles rather than
// a wrapped mess.
func (m *Model) renderEntry(n *state.Node, width int, selected bool) string {
	marker := m.entryMarker(n)
	meta := m.entryMeta(n)

	metaWidth := lipgloss.Width(meta)
	titleWidth := width - lipgloss.Width(marker) - metaWidth - 4
	if titleWidth < 12 {
		// Not enough room for both; the title is what identifies the entry.
		meta, metaWidth, titleWidth = "", 0, width-lipgloss.Width(marker)-3
	}
	if titleWidth < 1 {
		titleWidth = 1
	}

	title := truncate(n.Title, titleWidth)
	styledTitle := title
	if n.Kind == state.KindTask && n.Status == state.StatusDone {
		styledTitle = styleDone.Render(title)
	}

	gap := width - lipgloss.Width(marker) - lipgloss.Width(title) - metaWidth - 3
	if gap < 1 {
		gap = 1
	}

	line := " " + marker + " " + styledTitle + strings.Repeat(" ", gap) + meta
	if selected {
		// Padded before reversing, so the highlight spans the full row rather
		// than stopping at the text.
		return styleSelected.Render(pad(stripToWidth(line, width), width))
	}
	return line
}

// entryMarker is the leading glyph: a checkbox for a task, a rule for a note.
func (m *Model) entryMarker(n *state.Node) string {
	if n.Kind != state.KindTask {
		return styleDim.Render("--")
	}
	switch n.Status {
	case state.StatusDone:
		return styleOK.Render("[x]")
	case state.StatusDoing:
		return styleDoing.Render("[~]")
	default:
		return "[ ]"
	}
}

// entryMeta renders tags, priority and due date for a row.
func (m *Model) entryMeta(n *state.Node) string {
	var parts []string

	if n.Priority == state.PriorityHigh {
		parts = append(parts, stylePriority.Render("!"))
	}
	for _, tag := range n.Tags {
		parts = append(parts, styleTag.Render("#"+tag))
	}
	if !n.Due.IsZero() {
		text := state.FormatDue(n.Due)
		if n.Overdue(m.now()) {
			parts = append(parts, styleOverdue.Render(text))
		} else {
			parts = append(parts, styleDue.Render(text))
		}
	}
	if len(n.Assignees) > 0 {
		parts = append(parts, styleDim.Render("@"+m.sess.State.Contributor(n.Assignees[0])))
	}
	return strings.Join(parts, " ")
}

// stripToWidth clips a styled string to a column count.
func stripToWidth(s string, width int) string {
	if lipgloss.Width(s) <= width {
		return s
	}
	return truncate(s, width)
}

// viewStatus is the bottom line: whichever input is open, or the last message,
// or the key hints.
func (m *Model) viewStatus() string {
	switch m.mode {
	case modeCommand:
		return m.input.render(":", m.width)
	case modeSearch:
		return m.input.render("/", m.width)
	case modePrompt:
		return m.input.render(m.pending.label, m.width)
	}

	if m.status != "" {
		if m.statusErr {
			return styleError.Render(truncate(m.status, m.width))
		}
		return styleOK.Render(truncate(m.status, m.width))
	}

	// Hints are trimmed to what fits rather than elided, so a narrow terminal
	// shows the first few keys cleanly instead of a truncated word.
	hints := []string{"n note", "t task", "space done", "e edit", "d delete", "/ search", ": command", "? help", "q quit"}
	line := ""
	for _, h := range hints {
		if len(line)+len(h)+2 > m.width {
			break
		}
		if line != "" {
			line += "  "
		}
		line += h
	}
	return styleDim.Render(line)
}

// viewDetail shows one entry full-screen.
func (m *Model) viewDetail() string {
	n := m.currentEntry()
	if n == nil {
		m.mode = modeNormal
		return m.View()
	}

	var b strings.Builder
	b.WriteString(clip(styleBold.Render(truncate(n.Title, m.width)), m.width))
	b.WriteByte('\n')

	b.WriteString(clip(styleDim.Render(truncate(strings.Join(m.sess.State.Path(n), " / ")+
		"   "+ulid.Short(n.ID, 6), m.width)), m.width))
	b.WriteByte('\n')

	if meta := m.detailMeta(n); meta != "" {
		b.WriteString(meta)
		b.WriteByte('\n')
	}
	b.WriteString(styleDim.Render(strings.Repeat("-", min(m.width, 60))))
	b.WriteByte('\n')

	// The body is wrapped rather than clipped: this is the view whose whole
	// purpose is reading it.
	body := n.Body
	if body == "" {
		body = styleDim.Render("(no body -- press e to write one)")
	}
	lines := strings.Split(lipgloss.NewStyle().Width(min(m.width, 100)).Render(body), "\n")

	visible := m.height - 6
	if m.detailScroll > max(0, len(lines)-visible) {
		m.detailScroll = max(0, len(lines)-visible)
	}
	for i := m.detailScroll; i < len(lines) && i < m.detailScroll+visible; i++ {
		b.WriteString(lines[i])
		b.WriteByte('\n')
	}

	// Pad to the full height so the status line stays put between frames.
	for i := len(lines) - m.detailScroll; i < visible; i++ {
		b.WriteByte('\n')
	}
	b.WriteString(styleDim.Render("j/k scroll  e edit  space done  q back"))
	return b.String()
}

// detailMeta renders the field summary above a body.
func (m *Model) detailMeta(n *state.Node) string {
	var parts []string

	if n.Kind == state.KindTask {
		parts = append(parts, n.Status.String())
		if n.Priority != state.PriorityNone {
			parts = append(parts, "!"+n.Priority.String())
		}
		if !n.Due.IsZero() {
			due := "due " + state.FormatDue(n.Due)
			if n.Overdue(m.now()) {
				due = styleOverdue.Render(due + " (overdue)")
			}
			parts = append(parts, due)
		}
		for _, id := range n.Assignees {
			parts = append(parts, "@"+m.sess.State.Contributor(id))
		}
	}
	for _, tag := range n.Tags {
		parts = append(parts, styleTag.Render("#"+tag))
	}
	for _, id := range n.Links {
		if target := m.sess.State.Get(id); target != nil {
			parts = append(parts, styleDim.Render("-> "+truncate(target.Title, 30)))
		}
	}
	return strings.Join(parts, "  ")
}

// viewHelp is the key reference, shown by '?'.
func (m *Model) viewHelp() string {
	var b strings.Builder
	b.WriteString(styleBold.Render("gnotes"))
	b.WriteString("\n\n")

	section := func(title string, rows [][2]string) {
		b.WriteString(styleHeader.Render(title))
		b.WriteByte('\n')
		for _, r := range rows {
			b.WriteString(fmt.Sprintf("  %s  %s\n", pad(r[0], 14), styleDim.Render(r[1])))
		}
		b.WriteByte('\n')
	}

	section("move", [][2]string{
		{"j k / arrows", "up and down"},
		{"h l / tab", "between the panes"},
		{"g G", "first and last"},
		{"ctrl-d ctrl-u", "half a page"},
		{"enter", "open the entry"},
	})

	section("change", [][2]string{
		{"n", "new note"},
		{"t", "new task"},
		{"N", "new notebook"},
		{"space", "toggle a task done"},
		{"x s o", "done, doing, open"},
		{"e", "edit the body"},
		{"r", "rename"},
		{"d", "delete"},
		{"u", "undo the last delete"},
		{"J K", "reorder"},
	})

	section("find", [][2]string{
		{"/", "search titles, bodies and tags"},
		{"esc", "clear the search, then the filter"},
		{":filter", "narrow by kind, tag, status, priority"},
		{":sort", "reorder the list"},
	})

	var rows [][2]string
	for _, c := range tuiCommands {
		rows = append(rows, [2]string{":" + c.name + " " + c.args, c.summary})
	}
	section("commands", rows)

	b.WriteString(styleDim.Render("any key to go back"))
	return b.String()
}
