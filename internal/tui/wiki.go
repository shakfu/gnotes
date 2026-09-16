// Package tui is the wiki's terminal interface: an overview, a page tree
// beside the open page in a vim buffer, a backlinks panel, search, quick open,
// broken links and tasks; see docs/dev/wiki-design.md, section 12, and
// docs/dev/modal-reader.md.
package tui

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/shakfu/gwiki/internal/editor"
	"github.com/shakfu/gwiki/internal/markdown"
	"github.com/shakfu/gwiki/internal/vim"
	"github.com/shakfu/gwiki/internal/wiki"
)

// wscreen is what the body of the wiki interface shows.
type wscreen int

const (
	screenLatest wscreen = iota // the overview's tab of recent changes
	screenRead                  // tree and page
	screenSearch                // '/' results as you type
	screenOpen                  // ctrl-p quick open
	screenBroken                // 'c' broken links across the wiki
	screenTasks                 // 't' tasks
	screenOffers                // 'f' repairs for one link
	screenPages                 // a list of pages, such as a tag's
	screenStats                 // the overview's tab of health and structure
	screenHelp
)

// overviewTabs are the overview's screens, in the order the header shows them
// and tab moves through them.
var overviewTabs = []wscreen{screenLatest, screenTasks, screenStats}

func isTab(s wscreen) bool { return slices.Contains(overviewTabs, s) }

// wfocus is the focused part of the read screen.
type wfocus int

const (
	focusTree    wfocus = iota
	focusContent        // the page's buffer
	focusPanel          // the backlinks under the page
)

// WikiModel is the wiki interface state.
type WikiModel struct {
	w *wiki.Wiki

	width, height int

	screen wscreen
	focus  wfocus

	// base is the screen that lists and prompts return to: an overview tab or
	// the page, whichever was used last. tab is the overview tab last shown.
	base wscreen
	tab  wscreen

	// edit is the open page's buffer, set whenever cur is.
	edit      *editing
	home      *overview
	homeCol   int
	homeRow   [2]int
	listTitle string
	listed    []wiki.PageInfo

	// helpFrom is the screen the help returns to, and helpScroll its first
	// visible line.
	helpFrom   wscreen
	helpScroll int

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

	// exec runs a program with the terminal, and copy puts text in the
	// system clipboard; tests replace both.
	exec func(*exec.Cmd, tea.ExecCallback) tea.Cmd
	copy func(text string)

	// after is a command queued by a key for Update to return.
	after tea.Cmd
}

// reading is what the index knows about the open page.
type reading struct {
	info      wiki.PageInfo
	links     int
	broken    int
	backlinks []wiki.Link
	linkers   []linker

	backCursor int
}

// linker is a page linking to the open one: the line of its first link, and
// how many links it has.
type linker struct {
	page        string
	line, links int
}

// place is an entry in the back stack.
type place struct {
	page   string
	cursor vim.Pos
	top    int
}

// treeRow is a directory or a page in the tree. A directory with a README has
// that page on its own row, and the README gets none of its own.
type treeRow struct {
	dir     string // a directory's path, empty for a page
	page    int    // index into pages: the page, or the directory's README
	hasPage bool   // a directory has a README
	depth   int
}

// NewWiki builds the interface over an open wiki.
func NewWiki(w *wiki.Wiki) (*WikiModel, error) {
	m := &WikiModel{
		w:         w,
		width:     80,
		height:    24,
		collapsed: map[string]bool{},
		getenv:    os.Getenv,
		now:       time.Now,
		exec:      tea.ExecProcess,
		// OSC 52: the terminal sets its clipboard, over SSH too.
		copy: func(text string) { term.Output().Copy(text) },
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
	// Ask the terminal for its background now: once the program runs, the
	// reply would arrive as keystrokes.
	term.HasDarkBackground()
	if _, err := tea.NewProgram(m, tea.WithAltScreen()).Run(); err != nil {
		return fmt.Errorf("interface: %w", err)
	}
	return nil
}

// pollInterval is how often pages are checked for outside changes.
const pollInterval = time.Second

// pollMsg asks the model to check for outside changes.
type pollMsg struct{}

func poll() tea.Cmd {
	return tea.Tick(pollInterval, func(time.Time) tea.Msg { return pollMsg{} })
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
	readmes := map[string]int{}
	for i, p := range m.pages {
		if dir := wiki.ReadmeDir(p.Path); dir != "" {
			readmes[dir] = i
		}
	}
	m.rows = m.rows[:0]
	shown := map[string]bool{}
	// The home page first, then everything by path.
	order := make([]int, 0, len(m.pages))
	for i, p := range m.pages {
		if p.Path == "index" {
			order = append([]int{i}, order...)
		} else {
			order = append(order, i)
		}
	}
	for _, i := range order {
		p := m.pages[i]
		parts := strings.Split(p.Path, "/")
		hidden := false
		for d := 1; d < len(parts) && !hidden; d++ {
			dir := strings.Join(parts[:d], "/")
			if !shown[dir] {
				shown[dir] = true
				readme, has := readmes[dir]
				m.rows = append(m.rows, treeRow{dir: dir, page: readme, hasPage: has, depth: d - 1})
			}
			hidden = m.collapsed[dir]
		}
		if !hidden && wiki.ReadmeDir(p.Path) == "" {
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

// errUnsaved refuses to leave a page with unsaved changes, as vim does.
var errUnsaved = errors.New("the page has unsaved changes; :w writes them, :e! discards them")

// open shows a page, remembering the one it replaces. The open page's buffer
// is kept when it is the page asked for.
func (m *WikiModel) open(page string) error {
	if m.cur != nil && m.cur.info.Path != page {
		if m.edit != nil && m.edit.ed.Dirty {
			return errUnsaved
		}
		if m.base == screenRead {
			m.history = append(m.history, m.here())
		}
	}
	if m.cur == nil || m.cur.info.Path != page {
		if err := m.load(page); err != nil {
			return err
		}
	}
	m.screen, m.base = screenRead, screenRead
	return nil
}

// here is the open page and position, for the back stack.
func (m *WikiModel) here() place {
	return place{page: m.cur.info.Path, cursor: m.edit.ed.Cursor, top: m.edit.ed.Top}
}

// load reads a page into the buffer, without touching the back stack.
func (m *WikiModel) load(page string) error {
	src, hash, err := m.w.Read(page)
	if err != nil {
		return err
	}
	r, err := m.pageInfo(page)
	if err != nil {
		return err
	}
	m.cur = r
	m.openBuffer(page, src, hash)
	for i, row := range m.rows {
		if (row.dir == "" || row.hasPage) && m.pages[row.page].Path == page {
			m.treeCursor = i
		}
	}
	return nil
}

// pageInfo reads the index's links and backlinks for a page.
func (m *WikiModel) pageInfo(page string) (*reading, error) {
	full, err := m.w.Page(page)
	if err != nil {
		return nil, err
	}
	r := &reading{info: full.PageInfo, links: len(full.Links), backlinks: full.Backlinks}
	for _, l := range full.Links {
		if l.Status != wiki.StatusOK {
			r.broken++
		}
	}
	at := map[string]int{}
	for _, l := range full.Backlinks {
		i, seen := at[l.Page]
		if !seen {
			i = len(r.linkers)
			at[l.Page] = i
			r.linkers = append(r.linkers, linker{page: l.Page, line: l.Line})
		}
		r.linkers[i].links++
	}
	return r, nil
}

// reload brings the open page up to date after a write or an outside change:
// the index's view of it, and the buffer when it has no unsaved changes. A
// modified buffer is marked instead, for :w! or :e!.
func (m *WikiModel) reload() {
	if m.cur == nil {
		return
	}
	page := m.cur.info.Path
	r, err := m.pageInfo(page)
	if err != nil {
		m.setError(fmt.Errorf("%s: %w", page, err))
		return
	}
	r.backCursor = min(m.cur.backCursor, max(0, len(r.linkers)-1))
	m.cur = r
	e := m.edit
	if src, hash, err := m.w.Read(page); err == nil && hash != e.base {
		if e.ed.Dirty {
			e.outside = true
		} else {
			e.ed.Load(string(src))
			e.base, e.outside = hash, false
		}
	}
	m.checkLinks()
}

// ---------------------------------------------------------------- layout

const wikiTreeWidth = 28

// treeWidth is the tree's column, zero when the terminal is too narrow to
// show it beside the page.
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

// hasPanel reports whether the page has a backlinks panel to show: something
// links to it, and the terminal is tall enough.
func (m *WikiModel) hasPanel() bool {
	return m.cur != nil && len(m.cur.linkers) > 0 && m.bodyHeight() >= 12
}

// panelHeight is the backlinks panel under the page: a title row and a row
// per linking page, at most a third of the body. It is drawn only while it has
// the focus, so the page keeps the height otherwise; the header bar counts the
// backlinks.
func (m *WikiModel) panelHeight() int {
	if !m.hasPanel() || m.focus != focusPanel {
		return 0
	}
	return min(len(m.cur.linkers)+1, m.bodyHeight()/3)
}

// contentWidth is the page pane, beside the tree and its separator.
func (m *WikiModel) contentWidth() int {
	w := m.width - m.treeWidth()
	if m.treeWidth() > 0 && w > 0 {
		w-- // the separator
	}
	return max(0, w)
}

// contentHeight is the rows the buffer is drawn in, under the front matter
// line and above the backlinks panel.
func (m *WikiModel) contentHeight() int {
	return max(1, m.bodyHeight()-m.panelHeight()-m.metaHeight())
}

// metaHeight is the front matter line above the preview, which does not draw
// the front matter; the buffer shows it as text.
func (m *WikiModel) metaHeight() int {
	if m.cur == nil || m.edit == nil || !m.edit.preview || len(m.metaLine()) == 0 {
		return 0
	}
	return 1
}

// jumpTo puts the cursor at the start of a 1-based source line, a third of
// the way down the pane.
func (m *WikiModel) jumpTo(line int) {
	ed := m.edit.ed
	ed.SetCursor(vim.Pos{Line: max(0, line-1)})
	ed.Top = max(0, ed.Cursor.Line-m.contentHeight()/3)
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
	if m.cur != nil {
		m.edit.links = nil
		page := m.cur.info.Path
		for _, r := range ch.Renamed {
			if r[0] == page {
				page = r[1]
			}
		}
		if page != m.cur.info.Path {
			m.dropDraft()
			m.cur.info.Path, m.edit.page = page, page
			m.setStatus("renamed to " + page)
		}
		switch _, _, err := m.w.Read(page); {
		case err == nil:
			m.reload()
		case m.edit.ed.Dirty:
			m.edit.outside = true
			m.setError(errors.New(page + " was removed; :w writes it again"))
		default:
			m.setStatus(page + " was removed")
			m.cur, m.edit = nil, nil
			m.focus = focusTree
		}
	}
	switch m.screen {
	case screenBroken:
		m.loadBroken()
	case screenTasks:
		m.loadTasks()
	case screenLatest, screenStats:
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
	switch {
	case m.screen == screenSearch, m.screen == screenOpen:
		m.keyFind(msg)
	case m.screen == screenRead && m.focus == focusContent && m.edit != nil:
		m.keyContent(msg)
	case m.screen == screenHelp:
		m.keyHelp(msg)
	default:
		m.status = ""
		m.keyNormal(msg.String())
	}
}

// ---------------------------------------------------------------- actions

func (m *WikiModel) startSearch() {
	m.screen, m.cursor, m.scroll, m.hits = screenSearch, 0, 0, nil
	m.input.clear()
}

func (m *WikiModel) startOpen() {
	m.screen, m.cursor, m.scroll = screenOpen, 0, 0
	m.input.clear()
	m.findPages()
}

// showList opens the broken links, or the tasks tab.
func (m *WikiModel) showList(screen wscreen) {
	if screen == screenTasks {
		m.showTab(screenTasks)
		return
	}
	m.screen, m.cursor, m.scroll = screen, 0, 0
	m.loadBroken()
}

// showHome opens the overview on the tab last shown.
func (m *WikiModel) showHome() { m.showTab(m.tab) }

// showTab opens an overview tab, loading what it shows.
func (m *WikiModel) showTab(tab wscreen) {
	if m.screen != tab {
		m.cursor, m.scroll = 0, 0
	}
	m.screen, m.base, m.tab = tab, tab, tab
	if tab == screenTasks {
		m.loadTasks()
	} else {
		m.loadHome()
	}
}

// cycleTab moves to the next overview tab, or the previous one.
func (m *WikiModel) cycleTab(by int) {
	i := slices.Index(overviewTabs, m.screen)
	m.showTab(overviewTabs[(i+by+len(overviewTabs))%len(overviewTabs)])
}

// reloadAll reads pages and git details again, for a commit, which changes no
// page and so is not noticed by polling.
func (m *WikiModel) reloadAll() {
	m.afterWrite()
	if m.screen == screenLatest || m.screen == screenStats {
		m.loadHome()
	}
	if !m.statusErr {
		m.setStatus("reloaded")
	}
}

// escape returns from a list to the overview or the reader.
func (m *WikiModel) escape() {
	if m.screen != m.base {
		m.screen = m.base
	}
}

func (m *WikiModel) treeMove(s func(pos, last int) int) {
	m.treeCursor = step(m.treeCursor, len(m.rows)-1, s)
}

// treeEnter opens a page, or a directory's README; a directory without one
// folds.
func (m *WikiModel) treeEnter() {
	if m.treeCursor >= len(m.rows) {
		return
	}
	row := m.rows[m.treeCursor]
	if row.dir != "" && !row.hasPage {
		m.treeFold()
		return
	}
	if err := m.open(m.pages[row.page].Path); err != nil {
		m.setError(err)
		return
	}
	m.focus = focusContent
}

// treeFold folds or unfolds a directory, and opens a page.
func (m *WikiModel) treeFold() {
	if m.treeCursor >= len(m.rows) {
		return
	}
	if row := m.rows[m.treeCursor]; row.dir != "" {
		m.collapsed[row.dir] = !m.collapsed[row.dir]
		m.buildTree()
		return
	}
	m.treeEnter()
}

// treeRight unfolds a folded directory, steps into an unfolded one, and opens
// a page.
func (m *WikiModel) treeRight() {
	if m.treeCursor >= len(m.rows) {
		return
	}
	row := m.rows[m.treeCursor]
	switch {
	case row.dir == "":
		m.treeEnter()
	case m.collapsed[row.dir]:
		m.treeFold()
	case m.treeCursor+1 < len(m.rows) && m.rows[m.treeCursor+1].depth > row.depth:
		m.treeCursor++
	}
}

// treeLeft folds an unfolded directory, and otherwise moves to the directory
// holding the row.
func (m *WikiModel) treeLeft() {
	if m.treeCursor >= len(m.rows) {
		return
	}
	row := m.rows[m.treeCursor]
	if row.dir != "" && !m.collapsed[row.dir] {
		m.treeFold()
		return
	}
	for i := m.treeCursor - 1; i >= 0; i-- {
		if m.rows[i].dir != "" && m.rows[i].depth < row.depth {
			m.treeCursor = i
			return
		}
	}
}

func (m *WikiModel) toContent() {
	if m.cur != nil {
		m.focus = focusContent
	}
}

// cycleFocus moves between the panes shown: the tree, the page and the
// backlinks panel.
func (m *WikiModel) cycleFocus(by int) {
	panes := []wfocus{focusTree}
	if m.cur != nil {
		panes = append(panes, focusContent)
	}
	if m.hasPanel() {
		panes = append(panes, focusPanel)
	}
	for i, p := range panes {
		if p == m.focus {
			m.focus = panes[(i+by+len(panes))%len(panes)]
			return
		}
	}
	m.focus = panes[0]
}

func (m *WikiModel) toPanel() error {
	switch {
	case m.hasPanel():
		m.focus = focusPanel
		return nil
	case len(m.cur.linkers) == 0:
		return errors.New("no page links here")
	}
	return errors.New("the terminal is too short for the backlinks")
}

// fixLink lists repairs for the broken link under the cursor.
func (m *WikiModel) fixLink() error {
	l, ok := m.linkAtCursor()
	if !ok {
		return errors.New("no link under the cursor")
	}
	if l.Status == wiki.StatusOK {
		return errors.New("the link is not broken")
	}
	m.showOffers(l, screenRead)
	return nil
}

func (m *WikiModel) panelMove(s func(pos, last int) int) {
	m.cur.backCursor = step(m.cur.backCursor, len(m.cur.linkers)-1, s)
}

func (m *WikiModel) panelEnter() {
	r := m.cur
	if r.backCursor >= len(r.linkers) {
		return
	}
	l := r.linkers[r.backCursor]
	m.openAt(l.page, l.line)
}

// openAt opens a page with the cursor on a source line.
func (m *WikiModel) openAt(page string, line int) {
	if err := m.open(page); err != nil {
		m.setError(err)
		return
	}
	m.focus = focusContent
	m.jumpTo(line)
}

// bufferLinks are the links in the buffer as it is now, resolved against the
// index. They are kept until the text changes or a write may have changed
// what they reach.
func (m *WikiModel) bufferLinks() []wiki.Link {
	e := m.edit
	if e == nil {
		return nil
	}
	text := e.ed.Text()
	if e.linksText == text && e.links != nil {
		return e.links
	}
	snap, err := m.w.Snapshot()
	if err != nil {
		return nil
	}
	e.links = snap.Links(e.page, []byte(text))
	if e.links == nil {
		e.links = []wiki.Link{}
	}
	e.linksText = text
	return e.links
}

// linkSpan is where a link sits in the buffer, as byte offsets.
func linkSpan(l wiki.Link) (start, end int) {
	if l.Start >= 0 {
		return l.Start, l.End
	}
	return l.DestStart, l.DestEnd
}

// cursorOffset is the buffer's cursor as a byte offset into its text.
func (e *editing) cursorOffset() int {
	off := 0
	for i := 0; i < e.ed.Cursor.Line; i++ {
		off += len(e.ed.Buf.LineString(i)) + 1
	}
	line := []rune(e.ed.Buf.LineString(e.ed.Cursor.Line))
	return off + len(string(line[:min(e.ed.Cursor.Col, len(line))]))
}

// posOf is a byte offset into the buffer's text as a line and rune column.
func (e *editing) posOf(off int) vim.Pos {
	for l := 0; l < e.ed.Buf.Lines(); l++ {
		line := e.ed.Buf.LineString(l)
		if off <= len(line) {
			return vim.Pos{Line: l, Col: len([]rune(line[:off]))}
		}
		off -= len(line) + 1
	}
	return vim.Pos{Line: max(0, e.ed.Buf.Lines()-1)}
}

// linkAtCursor is the link under the buffer's cursor, anywhere from its first
// character to its last.
func (m *WikiModel) linkAtCursor() (wiki.Link, bool) {
	if m.edit == nil {
		return wiki.Link{}, false
	}
	off := m.edit.cursorOffset()
	for _, l := range m.bufferLinks() {
		if start, end := linkSpan(l); start >= 0 && off >= start && off < end {
			return l, true
		}
	}
	return wiki.Link{}, false
}

// jumpLink moves the cursor to the start of the next link, or the previous one
// when by is negative, wrapping at either end of the page.
func (m *WikiModel) jumpLink(by int) error {
	e := m.edit
	var starts []int
	for _, l := range m.bufferLinks() {
		if start, _ := linkSpan(l); start >= 0 {
			starts = append(starts, start)
		}
	}
	if len(starts) == 0 {
		return errors.New("no links on this page")
	}
	sort.Ints(starts)
	// From inside a link, the next one is after its start.
	off := e.cursorOffset()
	if l, ok := m.linkAtCursor(); ok {
		off, _ = linkSpan(l)
	}
	target, wrapped := -1, false
	if by > 0 {
		for _, s := range starts {
			if s > off {
				target = s
				break
			}
		}
		if target < 0 {
			target, wrapped = starts[0], true
		}
	} else {
		for i := len(starts) - 1; i >= 0; i-- {
			if starts[i] < off {
				target = starts[i]
				break
			}
		}
		if target < 0 {
			target, wrapped = starts[len(starts)-1], true
		}
	}
	e.ed.SetCursor(e.posOf(target))
	if h := m.contentHeight(); e.ed.Cursor.Line < e.ed.Top || e.ed.Cursor.Line >= e.ed.Top+h {
		e.ed.Top = max(0, e.ed.Cursor.Line-h/3)
	}
	if wrapped {
		e.ed.Message, e.ed.Err = "wrapped to the other end of the page", false
	}
	return nil
}

var lineAnchor = regexp.MustCompile(`^L(\d+)`)

// follow opens what a link points at: a page, at its heading when it names
// one, or a file in $EDITOR.
func (m *WikiModel) follow(l wiki.Link) error {
	switch {
	case l.Kind == wiki.KindExternal:
		return errors.New("external link, not opened: " + l.Resolved)

	case (l.Kind == wiki.KindPage || l.Kind == wiki.KindHeading) && (l.Status == wiki.StatusOK || l.Status == wiki.StatusMissingHeading):
		if l.Resolved == m.cur.info.Path {
			m.history = append(m.history, m.here())
		} else if err := m.open(l.Resolved); err != nil {
			return err
		}
		m.focus = focusContent
		line := 1
		if l.Anchor != "" {
			found := false
			for _, h := range markdown.Parse([]byte(m.edit.ed.Text())).Headings {
				if h.Slug == markdown.Slug(l.Anchor) {
					line, found = h.Line, true
					break
				}
			}
			if !found {
				m.setStatus("no heading #" + l.Anchor)
			}
		}
		m.jumpTo(line)
		return nil

	case (l.Kind == wiki.KindFile || l.Kind == wiki.KindLine) && (l.Status == wiki.StatusOK || l.Status == wiki.StatusLineOutOfRange):
		line := 0
		if mm := lineAnchor.FindStringSubmatch(l.Anchor); mm != nil {
			line, _ = strconv.Atoi(mm[1])
		}
		cmd, err := editor.Open(filepath.Join(m.w.Repo, filepath.FromSlash(l.Resolved)), line, m.getenv)
		if err != nil {
			return err
		}
		m.after = m.exec(cmd, func(err error) tea.Msg { return fileEditedMsg{err} })
		return nil
	}
	return fmt.Errorf("%s: %s; :fix lists repairs", l.Status, l.Written())
}

// back returns to the previous page and position.
func (m *WikiModel) back() error {
	if len(m.history) == 0 {
		return errors.New("nothing to go back to")
	}
	p := m.history[len(m.history)-1]
	if p.page != m.cur.info.Path {
		if m.edit.ed.Dirty {
			return errUnsaved
		}
		if err := m.load(p.page); err != nil {
			return err
		}
	}
	m.history = m.history[:len(m.history)-1]
	m.edit.ed.SetCursor(p.cursor)
	m.edit.ed.Top = p.top
	m.focus = focusContent
	return nil
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
		m.screen, m.focus = screenRead, focusContent
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
	case screenLatest:
		if m.home == nil {
			return 0
		}
		return len(m.home.recent)
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
	sortTasks(tasks)
	m.tasks = tasks
	m.cursor = min(m.cursor, max(0, len(tasks)-1))
}

func (m *WikiModel) listMove(s func(pos, last int) int) {
	m.cursor = step(m.cursor, m.listLen()-1, s)
}

func (m *WikiModel) brokenOffers() {
	if m.cursor < len(m.broken) {
		m.showOffers(m.broken[m.cursor], screenBroken)
	}
}

func (m *WikiModel) toggleSelected() {
	if m.cursor < len(m.tasks) {
		m.toggleTask(m.tasks[m.cursor])
	}
}

func (m *WikiModel) toggleAllTasks() {
	m.allTasks = !m.allTasks
	m.loadTasks()
}

func (m *WikiModel) listEnter() {
	switch m.screen {
	case screenLatest:
		if m.home == nil || m.cursor >= len(m.home.recent) {
			return
		}
		if err := m.open(m.home.recent[m.cursor].Path); err != nil {
			m.setError(err)
			return
		}
		m.focus = focusContent
	case screenBroken:
		if m.cursor >= len(m.broken) {
			return
		}
		l := m.broken[m.cursor]
		m.openAt(l.Page, l.Line)
		if m.edit != nil && m.edit.page == l.Page {
			m.edit.ed.SetCursor(vim.Pos{Line: l.Line - 1, Col: max(0, l.Col-1)})
		}
	case screenTasks:
		if m.cursor >= len(m.tasks) {
			return
		}
		t := m.tasks[m.cursor]
		m.openAt(t.Page, t.Line)
	case screenPages:
		if m.cursor < len(m.listed) {
			if err := m.open(m.listed[m.cursor].Path); err != nil {
				m.setError(err)
				return
			}
			m.focus = focusContent
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
		m.setStatus("no repairs to offer for " + l.Written() + "; edit the link in the page")
		return
	}
	m.offers, m.offerFor, m.offerFrom = offers, l, from
	m.screen, m.cursor, m.scroll = screenOffers, 0, 0
}

// afterWrite brings the interface up to date with a write it made.
func (m *WikiModel) afterWrite() {
	if m.edit != nil {
		m.edit.links = nil
	}
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
	if isTab(m.base) {
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

// promptNew creates a page in the directory of the page open or selected,
// asking for its title unless one is given.
func (m *WikiModel) promptNew(title string) {
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
	create := func(title string) error {
		if strings.TrimSpace(title) == "" {
			return errors.New("a page needs a title")
		}
		if m.edit != nil && m.edit.ed.Dirty {
			return errUnsaved
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
		m.screen, m.focus = screenRead, focusContent
		m.setStatus("created " + info.File())
		return nil
	}
	if strings.TrimSpace(title) != "" {
		if err := create(title); err != nil {
			m.setError(err)
		}
		return
	}
	label := "new page title: "
	if dir != "" {
		label = "new page in " + dir + "/, title: "
	}
	m.askWiki(label, "", create)
}

// promptMove asks where to move the open page, unless to is given, shows what
// the move would rewrite, and asks before writing.
func (m *WikiModel) promptMove(to string) {
	from := m.cur.info.Path
	if strings.TrimSpace(to) != "" {
		m.confirmMove(from, to)
		return
	}
	m.askWiki("move "+from+" to: ", from, func(to string) error {
		m.confirmMove(from, to)
		return nil
	})
}

// confirmMove plans a move and asks before writing it.
func (m *WikiModel) confirmMove(from, to string) {
	to = strings.TrimSpace(to)
	if strings.HasSuffix(to, "/") {
		to += path.Base(from)
	}
	plan, err := m.w.PlanMove(from, to)
	if err != nil {
		m.setError(err)
		return
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
		m.dropDraft()
		m.cur.info.Path, m.edit.page = res.To, res.To
		m.afterWrite()
		status := fmt.Sprintf("moved to %s; %d links rewritten", res.To, len(res.Edits))
		if len(res.Broken) > 0 {
			status += fmt.Sprintf("; %d links broken, :broken lists them", len(res.Broken))
		}
		m.setStatus(status)
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

// editExternal hands the page to $EDITOR, for a page the built-in editor is
// not wanted for.
func (m *WikiModel) editExternal() error {
	if m.edit.ed.Dirty {
		return errUnsaved
	}
	page := m.cur.info.Path
	src, hash, err := m.w.Read(page)
	if err != nil {
		return err
	}
	e, err := editor.Start(string(src), m.getenv)
	if err != nil {
		return err
	}
	m.after = m.exec(e.Cmd, func(err error) tea.Msg {
		return wikiEditedMsg{page: page, hash: hash, src: src, edit: e, err: err}
	})
	return nil
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
