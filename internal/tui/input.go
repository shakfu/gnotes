package tui

import "strings"

// input is a single-line text field with a cursor.
//
// Written here rather than taken from a widget library: the interface needs
// exactly one line of editing with a handful of readline keys, and owning it
// keeps the command line, the search line and the prompts behaving identically
// without a dependency in between.
type input struct {
	// runes is the content. Runes rather than a string so that the cursor
	// indexes characters, not bytes, and moving over a multi-byte character
	// takes one keypress.
	runes []rune

	// cursor is the insertion point, from 0 to len(runes).
	cursor int
}

// set replaces the content and puts the cursor at the end.
func (in *input) set(s string) {
	in.runes = []rune(s)
	in.cursor = len(in.runes)
}

// clear empties the field.
func (in *input) clear() { in.set("") }

// String returns the content.
func (in *input) String() string { return string(in.runes) }

// empty reports whether anything has been typed.
func (in *input) empty() bool { return len(in.runes) == 0 }

// insert types a rune at the cursor.
func (in *input) insert(r rune) {
	in.runes = append(in.runes, 0)
	copy(in.runes[in.cursor+1:], in.runes[in.cursor:])
	in.runes[in.cursor] = r
	in.cursor++
}

// insertString types several runes at the cursor, for a paste.
func (in *input) insertString(s string) {
	for _, r := range s {
		in.insert(r)
	}
}

// backspace deletes the rune before the cursor.
func (in *input) backspace() {
	if in.cursor == 0 {
		return
	}
	in.runes = append(in.runes[:in.cursor-1], in.runes[in.cursor:]...)
	in.cursor--
}

// deleteForward deletes the rune under the cursor.
func (in *input) deleteForward() {
	if in.cursor >= len(in.runes) {
		return
	}
	in.runes = append(in.runes[:in.cursor], in.runes[in.cursor+1:]...)
}

// deleteWord deletes the word before the cursor, the readline behaviour of
// ctrl-w.
func (in *input) deleteWord() {
	i := in.cursor
	for i > 0 && in.runes[i-1] == ' ' {
		i--
	}
	for i > 0 && in.runes[i-1] != ' ' {
		i--
	}
	in.runes = append(in.runes[:i], in.runes[in.cursor:]...)
	in.cursor = i
}

// deleteToStart clears everything before the cursor, ctrl-u.
func (in *input) deleteToStart() {
	in.runes = in.runes[in.cursor:]
	in.cursor = 0
}

// left, right, home and end move the cursor.
func (in *input) left() {
	if in.cursor > 0 {
		in.cursor--
	}
}

func (in *input) right() {
	if in.cursor < len(in.runes) {
		in.cursor++
	}
}

func (in *input) home() { in.cursor = 0 }
func (in *input) end()  { in.cursor = len(in.runes) }

// render draws the field with a block cursor, clipped to width columns.
//
// When the content is longer than the field, the window follows the cursor, so
// typing past the right edge keeps what is being typed in view.
func (in *input) render(prefix string, width int) string {
	avail := width - len([]rune(prefix))
	if avail < 4 {
		return prefix
	}

	start := 0
	if in.cursor >= avail {
		start = in.cursor - avail + 1
	}
	end := min(len(in.runes), start+avail)

	var b strings.Builder
	b.WriteString(prefix)
	for i := start; i < end; i++ {
		b.WriteRune(in.runes[i])
	}
	// The cursor sits past the last character when appending, so it needs a
	// space of its own to occupy.
	if in.cursor == len(in.runes) {
		b.WriteString("_")
	}
	return b.String()
}

// cursorColumn is where the terminal cursor belongs, given a prefix.
func (in *input) cursorColumn(prefix string, width int) int {
	avail := width - len([]rune(prefix))
	if avail < 1 {
		return len([]rune(prefix))
	}
	offset := in.cursor
	if offset >= avail {
		offset = avail - 1
	}
	return len([]rune(prefix)) + offset
}

// history recall. The command line remembers what has been run so that a
// repeated command is one keypress away rather than retyped.

// pushHistory records a command, skipping a repeat of the previous one.
func (m *Model) pushHistory(cmd string) {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return
	}
	if n := len(m.history); n > 0 && m.history[n-1] == cmd {
		m.histPos = len(m.history)
		return
	}
	m.history = append(m.history, cmd)
	m.histPos = len(m.history)
}

// historyPrev walks back through past commands.
func (m *Model) historyPrev() {
	if m.histPos == 0 {
		return
	}
	m.histPos--
	m.input.set(m.history[m.histPos])
}

// historyNext walks forward, ending at an empty line.
func (m *Model) historyNext() {
	if m.histPos >= len(m.history) {
		return
	}
	m.histPos++
	if m.histPos == len(m.history) {
		m.input.clear()
		return
	}
	m.input.set(m.history[m.histPos])
}
