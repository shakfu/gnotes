package tui

import (
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/shakfu/gwiki/internal/display"
	"github.com/shakfu/gwiki/internal/wiki"
)

// overview is what the latest and stats tabs show, loaded when they open and
// when pages change under them.
type overview struct {
	pages    int
	recent   []wiki.Change
	broken   int
	orphans  []wiki.PageInfo
	deadEnds []wiki.PageInfo
	dirs     []wiki.Count
	tags     []wiki.Count
	hubs     []wiki.Count
	titles   map[string]string
}

// Tab sizes: the pages the latest tab lists, and the most-linked pages.
const (
	homeRecent = 100
	homeHubs   = 10
)

func (m *WikiModel) loadHome() {
	o := &overview{pages: len(m.pages), titles: map[string]string{}}
	for _, p := range m.pages {
		o.titles[p.Path] = p.Title
	}
	errs := []error{}
	try := func(err error) {
		if err != nil {
			errs = append(errs, err)
		}
	}
	var err error
	o.recent, err = m.w.Recent(homeRecent)
	try(err)
	o.broken, err = m.w.BrokenCount()
	try(err)
	o.orphans, err = m.w.Orphans()
	try(err)
	o.deadEnds, err = m.w.DeadEnds()
	try(err)
	o.tags, err = m.w.TagCounts()
	try(err)
	o.hubs, err = m.w.Hubs(homeHubs)
	try(err)
	if len(errs) > 0 {
		m.setError(errs[0])
	}

	dirs := map[string]int{}
	for _, p := range m.pages {
		if d := path.Dir(p.Path); d != "." {
			dirs[strings.SplitN(d, "/", 2)[0]]++
		} else {
			dirs[""]++
		}
	}
	for d, n := range dirs {
		o.dirs = append(o.dirs, wiki.Count{Name: d, Pages: n})
	}
	sort.Slice(o.dirs, func(i, j int) bool {
		if o.dirs[i].Pages != o.dirs[j].Pages {
			return o.dirs[i].Pages > o.dirs[j].Pages
		}
		return o.dirs[i].Name < o.dirs[j].Name
	})
	m.home = o
}

// sortTasks puts dated tasks first, soonest first, then the rest in order.
func sortTasks(tasks []wiki.Task) {
	sort.SliceStable(tasks, func(i, j int) bool {
		a, b := tasks[i], tasks[j]
		if (a.Due == "") != (b.Due == "") {
			return a.Due != ""
		}
		return a.Due < b.Due
	})
}

// taskSummary counts the listed tasks: open, overdue and due within a week.
func (m *WikiModel) taskSummary() string {
	today := m.now().Format("2006-01-02")
	soon := m.now().AddDate(0, 0, 7).Format("2006-01-02")
	open, overdue, dueSoon := 0, 0, 0
	for _, t := range m.tasks {
		if t.Status == "done" {
			continue
		}
		open++
		switch {
		case t.Due == "":
		case t.Due < today:
			overdue++
		case t.Due <= soon:
			dueSoon++
		}
	}
	parts := []string{fmt.Sprintf("%d open", open)}
	if overdue > 0 {
		parts = append(parts, fmt.Sprintf("%d overdue", overdue))
	}
	if dueSoon > 0 {
		parts = append(parts, fmt.Sprintf("%d due within a week", dueSoon))
	}
	if m.allTasks {
		parts = append(parts, fmt.Sprintf("%d done", len(m.tasks)-open))
	}
	return strings.Join(parts, " · ")
}

// ---------------------------------------------------------------- latest

// viewLatest lists pages by when they last changed.
func (m *WikiModel) viewLatest() []string {
	o := m.home
	if o == nil || len(o.recent) == 0 {
		return []string{styleDim.Render(" no pages yet; n creates one")}
	}
	t := &table{cols: []column{{title: "PAGE", shrink: 2, min: 12}, {title: "PATH", shrink: 3, min: 8}, {title: "CHANGED", right: true}, {title: "AUTHOR", shrink: 4}, {}}}
	for _, c := range o.recent {
		var state cell
		if c.Uncommitted {
			state = text("●", styleWarn)
		}
		t.rows = append(t.rows, []cell{
			text(display.Line(c.Title), stylePlain),
			text(display.Line(c.Path), styleDim),
			text(ago(m.now(), c.Modified), styleDim),
			text(display.Line(c.Author), styleDim),
			state,
		})
	}
	return m.viewTable(t, nil)
}

// latestSummary is the latest tab's count, and the legend when a page is not
// committed.
func (m *WikiModel) latestSummary() string {
	o := m.home
	if o == nil {
		return ""
	}
	s := plural(o.pages, "page")
	for _, c := range o.recent {
		if c.Uncommitted {
			return s + " · ● uncommitted"
		}
	}
	return s
}

// ---------------------------------------------------------------- stats

// homeRow is one line of the stats tab; a row with an action can be selected.
type homeRow struct {
	text   string
	action func()
}

// homeWidths are the stats tab's column widths; right is zero below 100
// columns, where the tab is one column.
func (m *WikiModel) homeWidths() (left, right int) {
	if m.width < 100 {
		return m.width, 0
	}
	left = m.width / 2
	return left, m.width - left - 1
}

// statsColumns lays out the stats tab: health and the most-linked pages, then
// directories and tags.
func (m *WikiModel) statsColumns() [2][]homeRow {
	o := m.home
	if o == nil {
		return [2][]homeRow{}
	}
	lw, rw := m.homeWidths()
	if rw == 0 {
		rw = lw
	}
	var left, right []homeRow
	header := func(rows *[]homeRow, title, detail string) {
		if len(*rows) > 0 {
			*rows = append(*rows, homeRow{})
		}
		text := " " + styleHeader.Render(title)
		if detail != "" {
			text += "  " + styleDim.Render(detail)
		}
		*rows = append(*rows, homeRow{text: text})
	}
	// rowsOf lays out a section's table and pairs its rows with their actions.
	rowsOf := func(t *table, width int, actions []func()) []homeRow {
		t.layout(width)
		out := make([]homeRow, len(t.rows))
		for i, r := range t.rows {
			out[i] = homeRow{text: t.draw(r), action: actions[i]}
		}
		return out
	}
	listPages := func(title string, pages []wiki.PageInfo) func() {
		return func() {
			m.listTitle, m.listed = title, pages
			m.screen, m.cursor, m.scroll = screenPages, 0, 0
		}
	}

	header(&left, "Health", "")
	health := &table{cols: []column{{shrink: 1, min: 8}, {right: true}}}
	count := func(label string, n int) []cell {
		if n > 0 {
			return []cell{{{"✗ ", styleError}, {label, stylePlain}}, text(fmt.Sprint(n), styleError)}
		}
		return []cell{{{"✓ ", styleOK}, {label, stylePlain}}, text("0", styleOK)}
	}
	health.rows = [][]cell{count("broken links", o.broken), count("orphan pages", len(o.orphans)), count("dead ends", len(o.deadEnds))}
	left = append(left, rowsOf(health, lw, []func(){
		func() { m.showList(screenBroken) },
		listPages("orphan pages: nothing links to them", o.orphans),
		listPages("dead ends: they link to no page", o.deadEnds),
	})...)

	if len(o.hubs) > 0 {
		header(&left, "Most linked", "")
		hubs := &table{cols: []column{{shrink: 1, min: 8}, {right: true}}}
		var actions []func()
		for _, h := range o.hubs {
			title := o.titles[h.Name]
			if title == "" {
				title = h.Name
			}
			hubs.rows = append(hubs.rows, []cell{text(display.Line(title), stylePlain), text(fmt.Sprintf("← %d", h.Pages), styleDim)})
			page := h.Name
			actions = append(actions, func() {
				if err := m.open(page); err != nil {
					m.setError(err)
					return
				}
				m.focus = focusContent
			})
		}
		left = append(left, rowsOf(hubs, lw, actions)...)
	}

	header(&right, "Directories", plural(o.pages, "page"))
	dirs := &table{cols: []column{{shrink: 1, min: 8}, {right: true}}}
	var actions []func()
	for _, d := range o.dirs {
		name, dir := d.Name+"/", d.Name
		if d.Name == "" {
			name = "(top)"
		}
		dirs.rows = append(dirs.rows, []cell{text(display.Line(name), stylePlain), text(fmt.Sprint(d.Pages), styleDim)})
		actions = append(actions, func() {
			pages, err := m.w.Pages(dir, "")
			if err != nil {
				m.setError(err)
				return
			}
			if dir == "" {
				pages = pages[:0]
				for _, p := range m.pages {
					if !strings.Contains(p.Path, "/") {
						pages = append(pages, p)
					}
				}
			}
			listPages("pages in "+name, pages)()
		})
	}
	right = append(right, rowsOf(dirs, rw, actions)...)

	if len(o.tags) > 0 {
		header(&right, "Tags", "")
		tags := &table{cols: []column{{shrink: 1, min: 8}, {right: true}}}
		actions = nil
		for _, t := range o.tags {
			tag := t.Name
			tags.rows = append(tags.rows, []cell{text("#"+display.Line(tag), styleTag), text(fmt.Sprint(t.Pages), styleDim)})
			actions = append(actions, func() {
				pages, err := m.w.Pages("", tag)
				if err != nil {
					m.setError(err)
					return
				}
				listPages("pages tagged #"+tag, pages)()
			})
		}
		right = append(right, rowsOf(tags, rw, actions)...)
	}

	if m.width < 100 {
		return [2][]homeRow{append(append(left, homeRow{}), right...), nil}
	}
	return [2][]homeRow{left, right}
}

// ago is a short age: minutes, hours or days, and a date beyond a month.
func ago(now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
	return t.Format("2006-01-02")
}

// selectable are the indexes of rows with an action.
func selectable(rows []homeRow) []int {
	var out []int
	for i, r := range rows {
		if r.action != nil {
			out = append(out, i)
		}
	}
	return out
}

// homeSelection returns the stats tab's columns and the selectable rows of the
// current one.
func (m *WikiModel) homeSelection() ([2][]homeRow, []int) {
	cols := m.statsColumns()
	if cols[1] == nil {
		m.homeCol = 0
	}
	return cols, selectable(cols[m.homeCol])
}

func (m *WikiModel) homeMove(s func(pos, last int) int) {
	_, sel := m.homeSelection()
	m.homeRow[m.homeCol] = step(m.homeRow[m.homeCol], len(sel)-1, s)
}

func (m *WikiModel) homeSwitch() {
	if cols, _ := m.homeSelection(); cols[1] != nil {
		m.homeCol = 1 - m.homeCol
	}
}

func (m *WikiModel) homeEnter() {
	cols, sel := m.homeSelection()
	if pos := m.homeRow[m.homeCol]; pos < len(sel) {
		cols[m.homeCol][sel[pos]].action()
	}
}

func (m *WikiModel) viewStats() []string {
	cols := m.statsColumns()
	h := m.bodyHeight()
	render := func(col int, width int) []string {
		rows := cols[col]
		sel := selectable(rows)
		cursor := -1
		if len(sel) > 0 {
			m.homeRow[col] = min(m.homeRow[col], len(sel)-1)
			cursor = sel[m.homeRow[col]]
		}
		// Scroll so the selected row is on screen.
		start := 0
		if cursor >= h {
			start = cursor - h + 1
		}
		var out []string
		for i := start; i < len(rows) && len(out) < h; i++ {
			text := truncate(rows[i].text, width)
			if i == cursor && col == m.homeCol {
				text = selectRow(ansi.Strip(text), width)
			}
			out = append(out, text)
		}
		return out
	}
	if cols[1] == nil {
		return render(0, m.width)
	}
	lw, rw := m.homeWidths()
	left, right := render(0, lw), render(1, rw)
	out := make([]string, h)
	for i := range out {
		l, r := "", ""
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		out[i] = pad(clip(l, lw), lw) + styleDim.Render("│") + clip(r, rw)
	}
	return out
}
