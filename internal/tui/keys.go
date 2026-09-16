package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// binding is one entry of a keymap: the keys that run it, how the help lists
// it, and its hint on the status line. The dispatcher, the hints and the help
// all read the keymaps, so none can list a key the others lack.
type binding struct {
	keys []string // as tea.KeyMsg.String names them
	show string   // the keys as the help lists them; empty leaves the row out
	help string
	hint string // the status line's text; empty leaves it out
	run  func(m *WikiModel)
}

// keymap is the bindings of one context, titled for the help.
type keymap struct {
	title    string
	bindings []binding
}

func (km *keymap) find(k string) *binding {
	for i := range km.bindings {
		for _, key := range km.bindings[i].keys {
			if key == k {
				return &km.bindings[i]
			}
		}
	}
	return nil
}

// helpHint is always drawn, at the right edge of the status bar.
const helpHint = "? help"

// Cursor steps for moves: each takes the position and the last index.
func down(pos, last int) int  { return pos + 1 }
func up(pos, last int) int    { return pos - 1 }
func first(pos, last int) int { return 0 }
func final(pos, last int) int { return last }

// moves binds j k g G to move, which applies a step to one cursor.
func moves(move func(m *WikiModel, step func(pos, last int) int)) []binding {
	return []binding{
		{keys: []string{"j", "down"}, show: "j k", help: "move", run: func(m *WikiModel) { move(m, down) }},
		{keys: []string{"k", "up"}, run: func(m *WikiModel) { move(m, up) }},
		{keys: []string{"g", "home"}, show: "g G", help: "first and last", run: func(m *WikiModel) { move(m, first) }},
		{keys: []string{"G", "end"}, run: func(m *WikiModel) { move(m, final) }},
	}
}

// step moves a cursor and keeps it within 0..last.
func step(pos, last int, s func(pos, last int) int) int {
	return max(0, min(s(pos, last), last))
}

var keysGlobal = &keymap{"anywhere", []binding{
	{keys: []string{"/"}, show: "/", help: "search titles, headings, tags and text", hint: "/ search", run: (*WikiModel).startSearch},
	{keys: []string{"ctrl+p"}, show: "ctrl-p", help: "open a page by title or path", hint: "^p open", run: (*WikiModel).startOpen},
	{keys: []string{"n"}, show: "n", help: "new page, beside the one open or selected", hint: "n new", run: func(m *WikiModel) { m.promptNew("") }},
	{keys: []string{"c"}, show: "c", help: "broken links", hint: "c broken", run: func(m *WikiModel) { m.showList(screenBroken) }},
	{keys: []string{"t"}, show: "t", help: "the tasks tab", hint: "t tasks", run: func(m *WikiModel) { m.showTab(screenTasks) }},
	{keys: []string{"O"}, show: "O", help: "the overview, on its last tab", hint: "O overview", run: (*WikiModel).showHome},
	{keys: []string{"R"}, show: "R", help: "reload pages and git details", run: (*WikiModel).reloadAll},
	{keys: []string{"esc"}, show: "esc", help: "back to the overview or the page", run: (*WikiModel).escape},
	{keys: []string{":"}, show: ":", help: "a command, as listed under commands", run: (*WikiModel).startCommand},
	{keys: []string{"?"}, show: "?", help: "this help", run: (*WikiModel).openHelp},
	{keys: []string{"q"}, show: "q ctrl-c", help: "quit", run: func(m *WikiModel) { m.quitting = true }},
}}

var keysTabs = &keymap{"overview tabs", []binding{
	{keys: []string{"tab"}, show: "tab shift-tab", help: "next and previous tab: latest, tasks, stats", hint: "tab next tab", run: func(m *WikiModel) { m.cycleTab(1) }},
	{keys: []string{"shift+tab"}, run: func(m *WikiModel) { m.cycleTab(-1) }},
}}

var keysStats = &keymap{"stats", append(moves((*WikiModel).homeMove),
	binding{keys: []string{"h", "left", "l", "right"}, show: "h l", help: "the other column", hint: "h l column", run: (*WikiModel).homeSwitch},
	binding{keys: []string{"enter"}, show: "enter", help: "open the row's page or list", hint: "enter open", run: (*WikiModel).homeEnter},
)}

var keysTree = &keymap{"tree", append(moves((*WikiModel).treeMove),
	binding{keys: []string{"enter", "o"}, show: "enter o", help: "open a page, or a directory's README; fold a directory without one", hint: "enter open", run: (*WikiModel).treeEnter},
	binding{keys: []string{" "}, show: "space", help: "fold a directory", run: (*WikiModel).treeFold},
	binding{keys: []string{"l", "right"}, show: "l right", help: "unfold a directory, step into it, or open a page", hint: "h l fold", run: (*WikiModel).treeRight},
	binding{keys: []string{"h", "left"}, show: "h left", help: "fold a directory, or go to the one holding the row", run: (*WikiModel).treeLeft},
	binding{keys: []string{"tab"}, show: "tab shift-tab", help: "next and previous pane", run: func(m *WikiModel) { m.cycleFocus(1) }},
	binding{keys: []string{"shift+tab"}, run: func(m *WikiModel) { m.cycleFocus(-1) }},
)}

var keysPanel = &keymap{"backlinks", append(moves((*WikiModel).panelMove),
	binding{keys: []string{"enter"}, show: "enter", help: "open the linking page at the link", hint: "enter open", run: (*WikiModel).panelEnter},
	binding{keys: []string{"esc", "b", "h", "left"}, show: "esc b h", help: "back to the page", hint: "esc page", run: (*WikiModel).toContent},
	binding{keys: []string{"tab"}, show: "tab shift-tab", help: "next and previous pane", run: func(m *WikiModel) { m.cycleFocus(1) }},
	binding{keys: []string{"shift+tab"}, run: func(m *WikiModel) { m.cycleFocus(-1) }},
)}

var keysList = &keymap{"lists", append(moves((*WikiModel).listMove),
	binding{keys: []string{"enter"}, show: "enter", help: "open the row", hint: "enter open", run: (*WikiModel).listEnter},
	binding{keys: []string{"esc"}, show: "esc", help: "back", hint: "esc back", run: (*WikiModel).escape},
)}

var keysBroken = &keymap{"broken links", []binding{
	{keys: []string{"f"}, show: "f", help: "repairs for the link", hint: "f repairs", run: (*WikiModel).brokenOffers},
}}

var keysTasks = &keymap{"tasks", []binding{
	{keys: []string{" ", "x"}, show: "space x", help: "toggle: an item open or done, a task page open, doing or done", hint: "space toggle", run: (*WikiModel).toggleSelected},
	{keys: []string{"a"}, show: "a", help: "all tasks, or open ones", hint: "a all/open", run: (*WikiModel).toggleAllTasks},
}}

var keysOffers = &keymap{"repairs", []binding{
	{keys: []string{"enter"}, show: "enter", help: "apply the repair", hint: "enter apply", run: (*WikiModel).listEnter},
	{keys: []string{"esc"}, show: "esc", help: "back to the link", hint: "esc back", run: func(m *WikiModel) { m.screen = m.offerFrom }},
}}

// keysContent is for the help only: the page's buffer handles its own keys,
// and keyContent the few it takes first.
var keysContent = &keymap{"page", []binding{
	{show: "tab shift-tab", help: "next and previous pane, in NORMAL mode"},
	{show: "ctrl-p", help: "open a page by title or path"},
	{show: "vim keys", help: "modes, counts, operators, text objects, registers, . and u"},
	{show: "/ ? n N", help: "search the page"},
	{show: "< >", help: "previous and next link"},
	{show: "enter ctrl-] gf", help: "follow the link under the cursor"},
	{show: "[ ]", help: "half a screen up and down"},
	{show: "ctrl-o", help: "back to the previous page"},
	{show: ":w :e!", help: "write the page; load it again, losing your edits"},
	{show: ":q :wq :q!", help: "quit gwiki; write and quit; quit, losing your edits"},
	{show: "[[ ctrl-n", help: "in insert mode, complete a page, a heading after #, a path after ]("},
	{show: "ctrl-space", help: "tick the checklist item on this line"},
	{show: ": commands", help: "vim's, then those listed under commands"},
}}

// allKeymaps is the help's order after the current screen's keymaps.
var allKeymaps = []*keymap{keysGlobal, keysTabs, keysStats, keysTree, keysContent, keysPanel, keysList, keysBroken, keysTasks, keysOffers}

// keymaps are the keymaps in force on a screen, most specific first.
func (m *WikiModel) keymaps(screen wscreen) []*keymap {
	switch screen {
	case screenLatest:
		return []*keymap{keysList, keysTabs, keysGlobal}
	case screenStats:
		return []*keymap{keysStats, keysTabs, keysGlobal}
	case screenBroken:
		return []*keymap{keysBroken, keysList, keysGlobal}
	case screenTasks:
		return []*keymap{keysTasks, keysList, keysTabs, keysGlobal}
	case screenOffers:
		return []*keymap{keysOffers, keysList, keysGlobal}
	case screenPages:
		return []*keymap{keysList, keysGlobal}
	case screenRead:
		switch {
		case m.focus == focusTree || m.cur == nil:
			return []*keymap{keysTree, keysGlobal}
		case m.focus == focusPanel:
			return []*keymap{keysPanel, keysGlobal}
		}
		// The buffer takes the keys; this is for the help.
		return []*keymap{keysContent}
	}
	return nil
}

// keyNormal runs the first binding for k in the screen's keymaps.
func (m *WikiModel) keyNormal(k string) {
	if m.screen == screenRead && m.focus != focusTree && m.cur == nil {
		m.focus = focusTree
	}
	if m.screen == screenRead && m.focus == focusContent {
		return
	}
	for _, km := range m.keymaps(m.screen) {
		if b := km.find(k); b != nil {
			b.run(m)
			return
		}
	}
}

// hints are the status line's hints for the screen. The global ones are
// listed only where no other key list fills the line.
func (m *WikiModel) hints() []string {
	if m.screen == screenHelp {
		return []string{"j k scroll", "any other key closes"}
	}
	maps := m.keymaps(m.screen)
	withGlobal := isTab(m.screen) || (m.screen == screenRead && maps[0] == keysTree)
	seen := map[string]bool{}
	var out []string
	for _, km := range maps {
		if km == keysGlobal && !withGlobal {
			continue
		}
		for _, b := range km.bindings {
			// An overview tab is where esc and O lead, so it has no hint for them.
			if b.hint == "" || seen[b.keys[0]] || isTab(m.screen) && (b.keys[0] == "esc" || b.keys[0] == "O") {
				continue
			}
			seen[b.keys[0]] = true
			out = append(out, b.hint)
		}
	}
	return out
}

// fitHints joins as many hints as fit in room columns.
func fitHints(hints []string, room int) string {
	line := ""
	for _, h := range hints {
		next := h
		if line != "" {
			next = line + "  " + h
		}
		if ansi.StringWidth(next) > room {
			break
		}
		line = next
	}
	return line
}

// ---------------------------------------------------------------- help

func (m *WikiModel) openHelp() {
	m.helpFrom, m.helpScroll = m.screen, 0
	m.screen = screenHelp
}

// helpLines lists every keymap, those of the screen help was opened from first,
// then the commands.
func (m *WikiModel) helpLines() []string {
	type row struct{ show, help string }
	var sections [][]row
	var titles []string
	width := 0
	add := func(show, help string) {
		sections[len(sections)-1] = append(sections[len(sections)-1], row{show, help})
		width = max(width, ansi.StringWidth(show))
	}
	done := map[*keymap]bool{}
	for _, km := range append(m.keymaps(m.helpFrom), allKeymaps...) {
		if done[km] {
			continue
		}
		done[km] = true
		titles, sections = append(titles, km.title), append(sections, nil)
		for _, b := range km.bindings {
			if b.show != "" {
				add(b.show, b.help)
			}
		}
	}
	titles, sections = append(titles, "commands"), append(sections, nil)
	for _, c := range wikiCommands {
		add(strings.TrimSpace(":"+strings.Join(c.names, " :")+" "+c.args), c.help)
	}

	var lines []string
	for i, rows := range sections {
		if i > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, " "+styleHeader.Render(titles[i]))
		for _, r := range rows {
			lines = append(lines, "   "+pad(r.show, width)+"  "+styleDim.Render(r.help))
		}
	}
	return lines
}

func (m *WikiModel) helpHeight() int { return m.bodyHeight() }

// keyHelp scrolls the help; any other key closes it.
func (m *WikiModel) keyHelp(msg tea.KeyMsg) {
	last := max(0, len(m.helpLines())-m.helpHeight())
	half := max(1, m.helpHeight()/2)
	to := func(pos int) { m.helpScroll = max(0, min(pos, last)) }
	switch msg.String() {
	case "j", "down":
		to(m.helpScroll + 1)
	case "k", "up":
		to(m.helpScroll - 1)
	case " ", "ctrl+d", "pgdown":
		to(m.helpScroll + half)
	case "ctrl+u", "pgup":
		to(m.helpScroll - half)
	case "g", "home":
		to(0)
	case "G", "end":
		to(last)
	default:
		m.screen = m.helpFrom
	}
}

func (m *WikiModel) viewHelp() []string {
	lines := m.helpLines()
	m.helpScroll = max(0, min(m.helpScroll, len(lines)-m.helpHeight()))
	return lines[m.helpScroll:min(len(lines), m.helpScroll+m.helpHeight())]
}
