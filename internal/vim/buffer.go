// Package vim is a modal text editor engine with vim's keys, over a buffer of
// lines. It draws nothing and touches no files: a host feeds it keys, reads
// the buffer and the cursor, and supplies hooks for saving, quitting,
// following a link and completing text.
//
// The keys it accepts are in docs/dev/wiki-design.md, section 11.
package vim

import (
	"strings"
)

// Pos is a position in the buffer: a line, and a column in runes.
type Pos struct {
	Line, Col int
}

// Before reports whether p comes before q.
func (p Pos) Before(q Pos) bool {
	return p.Line < q.Line || (p.Line == q.Line && p.Col < q.Col)
}

func sorted(a, b Pos) (Pos, Pos) {
	if b.Before(a) {
		return b, a
	}
	return a, b
}

// Buffer is the text being edited, as lines without their newlines. A buffer
// always has at least one line.
type Buffer struct {
	lines [][]rune

	// undo holds completed change groups, redo those undone.
	undo, redo []group

	// open is the group being recorded, nil between commands.
	open *group
}

// change is one replacement of text, and the cursor around it.
type change struct {
	at                        Pos
	removed, inserted         string
	cursorBefore, cursorAfter Pos
}

// group is one undoable step: everything a command changed.
type group struct {
	changes      []change
	cursorBefore Pos
	cursorAfter  Pos
}

// NewBuffer holds text, which is split on newlines. A trailing newline is the
// end of the last line, not an empty line after it.
func NewBuffer(text string) *Buffer {
	b := &Buffer{}
	b.SetText(text)
	return b
}

// SetText replaces the whole buffer, discarding the undo history.
func (b *Buffer) SetText(text string) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.TrimSuffix(text, "\n")
	b.lines = nil
	for _, l := range strings.Split(text, "\n") {
		b.lines = append(b.lines, []rune(l))
	}
	b.undo, b.redo, b.open = nil, nil, nil
}

// Text returns the buffer with a newline after every line.
func (b *Buffer) Text() string {
	var sb strings.Builder
	for _, l := range b.lines {
		sb.WriteString(string(l))
		sb.WriteByte('\n')
	}
	return sb.String()
}

// Lines is the number of lines.
func (b *Buffer) Lines() int { return len(b.lines) }

// Line returns one line, or nothing when it is out of range.
func (b *Buffer) Line(i int) []rune {
	if i < 0 || i >= len(b.lines) {
		return nil
	}
	return b.lines[i]
}

// LineString returns one line as a string.
func (b *Buffer) LineString(i int) string { return string(b.Line(i)) }

// end is the position just past the last line.
func (b *Buffer) end() Pos {
	last := len(b.lines) - 1
	return Pos{last, len(b.lines[last])}
}

// clamp brings a position inside the buffer. In normal mode the cursor sits
// on a character, so it stops one short of the end of a line unless past is
// set, as it is in insert mode.
func (b *Buffer) clamp(p Pos, past bool) Pos {
	p.Line = max(0, min(p.Line, len(b.lines)-1))
	limit := len(b.lines[p.Line])
	if !past {
		limit = max(0, limit-1)
	}
	p.Col = max(0, min(p.Col, limit))
	return p
}

// Slice returns the text between two positions.
func (b *Buffer) Slice(a, z Pos) string {
	a, z = sorted(b.clamp(a, true), b.clamp(z, true))
	if a.Line == z.Line {
		return string(b.lines[a.Line][a.Col:z.Col])
	}
	var sb strings.Builder
	sb.WriteString(string(b.lines[a.Line][a.Col:]))
	for i := a.Line + 1; i < z.Line; i++ {
		sb.WriteByte('\n')
		sb.WriteString(string(b.lines[i]))
	}
	sb.WriteByte('\n')
	sb.WriteString(string(b.lines[z.Line][:z.Col]))
	return sb.String()
}

// begin starts a change group. Nested calls join the group already open, so a
// command built of several edits undoes in one step.
func (b *Buffer) begin(cursor Pos) {
	if b.open == nil {
		b.open = &group{cursorBefore: cursor}
	}
}

// commit ends the change group, dropping one that changed nothing.
func (b *Buffer) commit(cursor Pos) {
	if b.open == nil {
		return
	}
	g := b.open
	b.open = nil
	if len(g.changes) == 0 {
		return
	}
	g.cursorAfter = cursor
	b.undo = append(b.undo, *g)
	b.redo = nil
}

// Replace puts text in place of the text between a and z, and returns the
// position just after what it wrote.
func (b *Buffer) Replace(a, z Pos, text string) Pos {
	a, z = sorted(b.clamp(a, true), b.clamp(z, true))
	removed := b.Slice(a, z)
	if removed == "" && text == "" {
		return a
	}
	if b.open != nil {
		b.open.changes = append(b.open.changes, change{at: a, removed: removed, inserted: text})
	}
	return b.splice(a, z, text)
}

// splice rewrites the lines between two positions without recording anything.
func (b *Buffer) splice(a, z Pos, text string) Pos {
	pre := string(b.lines[a.Line][:a.Col])
	post := string(b.lines[z.Line][z.Col:])
	parts := strings.Split(pre+text+post, "\n")
	fresh := make([][]rune, len(parts))
	for i, p := range parts {
		fresh[i] = []rune(p)
	}
	rest := append(fresh, b.lines[z.Line+1:]...)
	b.lines = append(b.lines[:a.Line], rest...)

	end := Pos{a.Line + len(parts) - 1, 0}
	end.Col = len([]rune(parts[len(parts)-1])) - len([]rune(post))
	return end
}

// Undo reverses the last group and returns the cursor for it.
func (b *Buffer) Undo() (Pos, bool) {
	if len(b.undo) == 0 {
		return Pos{}, false
	}
	g := b.undo[len(b.undo)-1]
	b.undo = b.undo[:len(b.undo)-1]
	for i := len(g.changes) - 1; i >= 0; i-- {
		c := g.changes[i]
		b.splice(c.at, b.advance(c.at, c.inserted), c.removed)
	}
	b.redo = append(b.redo, g)
	return b.clamp(g.cursorBefore, false), true
}

// Redo reapplies the last undone group.
func (b *Buffer) Redo() (Pos, bool) {
	if len(b.redo) == 0 {
		return Pos{}, false
	}
	g := b.redo[len(b.redo)-1]
	b.redo = b.redo[:len(b.redo)-1]
	for _, c := range g.changes {
		b.splice(c.at, b.advance(c.at, c.removed), c.inserted)
	}
	b.undo = append(b.undo, g)
	return b.clamp(g.cursorAfter, false), true
}

// advance is the position after text starting at p.
func (b *Buffer) advance(p Pos, text string) Pos {
	if text == "" {
		return p
	}
	n := strings.Count(text, "\n")
	if n == 0 {
		return Pos{p.Line, p.Col + len([]rune(text))}
	}
	last := text[strings.LastIndexByte(text, '\n')+1:]
	return Pos{p.Line + n, len([]rune(last))}
}
