package tui

import (
	"fmt"
	"path"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/shakfu/gnotes/internal/display"
	"github.com/shakfu/gnotes/internal/wiki"
)

// View renders the whole screen: a header, the body and a status line.
func (m *WikiModel) View() string {
	if m.quitting {
		return ""
	}
	if m.width < 1 || m.height < 1 {
		return ""
	}
	if m.screen == screenHelp {
		return m.viewWikiHelp()
	}

	var body []string
	switch m.screen {
	case screenSearch:
		body = m.viewHits()
	case screenOpen:
		body = m.viewMatches()
	case screenBroken:
		body = m.viewBroken()
	case screenTasks:
		body = m.viewTasks()
	case screenOffers:
		body = m.viewOffers()
	default:
		body = m.viewRead()
	}

	h := m.bodyHeight()
	for len(body) < h {
		body = append(body, "")
	}
	lines := []string{clip(m.viewWikiHeader(), m.width)}
	for _, l := range body[:h] {
		lines = append(lines, clip(l, m.width))
	}
	if m.height > 1 {
		lines = append(lines, clip(m.viewWikiStatus(), m.width))
	}
	return strings.Join(lines[:min(len(lines), m.height)], "\n")
}

func (m *WikiModel) viewWikiHeader() string {
	left := styleHeader.Render(display.Line(m.w.Config.Name))
	if left == "" {
		left = styleHeader.Render("wiki")
	}
	switch m.screen {
	case screenSearch:
		left += "  search"
	case screenOpen:
		left += "  open"
	case screenBroken:
		left += fmt.Sprintf("  broken links (%d)", len(m.broken))
	case screenTasks:
		which := "open tasks"
		if m.allTasks {
			which = "all tasks"
		}
		left += fmt.Sprintf("  %s (%d)", which, len(m.tasks))
	case screenOffers:
		left += "  repairs for " + display.Line(m.offerFor.Written())
	default:
		if m.cur != nil {
			left += "  " + styleDim.Render(display.Line(m.cur.info.Path))
		}
	}
	right := ""
	if m.cur != nil && m.cur.broken > 0 && m.screen == screenRead {
		right = styleError.Render(fmt.Sprintf("%d broken", m.cur.broken))
	}
	gap := m.width - ansi.StringWidth(left) - ansi.StringWidth(right)
	if right == "" || gap < 2 {
		return left
	}
	return left + strings.Repeat(" ", gap) + right
}

func (m *WikiModel) viewWikiStatus() string {
	switch {
	case m.prompt != nil:
		return m.input.render(m.prompt.label, m.width)
	case m.screen == screenSearch:
		return m.input.render("/", m.width)
	case m.screen == screenOpen:
		return m.input.render("open: ", m.width)
	case m.status != "":
		if m.statusErr {
			return styleError.Render(display.Line(m.status))
		}
		return display.Line(m.status)
	}
	var hint string
	switch m.screen {
	case screenBroken:
		hint = "enter open  f repairs  esc back"
	case screenTasks:
		hint = "space toggle  a all/open  enter open  esc back"
	case screenOffers:
		hint = "enter apply  esc back"
	default:
		switch m.focus {
		case focusTree:
			hint = "enter open  l reader  / search  ^p open  n new  c broken  t tasks  ? help"
		case focusPanel:
			hint = "enter open the linking page  esc reader"
		default:
			hint = "tab link  enter follow  bksp back  b backlinks  e edit  r move  f fix  ? help"
		}
	}
	return styleDim.Render(hint)
}

// viewRead lays the tree beside the reader and its link panel.
func (m *WikiModel) viewRead() []string {
	h := m.bodyHeight()
	tw, rw := m.treeWidth(), m.readerWidth()
	tree := m.viewTree(tw, h)

	var right []string
	if rw > 0 {
		if m.cur == nil {
			right = []string{styleDim.Render(" no page open; n creates one")}
		} else {
			right = m.viewReader(rw, m.readerHeight())
			right = append(right, m.viewPanel(rw)...)
		}
	}

	out := make([]string, h)
	for i := range out {
		var b strings.Builder
		if tw > 0 {
			cell := ""
			if i < len(tree) {
				cell = tree[i]
			}
			b.WriteString(pad(clip(cell, tw), tw))
		}
		if rw > 0 {
			if tw > 0 {
				b.WriteString(styleDim.Render("|"))
			}
			if i < len(right) {
				b.WriteString(clip(right[i], rw))
			}
		}
		out[i] = b.String()
	}
	return out
}

func (m *WikiModel) viewTree(width, height int) []string {
	if width == 0 {
		return nil
	}
	m.treeCursor = max(0, min(m.treeCursor, len(m.rows)-1))
	if m.treeCursor < m.treeScroll {
		m.treeScroll = m.treeCursor
	}
	if m.treeCursor >= m.treeScroll+height {
		m.treeScroll = m.treeCursor - height + 1
	}
	if len(m.rows) == 0 {
		return []string{styleDim.Render(" no pages; n creates one")}
	}
	var out []string
	for i := m.treeScroll; i < len(m.rows) && len(out) < height; i++ {
		row := m.rows[i]
		indent := strings.Repeat("  ", row.depth)
		var text string
		if row.dir != "" {
			marker := "v "
			if m.collapsed[row.dir] {
				marker = "> "
			}
			text = indent + marker + display.Line(path.Base(row.dir)) + "/"
		} else {
			p := m.pages[row.page]
			name := p.Title
			if name == "" {
				name = path.Base(p.Path)
			}
			text = indent + "  " + display.Line(name)
			if m.cur != nil && p.Path == m.cur.info.Path {
				text = styleBold.Render(text)
			}
		}
		text = truncate(" "+text, width-1)
		if i == m.treeCursor && m.focus == focusTree && m.screen == screenRead {
			text = styleSelected.Render(pad(ansi.Strip(text), width-1))
		}
		out = append(out, text)
	}
	return out
}

func (m *WikiModel) viewReader(width, height int) []string {
	doc := m.doc()
	r := m.cur
	r.scroll = max(0, min(r.scroll, len(doc.Lines)-height))
	end := min(len(doc.Lines), r.scroll+height)
	out := make([]string, 0, height)
	for _, l := range doc.Lines[r.scroll:end] {
		out = append(out, " "+l)
	}
	for len(out) < height {
		out = append(out, "")
	}
	return out
}

// viewPanel is the link panel: counts, the selected link, and backlinks.
func (m *WikiModel) viewPanel(width int) []string {
	ph := m.panelHeight()
	if ph == 0 {
		return nil
	}
	r := m.cur
	lines := []string{styleDim.Render(strings.Repeat("-", width))}
	lines = append(lines, fmt.Sprintf(" links %d  backlinks %d  broken %d", len(m.doc().Links), len(r.backlinks), r.broken))

	if l, ok := m.selectedLink(); ok {
		text := " -> " + display.Line(l.Written())
		switch {
		case l.Status != wiki.StatusOK:
			text += "  " + styleError.Render(l.Status) + styleDim.Render("  f repairs")
		case l.Resolved != "":
			text += styleDim.Render("  " + display.Line(l.Resolved))
		}
		lines = append(lines, text)
	} else {
		lines = append(lines, styleDim.Render(" tab selects a link"))
	}

	room := ph - len(lines)
	start := 0
	if r.backCursor >= room {
		start = r.backCursor - room + 1
	}
	for i := start; i < len(r.backlinks) && len(lines) < ph; i++ {
		l := r.backlinks[i]
		text := fmt.Sprintf(" <- %s:%d  %s", display.Line(l.Page), l.Line, display.Line(l.Written()))
		if i == r.backCursor && m.focus == focusPanel {
			text = styleSelected.Render(pad(truncate(text, width), width))
		}
		lines = append(lines, text)
	}
	return lines
}

// listWindow returns the visible range of a list screen, scrolled to keep the
// cursor in view with rowsPer lines per item.
func (m *WikiModel) listWindow(n, rowsPer int) (int, int) {
	visible := max(1, m.bodyHeight()/rowsPer)
	m.cursor = max(0, min(m.cursor, n-1))
	if m.cursor < m.scroll {
		m.scroll = m.cursor
	}
	if m.cursor >= m.scroll+visible {
		m.scroll = m.cursor - visible + 1
	}
	return m.scroll, min(n, m.scroll+visible)
}

func (m *WikiModel) row(i int, text string) string {
	text = truncate(text, m.width)
	if i == m.cursor {
		return styleSelected.Render(pad(ansi.Strip(text), m.width))
	}
	return text
}

func pageLabel(p wiki.PageInfo) string {
	s := display.Line(p.Path)
	if p.Title != "" && p.Title != path.Base(p.Path) {
		s += "  " + display.Line(p.Title)
	}
	return s
}

func (m *WikiModel) viewHits() []string {
	if len(m.hits) == 0 {
		if strings.TrimSpace(m.input.String()) == "" {
			return []string{styleDim.Render(" type to search titles, headings, tags and text")}
		}
		return []string{styleDim.Render(" no pages match")}
	}
	start, end := m.listWindow(len(m.hits), 2)
	var out []string
	for i := start; i < end; i++ {
		h := m.hits[i]
		out = append(out, m.row(i, " "+pageLabel(h.PageInfo)))
		out = append(out, "   "+snippet(h.Snippet))
	}
	return out
}

// snippet draws a search snippet with its matches in bold.
func snippet(s string) string {
	var b strings.Builder
	for i, part := range strings.Split(strings.Join(strings.Fields(s), " "), "\x02") {
		match, rest, found := strings.Cut(part, "\x03")
		if i == 0 || !found {
			b.WriteString(styleDim.Render(display.Line(part)))
			continue
		}
		b.WriteString(styleBold.Render(display.Line(match)))
		b.WriteString(styleDim.Render(display.Line(rest)))
	}
	return b.String()
}

func (m *WikiModel) viewMatches() []string {
	if len(m.matches) == 0 {
		return []string{styleDim.Render(" no pages match")}
	}
	start, end := m.listWindow(len(m.matches), 1)
	var out []string
	for i := start; i < end; i++ {
		out = append(out, m.row(i, " "+pageLabel(m.matches[i])))
	}
	return out
}

func (m *WikiModel) viewBroken() []string {
	if len(m.broken) == 0 {
		return []string{styleOK.Render(" no broken links")}
	}
	start, end := m.listWindow(len(m.broken), 1)
	var out []string
	for i := start; i < end; i++ {
		l := m.broken[i]
		out = append(out, m.row(i, fmt.Sprintf(" %s:%d  %s  %s", display.Line(l.Page), l.Line, l.Status, display.Line(l.Written()))))
	}
	return out
}

func (m *WikiModel) viewTasks() []string {
	if len(m.tasks) == 0 {
		return []string{styleDim.Render(" no tasks")}
	}
	start, end := m.listWindow(len(m.tasks), 1)
	var out []string
	for i := start; i < end; i++ {
		t := m.tasks[i]
		var text string
		if t.Line == 0 {
			text = fmt.Sprintf(" [%s] %s  %s", t.Status, display.Line(t.Text), display.Line(t.Page))
		} else {
			box := "[ ]"
			if t.Status == "done" {
				box = "[x]"
			}
			text = fmt.Sprintf(" %s %s  %s:%d", box, display.Line(t.Text), display.Line(t.Page), t.Line)
		}
		if t.Due != "" {
			text += "  due " + t.Due
		}
		out = append(out, m.row(i, text))
	}
	return out
}

func (m *WikiModel) viewOffers() []string {
	l := m.offerFor
	out := []string{fmt.Sprintf(" %s:%d  %s  %s", display.Line(l.Page), l.Line, styleError.Render(l.Status), display.Line(l.Written())), ""}
	for i, o := range m.offers {
		out = append(out, m.row(i, fmt.Sprintf(" %d  %s  %s", i+1, display.Line(o.New), styleDim.Render(display.Line(o.Label)))))
	}
	return out
}

func (m *WikiModel) viewWikiHelp() string {
	var b strings.Builder
	b.WriteString(styleBold.Render("gnotes wiki") + "\n\n")
	section := func(title string, rows [][2]string) {
		b.WriteString(styleHeader.Render(title) + "\n")
		for _, r := range rows {
			fmt.Fprintf(&b, "  %s  %s\n", pad(r[0], 14), styleDim.Render(r[1]))
		}
		b.WriteByte('\n')
	}
	section("tree", [][2]string{
		{"j k g G", "move"},
		{"enter", "open a page, fold a directory"},
		{"l tab", "to the reader"},
	})
	section("reader", [][2]string{
		{"j k", "scroll"},
		{"space ctrl-d", "half a page down"},
		{"ctrl-u", "half a page up"},
		{"tab shift-tab", "next and previous link"},
		{"enter", "follow the link; a file opens in $EDITOR"},
		{"backspace", "back"},
		{"b", "backlinks"},
		{"e", "edit the page in $EDITOR"},
		{"r", "move the page, rewriting links"},
		{"f", "repairs for the selected broken link"},
		{"h", "to the tree"},
	})
	section("anywhere", [][2]string{
		{"/", "search"},
		{"ctrl-p", "open a page by title or path"},
		{"n", "new page"},
		{"c", "broken links"},
		{"t", "tasks; space toggles one"},
		{"esc", "back to the reader"},
		{"q ctrl-c", "quit"},
	})
	lines := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
	lines = append(lines, styleDim.Render("any key to go back"))
	for i, l := range lines {
		lines[i] = clip(l, m.width)
	}
	return strings.Join(lines[:min(len(lines), m.height)], "\n")
}
