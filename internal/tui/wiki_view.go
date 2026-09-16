package tui

import (
	"fmt"
	"path"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/shakfu/gwiki/internal/display"
)

// View renders the whole screen: a header bar, the body and a status bar.
func (m *WikiModel) View() string {
	if m.quitting {
		return ""
	}
	if m.width < 1 || m.height < 1 {
		return ""
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
	case screenLatest:
		body = m.viewLatest()
	case screenStats:
		body = m.viewStats()
	case screenPages:
		body = m.viewPages()
	case screenHelp:
		body = m.viewHelp()
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

// titleOf is a page's title, or its path when it has none.
func (m *WikiModel) titleOf(page string) string {
	for _, p := range m.pages {
		if p.Path == page && p.Title != "" {
			return p.Title
		}
	}
	return page
}

// viewWikiHeader is the header bar: the project, where the view is, and facts
// about the open page.
func (m *WikiModel) viewWikiHeader() string {
	name := display.Line(m.w.Config.Name)
	if name == "" {
		name = "wiki"
	}
	left := []seg{plain(" "), {name, styleHeader}, plain("  ")}
	var right []seg
	if isTab(m.screen) {
		// The overview's header is its tab bar.
		for _, tab := range overviewTabs {
			style := styleDim
			if tab == m.screen {
				style = styleTab
			}
			left = append(left, seg{" " + tabName(tab) + " ", style}, plain(" "))
		}
		summary := m.latestSummary()
		if m.screen == screenTasks {
			summary = m.taskSummary()
		}
		if m.screen != screenStats && summary != "" {
			right = []seg{{summary + " ", styleDim}}
		}
		return bar(m.width, left, right)
	}
	page := func(path string) {
		left = append(left, seg{display.Line(m.titleOf(path)), styleBold}, seg{" · " + display.Line(path), styleDim})
	}
	switch m.screen {
	case screenSearch:
		left = append(left, plain("search"))
	case screenOpen:
		left = append(left, plain("open"))
	case screenBroken:
		left = append(left, plain("broken links"), seg{fmt.Sprintf("  %d", len(m.broken)), styleDim})
	case screenOffers:
		left = append(left, plain("repairs for "+display.Line(m.offerFor.Written())))
	case screenPages:
		left = append(left, plain(display.Line(m.listTitle)), seg{fmt.Sprintf("  %d", len(m.listed)), styleDim})
	case screenHelp:
		left = append(left, plain("keys"))
	default:
		if r := m.cur; r != nil {
			page(r.info.Path)
			if r.broken > 0 {
				right = append(right, seg{fmt.Sprintf("✗ %d broken  ", r.broken), styleError})
			}
			right = append(right, seg{plural(r.links, "link") + "  " + plural(len(r.backlinks), "backlink") + " ", styleDim})
		}
	}
	return bar(m.width, left, right)
}

// tabName is an overview tab's name in the header bar.
func tabName(tab wscreen) string {
	switch tab {
	case screenTasks:
		return "tasks"
	case screenStats:
		return "stats"
	}
	return "latest"
}

// screenLabel names the screen, or the focused part of the reader, at the
// left of the status bar.
func (m *WikiModel) screenLabel() string {
	switch m.screen {
	case screenLatest, screenTasks, screenStats:
		return strings.ToUpper(tabName(m.screen))
	case screenBroken:
		return "BROKEN"
	case screenOffers:
		return "REPAIRS"
	case screenPages:
		return "PAGES"
	case screenHelp:
		return "HELP"
	}
	switch {
	case m.focus == focusTree || m.cur == nil:
		return "TREE"
	case m.focus == focusPanel:
		return "BACKLINKS"
	}
	return m.edit.ed.Mode.String()
}

// viewWikiStatus is the status bar, or the line being typed into.
func (m *WikiModel) viewWikiStatus() string {
	switch {
	case m.prompt != nil:
		return m.input.render(m.prompt.label, m.width)
	case m.screen == screenRead && m.focus == focusContent && m.edit != nil:
		return m.viewEditStatus()
	case m.screen == screenSearch:
		return m.input.render("/", m.width)
	case m.screen == screenOpen:
		return m.input.render("open: ", m.width)
	}
	left := []seg{plain(" "), {m.screenLabel(), styleHeader}, plain("  ")}
	switch {
	case m.status != "" && m.statusErr:
		left = append(left, seg{display.Line(m.status), styleError})
	case m.status != "":
		left = append(left, plain(display.Line(m.status)))
	default:
		room := m.width - ansi.StringWidth(" "+m.screenLabel()+"  ") - len(helpHint) - 3
		left = append(left, seg{fitHints(m.hints(), room), styleDim})
	}
	right := []seg{{helpHint + " ", styleDim}}
	switch m.screen {
	case screenHelp:
		right = nil
	case screenLatest, screenBroken, screenTasks, screenOffers, screenPages:
		if n := m.listLen(); n > 0 {
			right = append([]seg{{fmt.Sprintf("%d/%d  ", min(m.cursor+1, n), n), styleDim}}, right...)
		}
	}
	return bar(m.width, left, right)
}

// viewRead lays the tree beside the reader and its link panel.
func (m *WikiModel) viewRead() []string {
	h := m.bodyHeight()
	tw, rw := m.treeWidth(), m.contentWidth()
	tree := m.viewTree(tw, h)

	var right []string
	if rw > 0 {
		if m.cur == nil {
			right = []string{styleDim.Render(" no page open; n creates one")}
		} else {
			if meta := m.metaLine(); m.metaHeight() > 0 {
				line, _ := drawSegs(append([]seg{plain(" ")}, meta...), rw, same)
				right = append(right, line)
			}
			buffer := m.viewBuffer(rw, m.contentHeight())
			for len(buffer) < m.contentHeight() {
				buffer = append(buffer, "")
			}
			right = append(right, buffer...)
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
				b.WriteString(styleDim.Render("│"))
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
			marker := "▾ "
			if m.collapsed[row.dir] {
				marker = "▸ "
			}
			name := path.Base(row.dir) + "/"
			if row.hasPage {
				// The README's title, or the directory's name.
				name = m.pages[row.page].Title
			}
			text = indent + marker + display.Line(name)
			if row.hasPage && m.cur != nil && m.pages[row.page].Path == m.cur.info.Path {
				text = styleBold.Render(text)
			}
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
		if i == m.treeCursor && m.screen == screenRead {
			if m.focus == focusTree {
				text = selectRow(ansi.Strip(text), width-1)
			} else {
				// The row h returns to.
				text = styleDim.Render(markInactive) + text[1:]
			}
		}
		out = append(out, text)
	}
	return out
}

// metaLine is the open page's front matter: type, status, priority, due date
// and tags.
func (m *WikiModel) metaLine() []seg {
	p := m.cur.info
	var out []seg
	add := func(s seg) {
		if len(out) > 0 {
			out = append(out, plain("  "))
		}
		out = append(out, s)
	}
	for _, field := range []string{p.Type, p.Status} {
		if field != "" {
			add(seg{display.Line(field), styleDim})
		}
	}
	if p.Priority != "" {
		style := styleDim
		if strings.EqualFold(p.Priority, "high") {
			style = styleError
		}
		add(seg{"!" + display.Line(p.Priority), style})
	}
	if due := m.dueCell(p.Due, p.Status == "done"); len(due) > 0 {
		add(seg{"due " + due[0].text, due[0].style})
	}
	for _, tag := range p.Tags {
		add(seg{"#" + display.Line(tag), styleTag})
	}
	return out
}

// viewPanel is the backlinks panel: a titled rule, then a row per linking
// page.
func (m *WikiModel) viewPanel(width int) []string {
	ph := m.panelHeight()
	if ph == 0 {
		return nil
	}
	r := m.cur
	title := fmt.Sprintf(" ── %s from %s ", plural(len(r.backlinks), "backlink"), plural(len(r.linkers), "page"))
	lines := []string{styleDim.Render(truncate(title+strings.Repeat("─", max(0, width-ansi.StringWidth(title))), width))}

	t := &table{cols: []column{{shrink: 1, min: 8}, {shrink: 2}, {right: true}}}
	for _, l := range r.linkers {
		count := ""
		if l.links > 1 {
			count = fmt.Sprintf("×%d", l.links)
		}
		t.rows = append(t.rows, []cell{
			text("← "+display.Line(m.titleOf(l.page)), stylePlain),
			text(display.Line(l.page), styleDim),
			text(count, styleDim),
		})
	}
	t.layout(width)
	room := ph - 1
	start := max(0, r.backCursor-room+1)
	for i := start; i < len(t.rows) && len(lines) < ph; i++ {
		text := t.draw(t.rows[i])
		if i == r.backCursor && m.focus == focusPanel {
			text = selectRow(ansi.Strip(text), width)
		}
		lines = append(lines, text)
	}
	return lines
}
