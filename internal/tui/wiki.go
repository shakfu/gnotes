package tui

// The wiki interface: a page tree, a reader with a link panel, search, quick
// open, broken links and tasks. It sits beside the notes interface until the
// wiki replaces it; see docs/dev/wiki-design.md, section 12.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/shakfu/gwiki/internal/editor"
	"github.com/shakfu/gwiki/internal/markdown"
	"github.com/shakfu/gwiki/internal/render"
	"github.com/shakfu/gwiki/internal/wiki"
)

// wscreen is what the body of the wiki interface shows.
type wscreen int

const (
	screenHome   wscreen = iota // the overview
	screenRead                  // tree and reader
	screenSearch                // '/' results as you type
	screenOpen                  // ctrl-p quick open
	screenBroken                // 'c' broken links across the wiki
	screenTasks                 // 't' tasks
	screenOffers                // 'f' repairs for one link
	screenPages                 // a list of pages, such as a tag's
	screenEdit                  // the page in the editor
	screenHelp
)

// wfocus is the focused part of the read screen.
type wfocus int

const (
	focusTree wfocus = iota
	focusReader
	focusPanel // the backlinks under the page
)

// WikiModel is the wiki interface state.
type WikiModel struct {
	w      *wiki.Wiki
	styles render.Styles

	width, height int

	screen wscreen
	focus  wfocus

	// base is the screen that lists and prompts return to: the overview or
	// the reader, whichever was used last.
	base wscreen

	edit      *editing
	clipboard string
	home      *overview
	homeCol   int
	homeRow   [2]int
	listTitle string
	listed    []wiki.PageInfo

	// input backs the search and open lines and prompts.
	input  input
	prompt *wikiPrompt

	pages      []wiki.PageInfo
	rows       []treeRow
	collapsed  map[string]bool
	treeCursor int
	treeScroll int

	cur     *reading
	history []place

	// cursor and scroll are the position in whichever list screen is open.
	cursor, scroll int
	hits           []wiki.Hit
	matches        []wiki.PageInfo
	broken         []wiki.Link
	tasks          []wiki.Task
	allTasks       bool
	offers         []wiki.Offer
	offerFor       wiki.Link
	offerFrom      wscreen

	status    string
	statusErr bool

	getenv   func(string) string
	now      func() time.Time
	quitting bool

	// exec runs a program with the terminal; tests replace it.
	exec func(*exec.Cmd, tea.ExecCallback) tea.Cmd

	// after is a command queued by a key for Update to return.
	after tea.Cmd
}

// reading is the open page.
type reading struct {
	info      wiki.PageInfo
	src       []byte
	hash      string
	links     map[string]wiki.Link
	backlinks []wiki.Link
	broken    int

	doc                   *render.Doc
	docWidth, docSelected int

	selected   int // index into doc.Links, or -1
	scroll     int
	backCursor int
}

// place is an entry in the back stack.
type place struct {
	page             string
	scroll, selected int
}

// treeRow is a directory or a page in the tree.
type treeRow struct {
	dir   string // a directory's path, empty for a page
	page  int    // index into pages
	depth int
}

// NewWiki builds the interface over an open wiki.
func NewWiki(w *wiki.Wiki) (*WikiModel, error) {
	m := &WikiModel{
		w:         w,
		styles:    render.DefaultStyles(),
		width:     80,
		height:    24,
		collapsed: map[string]bool{},
		getenv:    os.Getenv,
		now:       time.Now,
		exec:      tea.ExecProcess,
	}
	if err := m.loadPages(); err != nil {
		return nil, err
	}
	m.loadHome()
	// The page the reader starts on: index, else the first page.
	for _, p := range m.pages {
		if p.Path == "index" {
			return m, m.load(p.Path)
		}
	}
	if len(m.pages) > 0 {
		return m, m.load(m.pages[0].Path)
	}
	return m, nil
}

// RunWiki opens the wiki interface and returns when the user leaves.
func RunWiki(w *wiki.Wiki) error {
	m, err := NewWiki(w)
	if err != nil {
		return err
	}
	if _, err := tea.NewProgram(m, tea.WithAltScreen()).Run(); err != nil {
		return fmt.Errorf("interface: %w", err)
	}
	return nil
}

// Init starts polling for outside changes.
func (m *WikiModel) Init() tea.Cmd { return poll() }

func (m *WikiModel) setStatus(s string) { m.status, m.statusErr = s, false }
func (m *WikiModel) setError(err error) { m.status, m.statusErr = err.Error(), true }

// ---------------------------------------------------------------- pages

func (m *WikiModel) loadPages() error {
	pages, err := m.w.Pages("", "")
	if err != nil {
		return err
	}
	m.pages = pages
	m.buildTree()
	return nil
}

// buildTree lays out directories and pages, skipping what a collapsed
// directory holds.
func (m *WikiModel) buildTree() {
	var selected string
	if m.treeCursor < len(m.rows) {
		selected = m.rowKey(m.rows[m.treeCursor])
	}
	m.rows = m.rows[:0]
	shown := map[string]bool{}
	for i, p := range m.pages {
		parts := strings.Split(p.Path, "/")
		hidden := false
		for d := 1; d < len(parts) && !hidden; d++ {
			dir := strings.Join(parts[:d], "/")
			if !shown[dir] {
				shown[dir] = true
				m.rows = append(m.rows, treeRow{dir: dir, depth: d - 1})
			}
			hidden = m.collapsed[dir]
		}
		if !hidden {
			m.rows = append(m.rows, treeRow{page: i, depth: len(parts) - 1})
		}
	}
	m.treeCursor = 0
	for i, r := range m.rows {
		if m.rowKey(r) == selected {
			m.treeCursor = i
		}
	}
}

func (m *WikiModel) rowKey(r treeRow) string {
	if r.dir != "" {
		return r.dir + "/"
	}
	if r.page < len(m.pages) {
		return m.pages[r.page].Path
	}
	return ""
}

// open reads a page into the reader, remembering the page it replaces.
func (m *WikiModel) open(page string) error {
	if m.cur != nil && m.cur.info.Path != page && m.base == screenRead {
		m.history = append(m.history, place{m.cur.info.Path, m.cur.scroll, m.cur.selected})
	}
	if err := m.load(page); err != nil {
		return err
	}
	m.screen, m.base = screenRead, screenRead
	return nil
}

// load reads a page into the reader without touching the back stack.
func (m *WikiModel) load(page string) error {
	src, hash, err := m.w.Read(page)
	if err != nil {
		return err
	}
	full, err := m.w.Page(page)
	if err != nil {
		return err
	}
	r := &reading{info: full.PageInfo, src: src, hash: hash, links: map[string]wiki.Link{}, backlinks: full.Backlinks, selected: -1}
	for _, l := range full.Links {
		r.links[linkKey(string(l.Form), l.Target, l.Anchor)] = l
		if l.Status != wiki.StatusOK {
			r.broken++
		}
	}
	m.cur = r
	for i, row := range m.rows {
		if row.dir == "" && m.pages[row.page].Path == page {
			m.treeCursor = i
		}
	}
	return nil
}

// reload reads the open page again after a change, keeping the position.
func (m *WikiModel) reload() {
	if m.cur == nil {
		return
	}
	old := m.cur
	if err := m.load(old.info.Path); err != nil {
		m.cur = nil
		m.setError(fmt.Errorf("%s: %w", old.info.Path, err))
		return
	}
	m.cur.scroll, m.cur.selected, m.cur.backCursor = old.scroll, old.selected, old.backCursor
}

func linkKey(form, target, anchor string) string {
	return form + "\x00" + target + "\x00" + anchor
}

// cacheLink is the indexed link for a drawn one.
func (r *reading) cacheLink(l render.Link) (wiki.Link, bool) {
	c, ok := r.links[linkKey(string(l.Form), l.Target, l.Anchor)]
	return c, ok
}

// ---------------------------------------------------------------- layout

const wikiTreeWidth = 28

// treeWidth is the tree's column, zero when the terminal is too narrow to
// show it beside the reader.
func (m *WikiModel) treeWidth() int {
	if m.width < 70 {
		if m.focus == focusTree || m.cur == nil {
			return m.width
		}
		return 0
	}
	return wikiTreeWidth
}

func (m *WikiModel) bodyHeight() int { return max(1, m.height-2) }

// panelHeight is the link panel under the page, dropped on short terminals.
func (m *WikiModel) panelHeight() int {
	if m.bodyHeight() < 12 {
		return 0
	}
	return 6
}

func (m *WikiModel) readerWidth() int {
	w := m.width - m.treeWidth()
	if m.treeWidth() > 0 && w > 0 {
		w-- // the separator
	}
	return max(0, w)
}

func (m *WikiModel) readerHeight() int { return max(1, m.bodyHeight()-m.panelHeight()) }

// doc renders the open page for the reader's width and selection, reusing the
// last rendering when neither changed.
func (m *WikiModel) doc() *render.Doc {
	r := m.cur
	width := max(10, m.readerWidth()-1)
	if r.doc == nil || r.docWidth != width || r.docSelected != r.selected {
		r.doc = render.Render(r.src, render.Options{Width: width, Styles: m.styles, Selected: r.selected, Broken: func(l render.Link) bool {
			c, ok := r.cacheLink(l)
			return ok && c.Status != wiki.StatusOK
		}})
		r.docWidth, r.docSelected = width, r.selected
	}
	return r.doc
}

func (m *WikiModel) scrollReader(by int) {
	if m.cur == nil {
		return
	}
	m.cur.scroll = max(0, min(m.cur.scroll+by, len(m.doc().Lines)-m.readerHeight()))
}

// selectLink moves the selection by step, wrapping, and scrolls it into view.
func (m *WikiModel) selectLink(step int) {
	if m.cur == nil {
		return
	}
	links := m.doc().Links
	if len(links) == 0 {
		m.setStatus("no links on this page")
		return
	}
	r := m.cur
	if r.selected < 0 {
		// Start from the first link on screen.
		r.selected = len(links) - 1
		if step < 0 {
			r.selected = 0
		}
		for i, l := range links {
			if l.Line >= r.scroll {
				r.selected = (i - step + len(links)) % len(links)
				break
			}
		}
	}
	r.selected = (r.selected + step + len(links)) % len(links)
	m.showLine(links[r.selected].Line)
}

// showLine scrolls the reader so an output line is visible.
func (m *WikiModel) showLine(line int) {
	h := m.readerHeight()
	if line < m.cur.scroll || line >= m.cur.scroll+h {
		m.cur.scroll = max(0, line-h/3)
	}
}

// ---------------------------------------------------------------- update

// Update handles one message.
func (m *WikiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tea.KeyMsg:
		m.key(msg)
		cmd := m.after
		m.after = nil
		if m.quitting {
			return m, tea.Quit
		}
		return m, cmd
	case pollMsg:
		m.pollDisk()
		return m, poll()
	case wikiEditedMsg:
		m.edited(msg)
		return m, nil
	case fileEditedMsg:
		if msg.err != nil {
			m.setError(fmt.Errorf("the editor exited with an error: %w", msg.err))
		}
		return m, nil
	}
	return m, nil
}

// pollDisk picks up pages changed outside the interface, and saves the
// editor's draft.
func (m *WikiModel) pollDisk() {
	m.writeDraft()
	ch, err := m.w.Refresh()
	if err != nil {
		m.setError(err)
		return
	}
	if ch.Empty() {
		return
	}
	if err := m.loadPages(); err != nil {
		m.setError(err)
		return
	}
	if e := m.edit; e != nil {
		// A page changed under the editor: reload a clean buffer, and warn
		// about one with unsaved changes.
		if _, hash, err := m.w.Read(e.page); err == nil && hash != e.base {
			if e.ed.Dirty {
				e.outside = true
			} else {
				m.editReload()
			}
		}
	}
	if m.cur != nil {
		page := m.cur.info.Path
		for _, r := range ch.Renamed {
			if r[0] == page {
				page = r[1]
			}
		}
		if page != m.cur.info.Path {
			m.cur.info.Path = page
			m.setStatus("renamed to " + page)
		}
		if _, _, err := m.w.Read(page); err != nil {
			m.setStatus(page + " was removed")
			m.cur = nil
		} else {
			m.reload()
		}
	}
	switch m.screen {
	case screenBroken:
		m.loadBroken()
	case screenTasks:
		m.loadTasks()
	case screenHome:
		m.loadHome()
	}
}

func (m *WikiModel) key(msg tea.KeyMsg) {
	if msg.Type == tea.KeyCtrlC {
		m.quitting = true
		return
	}
	if m.prompt != nil {
		m.keyPrompt(msg)
		return
	}
	switch m.screen {
	case screenSearch, screenOpen:
		m.keyFind(msg)
		return
	case screenEdit:
		m.keyEdit(msg)
		return
	case screenHelp:
		m.screen = m.base
		return
	}

	k := msg.String()
	m.status = ""
	switch k {
	case "q":
		m.quitting = true
		return
	case "?":
		m.screen = screenHelp
		return
	case "/":
		m.screen, m.cursor, m.scroll, m.hits = screenSearch, 0, 0, nil
		m.input.clear()
		return
	case "ctrl+p":
		m.screen, m.cursor, m.scroll = screenOpen, 0, 0
		m.input.clear()
		m.findPages()
		return
	case "c":
		if m.screen != screenOffers {
			m.screen, m.cursor, m.scroll = screenBroken, 0, 0
			m.loadBroken()
			return
		}
	case "t":
		if m.screen != screenOffers {
			m.screen, m.cursor, m.scroll = screenTasks, 0, 0
			m.loadTasks()
			return
		}
	case "n":
		m.promptNew()
		return
	case "O":
		m.screen, m.base = screenHome, screenHome
		m.loadHome()
		return
	case "esc":
		if m.screen == screenOffers {
			m.screen = m.offerFrom
			return
		}
		if m.screen != m.base {
			m.screen = m.base
			return
		}
	}

	switch m.screen {
	case screenHome:
		m.keyHome(k)
	case screenBroken, screenTasks, screenOffers, screenPages:
		m.keyList(msg)
	default:
		switch m.focus {
		case focusTree:
			m.keyTree(k)
		case focusPanel:
			m.keyPanel(k)
		default:
			m.keyReader(k)
		}
	}
}

func (m *WikiModel) keyTree(k string) {
	switch k {
	case "j", "down":
		m.treeCursor = min(m.treeCursor+1, len(m.rows)-1)
	case "k", "up":
		m.treeCursor = max(m.treeCursor-1, 0)
	case "g", "home":
		m.treeCursor = 0
	case "G", "end":
		m.treeCursor = max(0, len(m.rows)-1)
	case "enter", " ", "o":
		if m.treeCursor >= len(m.rows) {
			return
		}
		row := m.rows[m.treeCursor]
		if row.dir != "" {
			m.collapsed[row.dir] = !m.collapsed[row.dir]
			m.buildTree()
			return
		}
		if err := m.open(m.pages[row.page].Path); err != nil {
			m.setError(err)
			return
		}
		m.focus = focusReader
	case "l", "right", "tab":
		if m.cur != nil {
			m.focus = focusReader
		}
	}
}

func (m *WikiModel) keyReader(k string) {
	if m.cur == nil {
		m.focus = focusTree
		return
	}
	half := max(1, m.readerHeight()/2)
	switch k {
	case "j", "down":
		m.scrollReader(1)
	case "k", "up":
		m.scrollReader(-1)
	case "ctrl+d", "pgdown", " ":
		m.scrollReader(half)
	case "ctrl+u", "pgup":
		m.scrollReader(-half)
	case "g", "home":
		m.cur.scroll = 0
	case "G", "end":
		m.scrollReader(len(m.doc().Lines))
	case "tab":
		m.selectLink(1)
	case "shift+tab":
		m.selectLink(-1)
	case "enter":
		m.follow()
	case "backspace":
		m.back()
	case "h", "left":
		m.focus = focusTree
	case "b":
		if len(m.cur.backlinks) > 0 {
			m.focus = focusPanel
		} else {
			m.setStatus("no page links here")
		}
	case "e":
		line := 0
		if r := m.cur; r != nil && r.scroll < len(m.doc().Source) {
			line = m.doc().Source[r.scroll]
		}
		m.openEditor(m.cur.info.Path, line)
	case "E":
		m.editExternal()
	case "r":
		m.promptMove()
	case "f":
		if l, ok := m.selectedLink(); ok {
			m.showOffers(l, screenRead)
		} else {
			m.setStatus("select a broken link with tab first")
		}
	}
}

func (m *WikiModel) keyPanel(k string) {
	r := m.cur
	if r == nil {
		m.focus = focusTree
		return
	}
	switch k {
	case "j", "down":
		r.backCursor = min(r.backCursor+1, len(r.backlinks)-1)
	case "k", "up":
		r.backCursor = max(r.backCursor-1, 0)
	case "esc", "b", "h", "left":
		m.focus = focusReader
	case "enter":
		if r.backCursor >= len(r.backlinks) {
			return
		}
		l := r.backlinks[r.backCursor]
		m.openAt(l.Page, l.Line)
	}
}

// openAt opens a page scrolled to a source line.
func (m *WikiModel) openAt(page string, line int) {
	if err := m.open(page); err != nil {
		m.setError(err)
		return
	}
	m.focus = focusReader
	for i, s := range m.doc().Source {
		if s >= line {
			m.showLine(i)
			return
		}
	}
}

// selectedLink is the indexed form of the selected link.
func (m *WikiModel) selectedLink() (wiki.Link, bool) {
	r := m.cur
	if r == nil || r.selected < 0 || r.selected >= len(m.doc().Links) {
		return wiki.Link{}, false
	}
	return r.cacheLink(m.doc().Links[r.selected])
}

var lineAnchor = regexp.MustCompile(`^L(\d+)`)

// follow opens what the selected link points at.
func (m *WikiModel) follow() {
	l, ok := m.selectedLink()
	if !ok {
		if m.cur.selected < 0 {
			m.setStatus("tab selects a link")
		} else {
			m.setStatus("the link is not indexed yet")
		}
		return
	}
	switch {
	case l.Kind == wiki.KindExternal:
		m.setStatus("external link, not opened: " + l.Resolved)

	case (l.Kind == wiki.KindPage || l.Kind == wiki.KindHeading) && (l.Status == wiki.StatusOK || l.Status == wiki.StatusMissingHeading):
		if l.Resolved != m.cur.info.Path {
			if err := m.open(l.Resolved); err != nil {
				m.setError(err)
				return
			}
		} else {
			m.history = append(m.history, place{m.cur.info.Path, m.cur.scroll, m.cur.selected})
		}
		m.cur.selected = -1
		m.cur.scroll = 0
		if l.Anchor != "" {
			if line, ok := m.doc().Headings[markdown.Slug(l.Anchor)]; ok {
				m.cur.scroll = line
			} else {
				m.setStatus("no heading #" + l.Anchor)
			}
		}

	case (l.Kind == wiki.KindFile || l.Kind == wiki.KindLine) && (l.Status == wiki.StatusOK || l.Status == wiki.StatusLineOutOfRange):
		line := 0
		if mm := lineAnchor.FindStringSubmatch(l.Anchor); mm != nil {
			line, _ = strconv.Atoi(mm[1])
		}
		cmd, err := editor.Open(filepath.Join(m.w.Repo, filepath.FromSlash(l.Resolved)), line, m.getenv)
		if err != nil {
			m.setError(err)
			return
		}
		m.after = m.exec(cmd, func(err error) tea.Msg { return fileEditedMsg{err} })

	default:
		m.setError(fmt.Errorf("%s: %s; f lists repairs", l.Status, l.Written()))
	}
}

// back returns to the previous place.
func (m *WikiModel) back() {
	if len(m.history) == 0 {
		m.setStatus("nothing to go back to")
		return
	}
	p := m.history[len(m.history)-1]
	m.history = m.history[:len(m.history)-1]
	if err := m.load(p.page); err != nil {
		m.setError(err)
		return
	}
	m.cur.scroll, m.cur.selected = p.scroll, p.selected
}

// ---------------------------------------------------------------- finding

func (m *WikiModel) keyFind(msg tea.KeyMsg) {
	switch msg.String() {
	case "esc":
		m.screen = m.base
		return
	case "enter":
		var page string
		switch {
		case m.screen == screenSearch && m.cursor < len(m.hits):
			page = m.hits[m.cursor].Path
		case m.screen == screenOpen && m.cursor < len(m.matches):
			page = m.matches[m.cursor].Path
		default:
			return
		}
		if err := m.open(page); err != nil {
			m.setError(err)
			return
		}
		m.screen, m.focus = screenRead, focusReader
		return
	case "down", "ctrl+n":
		m.cursor = min(m.cursor+1, max(0, m.listLen()-1))
		return
	case "up", "ctrl+p":
		m.cursor = max(m.cursor-1, 0)
		return
	}
	if !editLine(&m.input, msg) {
		return
	}
	m.cursor, m.scroll = 0, 0
	if m.screen == screenSearch {
		m.search()
	} else {
		m.findPages()
	}
}

// editLine applies a key to a line of input and reports whether the text
// changed.
func editLine(in *input, msg tea.KeyMsg) bool {
	before := in.String()
	switch msg.Type {
	case tea.KeyRunes:
		in.insertString(string(msg.Runes))
	case tea.KeySpace:
		in.insert(' ')
	case tea.KeyBackspace:
		in.backspace()
	case tea.KeyDelete:
		in.deleteForward()
	case tea.KeyCtrlW:
		in.deleteWord()
	case tea.KeyCtrlU:
		in.deleteToStart()
	case tea.KeyLeft:
		in.left()
	case tea.KeyRight:
		in.right()
	case tea.KeyHome, tea.KeyCtrlA:
		in.home()
	case tea.KeyEnd, tea.KeyCtrlE:
		in.end()
	}
	return in.String() != before
}

func (m *WikiModel) search() {
	q := strings.TrimSpace(m.input.String())
	if q == "" {
		m.hits = nil
		return
	}
	hits, err := m.w.Search(q, 100)
	if err != nil {
		m.setError(err)
		return
	}
	m.hits, m.status = hits, ""
}

// findPages matches the open line against titles and paths: a title prefix
// first, then a title fragment, a path fragment, and the letters in order.
func (m *WikiModel) findPages() {
	q := strings.ToLower(strings.TrimSpace(m.input.String()))
	type scored struct {
		p     wiki.PageInfo
		score int
	}
	var found []scored
	for _, p := range m.pages {
		title, id := strings.ToLower(p.Title), strings.ToLower(p.Path)
		score := -1
		switch {
		case q == "" || strings.HasPrefix(title, q):
			score = 0
		case strings.Contains(title, q):
			score = 1
		case strings.Contains(id, q):
			score = 2
		case subsequence(id+" "+title, q):
			score = 3
		}
		if score >= 0 {
			found = append(found, scored{p, score})
		}
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].score < found[j].score })
	m.matches = m.matches[:0]
	for _, f := range found {
		m.matches = append(m.matches, f.p)
	}
}

func subsequence(s, sub string) bool {
	for _, r := range sub {
		i := strings.IndexRune(s, r)
		if i < 0 {
			return false
		}
		s = s[i+len(string(r)):]
	}
	return true
}

// ---------------------------------------------------------------- lists

func (m *WikiModel) listLen() int {
	switch m.screen {
	case screenSearch:
		return len(m.hits)
	case screenOpen:
		return len(m.matches)
	case screenBroken:
		return len(m.broken)
	case screenTasks:
		return len(m.tasks)
	case screenOffers:
		return len(m.offers)
	case screenPages:
		return len(m.listed)
	}
	return 0
}

func (m *WikiModel) loadBroken() {
	broken, err := m.w.Check()
	if err != nil {
		m.setError(err)
		return
	}
	m.broken = broken
	m.cursor = min(m.cursor, max(0, len(broken)-1))
}

func (m *WikiModel) loadTasks() {
	f := wiki.TaskFilter{Status: "open"}
	if m.allTasks {
		f.Status = ""
	}
	tasks, err := m.w.Tasks(f)
	if err != nil {
		m.setError(err)
		return
	}
	m.tasks = tasks
	m.cursor = min(m.cursor, max(0, len(tasks)-1))
}

func (m *WikiModel) keyList(msg tea.KeyMsg) {
	switch k := msg.String(); k {
	case "j", "down":
		m.cursor = min(m.cursor+1, max(0, m.listLen()-1))
	case "k", "up":
		m.cursor = max(m.cursor-1, 0)
	case "g", "home":
		m.cursor = 0
	case "G", "end":
		m.cursor = max(0, m.listLen()-1)
	case "enter":
		m.listEnter()
	case "f":
		if m.screen == screenBroken && m.cursor < len(m.broken) {
			m.showOffers(m.broken[m.cursor], screenBroken)
		}
	case " ", "x":
		if m.screen == screenTasks && m.cursor < len(m.tasks) {
			m.toggleTask(m.tasks[m.cursor])
		}
	case "a":
		if m.screen == screenTasks {
			m.allTasks = !m.allTasks
			m.loadTasks()
		}
	}
}

func (m *WikiModel) listEnter() {
	switch m.screen {
	case screenBroken:
		if m.cursor >= len(m.broken) {
			return
		}
		l := m.broken[m.cursor]
		m.screen = screenRead
		m.openAt(l.Page, l.Line)
		for i, dl := range m.doc().Links {
			if c, ok := m.cur.cacheLink(dl); ok && c.Line == l.Line && c.Written() == l.Written() {
				m.cur.selected = i
				m.showLine(dl.Line)
				break
			}
		}
	case screenTasks:
		if m.cursor >= len(m.tasks) {
			return
		}
		t := m.tasks[m.cursor]
		m.screen = screenRead
		m.openAt(t.Page, t.Line)
	case screenPages:
		if m.cursor < len(m.listed) {
			if err := m.open(m.listed[m.cursor].Path); err != nil {
				m.setError(err)
				return
			}
			m.focus = focusReader
		}
	case screenOffers:
		if m.cursor >= len(m.offers) {
			return
		}
		if err := m.w.Fix(m.offerFor, m.offers[m.cursor]); err != nil {
			m.setError(err)
			return
		}
		m.setStatus("fixed: " + m.offers[m.cursor].New)
		m.screen, m.cursor = m.offerFrom, 0
		m.afterWrite()
	}
}

// toggleTask ticks or clears an item, and moves a task page through open,
// doing and done.
func (m *WikiModel) toggleTask(t wiki.Task) {
	next := map[string]string{"open": "done", "done": "open"}[t.Status]
	if t.Line == 0 {
		next = map[string]string{"open": "doing", "doing": "done", "done": "open"}[t.Status]
	}
	if err := m.w.SetTaskStatus(t, next); err != nil {
		m.setError(err)
		return
	}
	m.setStatus(t.Text + ": " + next)
	m.afterWrite()
}

func (m *WikiModel) showOffers(l wiki.Link, from wscreen) {
	if l.Status == wiki.StatusOK {
		m.setStatus("the link is not broken")
		return
	}
	offers, err := m.w.Offers(l)
	if err != nil {
		m.setError(err)
		return
	}
	if len(offers) == 0 {
		m.setStatus("no repairs to offer for " + l.Written() + "; edit the page with e")
		return
	}
	m.offers, m.offerFor, m.offerFrom = offers, l, from
	m.screen, m.cursor, m.scroll = screenOffers, 0, 0
}

// afterWrite brings the interface up to date with a write it made.
func (m *WikiModel) afterWrite() {
	if err := m.loadPages(); err != nil {
		m.setError(err)
	}
	m.reload()
	switch m.screen {
	case screenBroken:
		m.loadBroken()
	case screenTasks:
		m.loadTasks()
	}
	if m.base == screenHome {
		m.loadHome()
	}
}

// ---------------------------------------------------------------- writes

// wikiPrompt is a question awaiting a typed answer. An error from action shows
// on the status line.
type wikiPrompt struct {
	label  string
	action func(answer string) error
}

func (m *WikiModel) askWiki(label, initial string, action func(answer string) error) {
	m.prompt = &wikiPrompt{label: label, action: action}
	m.input.set(initial)
}

func (m *WikiModel) keyPrompt(msg tea.KeyMsg) {
	switch msg.Type {
	case tea.KeyEsc:
		m.prompt = nil
		m.setStatus("cancelled")
	case tea.KeyEnter:
		p := m.prompt
		m.prompt = nil
		if err := p.action(m.input.String()); err != nil {
			m.setError(err)
		}
	default:
		editLine(&m.input, msg)
	}
}

// promptNew creates a page in the directory of the page open or selected.
func (m *WikiModel) promptNew() {
	dir := ""
	if m.cur != nil {
		dir = path.Dir(m.cur.info.Path)
	}
	if m.screen == screenRead && m.focus == focusTree && m.treeCursor < len(m.rows) {
		if row := m.rows[m.treeCursor]; row.dir != "" {
			dir = row.dir
		} else {
			dir = path.Dir(m.pages[row.page].Path)
		}
	}
	if dir == "." {
		dir = ""
	}
	label := "new page title: "
	if dir != "" {
		label = "new page in " + dir + "/, title: "
	}
	m.askWiki(label, "", func(title string) error {
		if strings.TrimSpace(title) == "" {
			return errors.New("a page needs a title")
		}
		info, err := m.w.Create(wiki.NewPage{Title: title, Dir: dir})
		if err != nil {
			return err
		}
		if err := m.loadPages(); err != nil {
			return err
		}
		if err := m.open(info.Path); err != nil {
			return err
		}
		m.screen, m.focus = screenRead, focusReader
		m.setStatus("created " + info.File() + "; e edits it")
		return nil
	})
}

// promptMove asks where to move the open page, shows what the move would
// rewrite, and asks again before writing.
func (m *WikiModel) promptMove() {
	from := m.cur.info.Path
	m.askWiki("move "+from+" to: ", from, func(to string) error {
		to = strings.TrimSpace(to)
		if strings.HasSuffix(to, "/") {
			to += path.Base(from)
		}
		plan, err := m.w.PlanMove(from, to)
		if err != nil {
			return err
		}
		pages := map[string]bool{}
		for _, e := range plan.Edits {
			pages[e.Page] = true
		}
		question := fmt.Sprintf("move to %s, rewriting %d links in %d pages? y/n: ", plan.To, len(plan.Edits), len(pages))
		m.askWiki(question, "", func(answer string) error {
			if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(answer)), "y") {
				m.setStatus("not moved")
				return nil
			}
			res, err := m.w.Move(from, to)
			if err != nil {
				return err
			}
			// The old path is gone from the back stack.
			for i := range m.history {
				if m.history[i].page == from {
					m.history[i].page = res.To
				}
			}
			m.cur.info.Path = res.To
			m.afterWrite()
			status := fmt.Sprintf("moved to %s; %d links rewritten", res.To, len(res.Edits))
			if len(res.Broken) > 0 {
				status += fmt.Sprintf("; %d links broken, c lists them", len(res.Broken))
			}
			m.setStatus(status)
			return nil
		})
		return nil
	})
}

type wikiEditedMsg struct {
	page, hash string
	src        []byte
	edit       *editor.Edit
	err        error
}

type fileEditedMsg struct{ err error }

// edit hands the page source to $EDITOR. The gwiki editor replaces this in
// phase 5.
// editExternal hands the page to $EDITOR, for a page the built-in editor is
// not wanted for.
func (m *WikiModel) editExternal() {
	r := m.cur
	e, err := editor.Start(string(r.src), m.getenv)
	if err != nil {
		m.setError(err)
		return
	}
	page, hash, src := r.info.Path, r.hash, r.src
	m.after = m.exec(e.Cmd, func(err error) tea.Msg {
		return wikiEditedMsg{page: page, hash: hash, src: src, edit: e, err: err}
	})
}

func (m *WikiModel) edited(msg wikiEditedMsg) {
	if msg.err != nil {
		msg.edit.Cleanup()
		m.setError(fmt.Errorf("the editor exited with an error, so nothing was saved: %w", msg.err))
		return
	}
	text, err := msg.edit.Finish()
	if err != nil {
		m.setError(err)
		return
	}
	if text == string(msg.src) {
		m.setStatus("no change")
		return
	}
	out := []byte(strings.TrimRight(text, "\n") + "\n")
	var conflict *wiki.ErrConflict
	switch err := m.w.Write(msg.page, out, msg.hash); {
	case errors.As(err, &conflict):
		// Keep the edit rather than lose it with the temporary file.
		kept, kerr := os.CreateTemp("", "gwiki-edit-*.md")
		if kerr == nil {
			_, kerr = kept.Write(out)
			kept.Close()
		}
		if kerr != nil {
			m.setError(fmt.Errorf("%w; your text could not be kept: %v", err, kerr))
		} else {
			m.setError(fmt.Errorf("%w; your text is in %s", err, kept.Name()))
		}
	case err != nil:
		m.setError(err)
	default:
		m.setStatus("saved " + msg.page)
	}
	m.afterWrite()
}
