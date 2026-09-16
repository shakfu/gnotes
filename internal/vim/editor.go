package vim

import (
	"strings"
	"unicode"
)

// Mode is what a keypress means.
type Mode int

const (
	Normal Mode = iota
	Insert
	Replace
	Visual
	VisualLine
	Command // the ':', '/' or '?' line
)

func (m Mode) String() string {
	switch m {
	case Insert:
		return "INSERT"
	case Replace:
		return "REPLACE"
	case Visual:
		return "VISUAL"
	case VisualLine:
		return "VISUAL LINE"
	case Command:
		return "COMMAND"
	}
	return "NORMAL"
}

// Hooks are what the host does for the editor. A nil hook reports that the
// command is not available here.
type Hooks struct {
	Save      func(force bool) error
	Quit      func(force bool) error
	Reload    func() error
	Follow    func() error // ctrl-] and gf
	Back      func() error // ctrl-o and ctrl-t
	External  func(url string) error
	Clipboard func(text string)
	Complete  func(previous bool) // ctrl-n and ctrl-p in insert mode
	Command   func(name, arg string) (bool, error)
	Changed   func() // after every change, for autosave
}

// shiftwidth is the indent step, in spaces. Markdown lists nest in twos and
// tabs make goldmark lose the position of the text under them.
const shiftwidth = 2

// pendingOp is an operator waiting on a search, as in d/word.
type pendingOp struct {
	op     string
	reg    rune
	from   Pos
	active bool
}

// register holds yanked or deleted text.
type register struct {
	text     string
	linewise bool
}

// Editor is a buffer, a cursor and a mode.
type Editor struct {
	Buf    *Buffer
	Cursor Pos
	Mode   Mode
	Hooks  Hooks

	// Width and Height are the text area, in columns and rows; the host sets
	// them so that page movement and gj match what it draws.
	Width, Height int

	// Top is the first buffer line on screen, which the editor scrolls to
	// keep the cursor visible.
	Top int

	// Dirty reports unsaved changes.
	Dirty bool

	// Message is the status line's text, and Err styles it as a failure.
	Message string
	Err     bool

	// Search is the last pattern, and Highlight is set while its matches are
	// shown; the host reads both through Matches.
	Search    string
	Highlight bool

	pending   []string
	registers map[rune]register
	lastReg   rune

	// seq records the keys of a change for ".", and lastChange is the change
	// it repeats.
	seq        []string
	changing   bool
	lastChange []string
	replaying  bool

	visualStart Pos
	cmdKind     rune
	cmdline     []rune
	history     []string
	searchDir   int
	lastFind    [2]string // the f, F, t or T command and its target
	wantCol     int       // the column j and k aim for
	insertStart Pos
	waiting     pendingOp // an operator waiting for a search to finish
	holding     bool      // an insert session is part of the change that started it
	replaced    []string  // characters R overwrote, for backspace
}

// New returns an editor over text.
func New(text string) *Editor {
	return &Editor{
		Buf:       NewBuffer(text),
		Width:     80,
		Height:    24,
		registers: map[rune]register{},
		searchDir: 1,
	}
}

// Load replaces the text, keeping the cursor where it fits.
func (e *Editor) Load(text string) {
	e.Buf.SetText(text)
	e.Cursor = e.Buf.clamp(e.Cursor, false)
	e.Dirty = false
	e.Mode = Normal
	e.pending = nil
}

// SetCursor moves the cursor, as the host does when it opens a page at a line
// or completes a word, and sets the column vertical movement aims for. In
// insert mode the cursor may sit just past the last character.
func (e *Editor) SetCursor(p Pos) {
	e.Cursor = e.Buf.clamp(p, e.Mode == Insert || e.Mode == Replace)
	e.wantCol = e.Cursor.Col
}

// Text is the buffer's content.
func (e *Editor) Text() string { return e.Buf.Text() }

// Pending is the keys of an unfinished command, for the status line.
func (e *Editor) Pending() string { return strings.Join(e.pending, "") }

// CommandLine is what is typed on the ':' or '/' line, with its prefix.
func (e *Editor) CommandLine() (string, bool) {
	if e.Mode != Command {
		return "", false
	}
	return string(e.cmdKind) + string(e.cmdline), true
}

// Selection is the visual selection, or false in other modes. The range is
// inclusive of both positions; in line mode the columns span whole lines.
func (e *Editor) Selection() (Pos, Pos, bool) {
	if e.Mode != Visual && e.Mode != VisualLine {
		return Pos{}, Pos{}, false
	}
	a, z := sorted(e.visualStart, e.Cursor)
	if e.Mode == VisualLine {
		a.Col = 0
		z.Col = max(0, len(e.Buf.Line(z.Line)))
	}
	return a, z, true
}

// Key feeds one keypress: a rune as a string, or a name such as "esc",
// "enter", "backspace", "tab", "ctrl+r" or "up".
func (e *Editor) Key(k string) {
	if !e.replaying && e.changing {
		e.seq = append(e.seq, k)
	}
	switch e.Mode {
	case Insert, Replace:
		e.insertKey(k)
	case Command:
		e.commandKey(k)
	default:
		e.normalKey(k)
	}
	e.scroll()
}

// Keys feeds several keypresses, as a test or "." does.
func (e *Editor) Keys(keys ...string) {
	for _, k := range keys {
		e.Key(k)
	}
}

// Type feeds a string one rune at a time.
func (e *Editor) Type(text string) {
	for _, r := range text {
		e.Key(string(r))
	}
}

// scroll keeps the cursor on screen.
func (e *Editor) scroll() {
	h := max(1, e.Height)
	e.Top = min(e.Top, max(0, e.Buf.Lines()-1))
	if e.Cursor.Line < e.Top {
		e.Top = e.Cursor.Line
	}
	if e.Cursor.Line >= e.Top+h {
		e.Top = e.Cursor.Line - h + 1
	}
	e.Top = max(0, e.Top)
}

func (e *Editor) setMessage(s string) { e.Message, e.Err = s, false }
func (e *Editor) fail(s string)       { e.Message, e.Err = s, true }

// change runs an edit as one undo step and marks the buffer dirty. Edits made
// in insert mode join the group the insert started, so one u undoes the whole
// insertion.
func (e *Editor) change(edit func()) {
	e.Buf.begin(e.Cursor)
	edit()
	if e.Mode != Insert && e.Mode != Replace && !e.holding {
		e.Buf.commit(e.Cursor)
	}
	e.Dirty = true
	if e.Hooks.Changed != nil {
		e.Hooks.Changed()
	}
}

// ---------------------------------------------------------------- insert

func (e *Editor) insertKey(k string) {
	switch k {
	case "esc", "ctrl+[":
		e.leaveInsert()
		return
	case "enter":
		e.change(func() { e.insertText(e.continueList()) })
	case "backspace":
		e.change(e.backspace)
	case "delete":
		e.change(func() {
			line := e.Buf.Line(e.Cursor.Line)
			if e.Cursor.Col < len(line) {
				e.Buf.Replace(e.Cursor, Pos{e.Cursor.Line, e.Cursor.Col + 1}, "")
			} else if e.Cursor.Line < e.Buf.Lines()-1 {
				e.Buf.Replace(e.Cursor, Pos{e.Cursor.Line + 1, 0}, "")
			}
		})
	case "tab":
		e.change(func() { e.insertText(strings.Repeat(" ", shiftwidth)) })
	case "ctrl+w":
		e.change(e.deleteWordBack)
	case "ctrl+u":
		e.change(func() {
			start := Pos{e.Cursor.Line, indentOf(e.Buf.Line(e.Cursor.Line))}
			if !start.Before(e.Cursor) {
				start.Col = 0
			}
			e.Buf.Replace(start, e.Cursor, "")
			e.Cursor = start
		})
	case "ctrl+t":
		e.change(func() { e.indent(e.Cursor.Line, e.Cursor.Line, 1) })
	case "ctrl+d":
		e.change(func() { e.indent(e.Cursor.Line, e.Cursor.Line, -1) })
	case "ctrl+n", "ctrl+p":
		if e.Hooks.Complete != nil {
			e.Hooks.Complete(k == "ctrl+p")
		}
	case "ctrl+o":
		// One normal-mode command, then back to insert.
		e.Mode = Normal
		e.pending = nil
		e.setMessage("-- (insert) --")
		return
	case "left", "right", "up", "down", "home", "end":
		if target, linewise, _, r := e.motion([]string{k}, 1, true); r == done {
			e.Cursor = e.Buf.clamp(target, true)
			if !linewise {
				e.wantCol = e.Cursor.Col
			}
		}
	case "space":
		e.change(func() { e.insertText(" ") })
	default:
		if len([]rune(k)) == 1 {
			e.change(func() { e.insertText(k) })
		}
	}
}

// insertText writes at the cursor, overwriting in replace mode.
func (e *Editor) insertText(text string) {
	end := e.Cursor
	if e.Mode == Replace && text != "\n" {
		line := e.Buf.Line(e.Cursor.Line)
		n := len([]rune(text))
		over := min(e.Cursor.Col+n, len(line))
		e.replaced = append(e.replaced, string(line[e.Cursor.Col:over]))
		end = Pos{e.Cursor.Line, over}
	}
	e.Cursor = e.Buf.Replace(e.Cursor, end, text)
}

func (e *Editor) backspace() {
	if e.Mode == Replace && len(e.replaced) > 0 {
		last := e.replaced[len(e.replaced)-1]
		e.replaced = e.replaced[:len(e.replaced)-1]
		if e.Cursor.Col > 0 {
			e.Cursor.Col--
			e.Buf.Replace(e.Cursor, Pos{e.Cursor.Line, e.Cursor.Col + 1}, last)
		}
		return
	}
	switch {
	case e.Cursor.Col > 0:
		start := Pos{e.Cursor.Line, e.Cursor.Col - 1}
		// Backspace over a full indent step when only spaces precede.
		if indent := indentOf(e.Buf.Line(e.Cursor.Line)); e.Cursor.Col <= indent && e.Cursor.Col%shiftwidth == 0 {
			start.Col = e.Cursor.Col - shiftwidth
		}
		e.Buf.Replace(start, e.Cursor, "")
		e.Cursor = start
	case e.Cursor.Line > 0:
		prev := Pos{e.Cursor.Line - 1, len(e.Buf.Line(e.Cursor.Line - 1))}
		e.Buf.Replace(prev, e.Cursor, "")
		e.Cursor = prev
	}
}

func (e *Editor) deleteWordBack() {
	line := e.Buf.Line(e.Cursor.Line)
	i := e.Cursor.Col
	for i > 0 && isSpace(line[i-1]) {
		i--
	}
	if i > 0 {
		class := classOf(line[i-1])
		for i > 0 && !isSpace(line[i-1]) && classOf(line[i-1]) == class {
			i--
		}
	}
	start := Pos{e.Cursor.Line, i}
	e.Buf.Replace(start, e.Cursor, "")
	e.Cursor = start
}

// leaveInsert returns to normal mode, as esc does.
func (e *Editor) leaveInsert() {
	e.holding = false
	e.Buf.commit(e.Cursor)
	e.Mode = Normal
	e.replaced = nil
	e.Cursor = e.Buf.clamp(Pos{e.Cursor.Line, e.Cursor.Col - 1}, false)
	if e.changing && !e.replaying {
		e.lastChange = e.seq
	}
	e.changing = false
	e.Message = ""
}

// startInsert enters insert mode, recording the keys for ".".
func (e *Editor) startInsert(mode Mode) {
	e.Mode = mode
	e.insertStart = e.Cursor
	e.replaced = nil
	e.Message = "-- " + mode.String() + " --"
}

// ---------------------------------------------------------------- markdown

var listMarker = func() func(string) (indent, marker, rest string, ok bool) {
	return func(line string) (string, string, string, bool) {
		i := 0
		for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
			i++
		}
		indent, rest := line[:i], line[i:]
		switch {
		case strings.HasPrefix(rest, "> "), rest == ">":
			return indent, "> ", strings.TrimPrefix(rest, ">"), true
		}
		// A bullet, optionally with a checkbox, or a number.
		if len(rest) > 1 && strings.ContainsRune("-*+", rune(rest[0])) && rest[1] == ' ' {
			marker := rest[:2]
			after := rest[2:]
			if len(after) >= 4 && after[0] == '[' && after[2] == ']' && after[3] == ' ' &&
				strings.ContainsRune(" xX", rune(after[1])) {
				return indent, marker + "[ ] ", after[4:], true
			}
			return indent, marker, after, true
		}
		digits := 0
		for digits < len(rest) && rest[digits] >= '0' && rest[digits] <= '9' {
			digits++
		}
		if digits > 0 && digits+1 < len(rest) && (rest[digits] == '.' || rest[digits] == ')') && rest[digits+1] == ' ' {
			return indent, rest[:digits+2], rest[digits+2:], true
		}
		return "", "", "", false
	}
}()

// continueList is what Enter inserts: a new line, and the list marker again
// when the cursor is in a list item. An item with no text ends the list.
func (e *Editor) continueList() string {
	line := e.Buf.LineString(e.Cursor.Line)
	indent, marker, rest, ok := listMarker(line)
	if !ok || e.Cursor.Col < len(indent)+len(marker) {
		return "\n"
	}
	if strings.TrimSpace(rest) == "" {
		// Ending the list: clear the item, leaving a blank line.
		e.Buf.Replace(Pos{e.Cursor.Line, 0}, Pos{e.Cursor.Line, len([]rune(line))}, "")
		e.Cursor = Pos{e.Cursor.Line, 0}
		return "\n"
	}
	if n, ok := numbered(marker); ok {
		marker = itoa(n+1) + marker[len(marker)-2:]
	}
	return "\n" + indent + marker
}

func numbered(marker string) (int, bool) {
	digits := 0
	for digits < len(marker) && marker[digits] >= '0' && marker[digits] <= '9' {
		digits++
	}
	if digits == 0 {
		return 0, false
	}
	n := 0
	for _, c := range marker[:digits] {
		n = n*10 + int(c-'0')
	}
	return n, true
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

// toggleCheckbox ticks or clears the checklist item on the cursor's line.
func (e *Editor) toggleCheckbox() {
	line := e.Buf.LineString(e.Cursor.Line)
	i := strings.Index(line, "[ ]")
	box := "[x]"
	if i < 0 {
		if i = strings.IndexAny(line, "["); i >= 0 && strings.HasPrefix(strings.ToLower(line[i:]), "[x]") {
			box = "[ ]"
		} else {
			e.fail("no checklist item on this line")
			return
		}
	}
	_, marker, _, ok := listMarker(line)
	if !ok || !strings.Contains(marker, "[") {
		e.fail("no checklist item on this line")
		return
	}
	col := len([]rune(line[:i]))
	e.change(func() {
		e.Buf.Replace(Pos{e.Cursor.Line, col}, Pos{e.Cursor.Line, col + 3}, box)
	})
}

// indent shifts lines by n steps, keeping them at column zero or beyond.
func (e *Editor) indent(from, to, n int) {
	for l := from; l <= to && l < e.Buf.Lines(); l++ {
		line := e.Buf.Line(l)
		width := indentOf(line)
		want := max(0, width+n*shiftwidth)
		if len(line) == 0 && n < 0 {
			continue
		}
		e.Buf.Replace(Pos{l, 0}, Pos{l, width}, strings.Repeat(" ", want))
	}
	e.Cursor = Pos{e.Cursor.Line, indentOf(e.Buf.Line(e.Cursor.Line))}
	e.Cursor = e.Buf.clamp(e.Cursor, false)
}

func indentOf(line []rune) int {
	i := 0
	for i < len(line) && isSpace(line[i]) {
		i++
	}
	return i
}

func isSpace(r rune) bool { return r == ' ' || r == '\t' }

// classOf groups runes as vim does for word motions: word characters, other
// punctuation, and whitespace.
func classOf(r rune) int {
	switch {
	case isSpace(r):
		return 0
	case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_':
		return 2
	}
	return 1
}
