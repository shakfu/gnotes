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

// overview is what the home screen shows, loaded when it opens and when pages
// change under it.
type overview struct {
	pages    int
	recent   []wiki.Change
	broken   int
	orphans  []wiki.PageInfo
	deadEnds []wiki.PageInfo
	tasks    []wiki.Task
	overdue  int
	dueSoon  int
	dirs     []wiki.Count
	tags     []wiki.Count
	hubs     []wiki.Count
	titles   map[string]string
}

// Section sizes on the overview.
const (
	homeRecent = 10
	homeTasks  = 8
	homeCounts = 6
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
	o.hubs, err = m.w.Hubs(homeCounts)
	try(err)
	o.tasks, err = m.w.Tasks(wiki.TaskFilter{Status: "open"})
	try(err)
	if len(errs) > 0 {
		m.setError(errs[0])
	}

	today := m.now().Format("2006-01-02")
	soon := m.now().AddDate(0, 0, 7).Format("2006-01-02")
	for _, t := range o.tasks {
		switch {
		case t.Due == "":
		case t.Due < today:
			o.overdue++
		case t.Due <= soon:
			o.dueSoon++
		}
	}
	// Dated tasks first, soonest first; then the rest by page.
	sort.SliceStable(o.tasks, func(i, j int) bool {
		a, b := o.tasks[i], o.tasks[j]
		if (a.Due == "") != (b.Due == "") {
			return a.Due != ""
		}
		return a.Due < b.Due
	})

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

// homeRow is one line of the overview; a row with an action can be selected.
type homeRow struct {
	text   string
	action func()
}

// homeColumns lays out the overview: recent changes and tasks, then health
// and structure.
func (m *WikiModel) homeColumns() [2][]homeRow {
	o := m.home
	if o == nil {
		return [2][]homeRow{}
	}
	var left, right []homeRow
	header := func(rows *[]homeRow, title, detail string) {
		if len(*rows) > 0 {
			*rows = append(*rows, homeRow{})
		}
		text := styleHeader.Render(title)
		if detail != "" {
			text += "  " + styleDim.Render(detail)
		}
		*rows = append(*rows, homeRow{text: text})
	}
	openPage := func(page string) func() {
		return func() {
			if err := m.open(page); err != nil {
				m.setError(err)
				return
			}
			m.focus = focusReader
		}
	}
	listPages := func(title string, pages []wiki.PageInfo) func() {
		return func() {
			m.listTitle, m.listed = title, pages
			m.screen, m.cursor, m.scroll = screenPages, 0, 0
		}
	}

	header(&left, "Recent changes", "")
	if len(o.recent) == 0 {
		left = append(left, homeRow{text: styleDim.Render("  no pages yet; n creates one")})
	}
	for _, c := range o.recent {
		when := ago(m.now(), c.Modified)
		detail := when
		if c.Author != "" {
			detail += "  " + display.Line(c.Author)
		}
		if c.Uncommitted {
			detail += "  " + styleDoing.Render("uncommitted")
		}
		left = append(left, homeRow{
			text:   "  " + display.Line(c.Title) + "  " + styleDim.Render(display.Line(c.Path)+"  "+detail),
			action: openPage(c.Path),
		})
	}

	taskDetail := fmt.Sprintf("%d open", len(o.tasks))
	if o.overdue > 0 {
		taskDetail += fmt.Sprintf(", %d overdue", o.overdue)
	}
	if o.dueSoon > 0 {
		taskDetail += fmt.Sprintf(", %d due within a week", o.dueSoon)
	}
	header(&left, "Tasks", taskDetail)
	today := m.now().Format("2006-01-02")
	for i, t := range o.tasks {
		if i == homeTasks {
			left = append(left, homeRow{text: styleDim.Render(fmt.Sprintf("  %d more; t lists them all", len(o.tasks)-homeTasks)), action: func() {
				m.screen, m.cursor, m.scroll = screenTasks, 0, 0
				m.loadTasks()
			}})
			break
		}
		where := t.Page
		if t.Line > 0 {
			where = fmt.Sprintf("%s:%d", t.Page, t.Line)
		}
		text := "  " + display.Line(t.Text) + "  " + styleDim.Render(display.Line(where))
		switch {
		case t.Due != "" && t.Due < today:
			text += "  " + styleOverdue.Render("overdue "+t.Due)
		case t.Due != "":
			text += "  " + styleDue.Render("due "+t.Due)
		}
		page, line := t.Page, t.Line
		left = append(left, homeRow{text: text, action: func() { m.openAt(page, line) }})
	}

	header(&right, "Health", "")
	count := func(label string, n int, action func()) homeRow {
		style := styleOK
		if n > 0 {
			style = styleError
		}
		return homeRow{text: fmt.Sprintf("  %-14s %s", label, style.Render(fmt.Sprint(n))), action: action}
	}
	right = append(right,
		count("broken links", o.broken, func() {
			m.screen, m.cursor, m.scroll = screenBroken, 0, 0
			m.loadBroken()
		}),
		count("orphan pages", len(o.orphans), listPages("orphan pages: nothing links to them", o.orphans)),
		count("dead ends", len(o.deadEnds), listPages("dead ends: they link to no page", o.deadEnds)),
	)

	header(&right, "Structure", fmt.Sprintf("%d pages", o.pages))
	for i, d := range o.dirs {
		if i == homeCounts {
			break
		}
		name, dir := d.Name+"/", d.Name
		if d.Name == "" {
			name = "(top)"
		}
		right = append(right, homeRow{
			text: fmt.Sprintf("  %s  %s", display.Line(name), styleDim.Render(fmt.Sprint(d.Pages))),
			action: func() {
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
			},
		})
	}
	for i, t := range o.tags {
		if i == homeCounts {
			break
		}
		tag := t.Name
		right = append(right, homeRow{
			text: fmt.Sprintf("  %s  %s", styleTag.Render("#"+display.Line(tag)), styleDim.Render(fmt.Sprint(t.Pages))),
			action: func() {
				pages, err := m.w.Pages("", tag)
				if err != nil {
					m.setError(err)
					return
				}
				listPages("pages tagged #"+tag, pages)()
			},
		})
	}
	if len(o.hubs) > 0 {
		right = append(right, homeRow{text: styleDim.Render("  most linked")})
	}
	for _, h := range o.hubs {
		title := o.titles[h.Name]
		if title == "" {
			title = h.Name
		}
		right = append(right, homeRow{
			text:   fmt.Sprintf("  %s  %s", display.Line(title), styleDim.Render(fmt.Sprintf("<- %d", h.Pages))),
			action: openPage(h.Name),
		})
	}

	if m.width < 100 {
		if len(left) > 0 {
			left = append(left, homeRow{})
		}
		return [2][]homeRow{append(left, right...), nil}
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

func (m *WikiModel) keyHome(k string) {
	cols := m.homeColumns()
	if cols[1] == nil {
		m.homeCol = 0
	}
	sel := selectable(cols[m.homeCol])
	pos := m.homeRow[m.homeCol]
	switch k {
	case "j", "down":
		m.homeRow[m.homeCol] = min(pos+1, max(0, len(sel)-1))
	case "k", "up":
		m.homeRow[m.homeCol] = max(pos-1, 0)
	case "g", "home":
		m.homeRow[m.homeCol] = 0
	case "G", "end":
		m.homeRow[m.homeCol] = max(0, len(sel)-1)
	case "h", "left", "l", "right", "tab":
		if cols[1] != nil {
			m.homeCol = 1 - m.homeCol
		}
	case "enter":
		if pos < len(sel) {
			cols[m.homeCol][sel[pos]].action()
		}
	case "r":
		if err := m.loadPages(); err != nil {
			m.setError(err)
		}
		m.loadHome()
		m.setStatus("overview reloaded")
	}
}

func (m *WikiModel) viewHome() []string {
	cols := m.homeColumns()
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
				text = styleSelected.Render(pad(ansi.Strip(text), width))
			}
			out = append(out, text)
		}
		return out
	}
	if cols[1] == nil {
		return render(0, m.width)
	}
	lw := m.width * 3 / 5
	rw := m.width - lw - 1
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
		out[i] = pad(clip(l, lw), lw) + styleDim.Render("|") + clip(r, rw)
	}
	return out
}

func (m *WikiModel) viewPages() []string {
	if len(m.listed) == 0 {
		return []string{styleDim.Render(" none")}
	}
	start, end := m.listWindow(len(m.listed), 1)
	var out []string
	for i := start; i < end; i++ {
		out = append(out, m.row(i, " "+pageLabel(m.listed[i])))
	}
	return out
}
