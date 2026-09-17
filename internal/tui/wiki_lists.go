package tui

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/shakfu/gwiki/internal/display"
	"github.com/shakfu/gwiki/internal/wiki"
)

// listWindow returns the visible range of a list screen with reserved rows
// above it, scrolled to keep the cursor in view with rowsPer lines per item.
func (m *WikiModel) listWindow(n, rowsPer, reserved int) (int, int) {
	visible := max(1, (m.bodyHeight()-reserved)/rowsPer)
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
		return selectRow(ansi.Strip(text), m.width)
	}
	return text
}

// viewTable draws a list screen's rows under their column titles.
func (m *WikiModel) viewTable(t *table, above []string) []string {
	t.layout(m.width)
	out := append(above, t.header())
	start, end := m.listWindow(len(t.rows), 1, len(out))
	for i := start; i < end; i++ {
		out = append(out, m.row(i, t.draw(t.rows[i])))
	}
	return out
}

// ---------------------------------------------------------------- pages

var pageColumns = []column{{title: "TITLE", shrink: 1, min: 12}, {title: "PATH", shrink: 2, min: 8}, {title: "TAGS", shrink: 3}}

func pageCells(p wiki.PageInfo) []cell {
	title := p.Title
	if title == "" {
		title = path.Base(p.Path)
	}
	var tags cell
	for i, tag := range p.Tags {
		if i > 0 {
			tags = append(tags, plain(" "))
		}
		tags = append(tags, seg{"#" + display.Line(tag), styleTag})
	}
	return []cell{text(display.Line(title), stylePlain), text(display.Line(p.Path), styleDim), tags}
}

func (m *WikiModel) viewPageList(pages []wiki.PageInfo, empty string) []string {
	if len(pages) == 0 {
		return []string{styleDim.Render(" " + empty)}
	}
	t := &table{cols: pageColumns}
	for _, p := range pages {
		t.rows = append(t.rows, pageCells(p))
	}
	return m.viewTable(t, nil)
}

func (m *WikiModel) viewMatches() []string { return m.viewPageList(m.matches, "no pages match") }
func (m *WikiModel) viewPages() []string   { return m.viewPageList(m.listed, "none") }

// ---------------------------------------------------------------- search

func (m *WikiModel) viewHits() []string {
	if len(m.hits) == 0 {
		if strings.TrimSpace(m.input.String()) == "" {
			return []string{styleDim.Render(" type to search titles, headings, tags and text")}
		}
		return []string{styleDim.Render(" no pages match")}
	}
	t := &table{cols: []column{{shrink: 1, min: 12}, {shrink: 2, min: 8}}}
	for _, h := range m.hits {
		cells := pageCells(h.PageInfo)
		cells[0][0].style = styleBold
		t.rows = append(t.rows, cells[:2])
	}
	t.layout(m.width)
	start, end := m.listWindow(len(m.hits), 2, 0)
	var out []string
	for i := start; i < end; i++ {
		out = append(out, m.row(i, t.draw(t.rows[i])), "   "+snippet(m.hits[i].Snippet, m.width-3))
	}
	return out
}

// Markdown syntax removed from a snippet, which is page source.
var (
	snipWiki = regexp.MustCompile(`\[\[(?:[^\]|]*\|)?([^\]]*)\]\]`)
	snipLink = regexp.MustCompile(`!?\[([^\]]*)\]\([^)]*\)`)
	// Heading and list markers only at the start of a line, or "version 5. See"
	// loses "5. " and "# is" loses "# ".
	snipHeading = regexp.MustCompile(`(?m)^([ \t]*)#{1,6}[ \t]`)
	snipItem    = regexp.MustCompile(`(?m)^([ \t]*)(?:[-*+]|\d+[.)])[ \t](?:\[[ xX]\][ \t])?`)
	snipRule    = regexp.MustCompile("```\\w*|-{3,}|[|`]|\\*\\*|__")
)

// inlineText is a line of markdown with its links shown as their labels.
func inlineText(s string) string {
	return snipLink.ReplaceAllString(snipWiki.ReplaceAllString(s, "$1"), "$1")
}

// snippet draws a search snippet in width columns: page source with the
// markdown syntax taken out, and the matches highlighted. \x02 and \x03 mark
// where a match starts and ends.
func snippet(s string, width int) string {
	s = inlineText(s)
	s = snipItem.ReplaceAllString(s, "$1")
	s = snipHeading.ReplaceAllString(s, "$1")
	s = snipRule.ReplaceAllString(s, " ")
	var segs []seg
	for i, part := range strings.Split(strings.Join(strings.Fields(s), " "), "\x02") {
		match, rest, found := strings.Cut(part, "\x03")
		if i == 0 || !found {
			segs = append(segs, seg{display.Line(part), styleDim})
			continue
		}
		segs = append(segs, seg{display.Line(match), styleMatch}, seg{display.Line(rest), styleDim})
	}
	out, _ := drawSegs(segs, width, same)
	return out
}

// ---------------------------------------------------------------- broken links

func (m *WikiModel) viewBroken() []string {
	if len(m.broken) == 0 {
		return []string{styleOK.Render(" no broken links")}
	}
	t := &table{cols: []column{{title: "LINK", shrink: 1, min: 12}, {title: "PROBLEM"}, {title: "WHERE", shrink: 2, min: 8}}}
	for _, l := range m.broken {
		t.rows = append(t.rows, []cell{
			text(display.Line(l.Written()), stylePlain),
			text(strings.ReplaceAll(l.Status, "-", " "), styleError),
			text(fmt.Sprintf("%s:%d", display.Line(l.Page), l.Line), styleDim),
		})
	}
	return m.viewTable(t, nil)
}

func (m *WikiModel) viewOffers() []string {
	l := m.offerFor
	above := []string{
		fmt.Sprintf(" %s  %s  %s", display.Line(l.Written()), styleError.Render(strings.ReplaceAll(l.Status, "-", " ")), styleDim.Render(fmt.Sprintf("%s:%d", display.Line(l.Page), l.Line))),
		"",
	}
	t := &table{cols: []column{{title: "#"}, {title: "REPAIR", shrink: 2, min: 12}, {title: "WHAT IT DOES", shrink: 1, min: 8}}}
	for i, o := range m.offers {
		t.rows = append(t.rows, []cell{text(fmt.Sprint(i+1), styleDim), text(display.Line(o.New), stylePlain), text(display.Line(o.Label), styleDim)})
	}
	return m.viewTable(t, above)
}

// ---------------------------------------------------------------- tasks

var taskColumns = []column{{}, {title: "TASK", shrink: 1, min: 12}, {title: "WHERE", shrink: 2, min: 8}, {title: "DUE"}}

// taskCells are a task's status, text, place and due date.
func (m *WikiModel) taskCells(t wiki.Task) []cell {
	glyph := text("☐", stylePlain)
	textStyle := stylePlain
	switch t.Status {
	case "doing":
		glyph = text("◐", styleWarn)
	case "done":
		glyph, textStyle = text("☑", styleOK), styleDim
	}
	var task cell
	if strings.EqualFold(t.Priority, "high") {
		task = append(task, seg{"! ", styleError})
	}
	task = append(task, seg{display.Line(inlineText(t.Text)), textStyle})
	where := t.Page
	if t.Line > 0 {
		where = fmt.Sprintf("%s:%d", t.Page, t.Line)
	}
	return []cell{glyph, task, text(display.Line(where), styleDim), m.dueCell(t.Due, t.Status == "done")}
}

// dueCell is a due date: red and marked when past, yellow within a week.
func (m *WikiModel) dueCell(due string, done bool) cell {
	today := m.now().Format("2006-01-02")
	soon := m.now().AddDate(0, 0, 7).Format("2006-01-02")
	switch {
	case due == "":
		return nil
	case done:
		return text(due, styleDim)
	case due < today:
		return text(due+" ✗", styleError)
	case due <= soon:
		return text(due, styleWarn)
	}
	return text(due, styleDim)
}

func (m *WikiModel) viewTasks() []string {
	if len(m.tasks) == 0 {
		return []string{styleDim.Render(" no tasks")}
	}
	t := &table{cols: taskColumns}
	for _, task := range m.tasks {
		t.rows = append(t.rows, m.taskCells(task))
	}
	return m.viewTable(t, nil)
}
