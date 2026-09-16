package vim

import (
	"strings"
	"unicode"
)

// result says what the keys parsed so far amount to.
type result int

const (
	done result = iota
	needMore
	bad
)

// changeCommands begin a change, so "." can repeat them.
var changeCommands = map[string]bool{
	"d": true, "c": true, "x": true, "X": true, "s": true, "S": true, "r": true,
	"R": true, "J": true, "~": true, "p": true, "P": true, ">": true, "<": true,
	"i": true, "a": true, "I": true, "A": true, "o": true, "O": true,
	"D": true, "C": true, "gu": true, "gU": true, "g~": true, "gi": true,
	"ctrl+@": true, "ctrl+ ": true, "ctrl+space": true,
}

func (e *Editor) normalKey(k string) {
	if k == "esc" || k == "ctrl+[" {
		e.pending = nil
		if e.Mode == Visual || e.Mode == VisualLine {
			e.Mode = Normal
		}
		e.Message = ""
		return
	}
	e.Message = ""
	e.pending = append(e.pending, k)
	if r := e.parse(); r != needMore {
		if e.changing && e.Mode != Insert && e.Mode != Replace && !e.replaying {
			e.lastChange = e.seq
			e.changing = false
		}
		e.pending = nil
	}
}

// parse reads a register, a count and a command from the pending keys.
func (e *Editor) parse() result {
	keys := e.pending
	i := 0
	reg := rune(0)
	if keys[i] == `"` {
		if len(keys) < i+2 {
			return needMore
		}
		reg = firstRune(keys[i+1])
		i += 2
	}
	count, i := readCount(keys, i)
	if i >= len(keys) {
		return needMore
	}
	return e.run(keys[i:], count, reg)
}

func readCount(keys []string, i int) (int, int) {
	count := 0
	for i < len(keys) && len(keys[i]) == 1 && keys[i][0] >= '0' && keys[i][0] <= '9' {
		if keys[i] == "0" && count == 0 {
			break // a bare 0 is a motion
		}
		count = count*10 + int(keys[i][0]-'0')
		i++
	}
	return count, i
}

func firstRune(s string) rune {
	for _, r := range s {
		return r
	}
	return 0
}

func atLeast(count int) int {
	if count == 0 {
		return 1
	}
	return count
}

func (e *Editor) run(keys []string, count int, reg rune) result {
	head := keys[0]
	visual := e.Mode == Visual || e.Mode == VisualLine

	// Two-key commands starting with g. The motions keep both keys, since
	// motion reads the prefix itself.
	move := keys
	if head == "g" {
		if len(keys) < 2 {
			return needMore
		}
		head = "g" + keys[1]
		keys = keys[1:]
	}
	if !e.replaying && changeCommands[head] {
		e.changing, e.seq = true, append([]string{}, e.pending...)
	}

	switch head {
	// ------------------------------------------------------ operators
	case "d", "c", "y", ">", "<", "gu", "gU", "g~":
		if visual {
			a, z, _ := e.Selection()
			e.applyOperator(head, a, z, e.Mode == VisualLine, reg)
			return done
		}
		rest := keys[1:]
		if len(rest) == 0 {
			return needMore
		}
		// A doubled operator works on whole lines.
		if rest[0] == head || (len(head) == 2 && rest[0] == head[1:]) {
			last := min(e.Cursor.Line+atLeast(count)-1, e.Buf.Lines()-1)
			e.applyOperator(head, Pos{e.Cursor.Line, 0}, Pos{last, len(e.Buf.Line(last))}, true, reg)
			return done
		}
		a, z, linewise, r := e.opRange(head, rest, atLeast(count))
		if r != done {
			return r
		}
		e.applyOperator(head, a, z, linewise, reg)
		return done

	// ------------------------------------------------------ simple edits
	case "x", "X":
		n := atLeast(count)
		a, z := e.Cursor, Pos{e.Cursor.Line, e.Cursor.Col + n}
		if head == "X" {
			a, z = Pos{e.Cursor.Line, max(0, e.Cursor.Col-n)}, e.Cursor
		}
		z.Col = min(z.Col, len(e.Buf.Line(e.Cursor.Line)))
		if a == z {
			return done
		}
		e.applyOperator("d", a, Pos{z.Line, z.Col - 1}, false, reg)
		return done
	case "D", "C":
		line := len(e.Buf.Line(e.Cursor.Line))
		op := "d"
		if head == "C" {
			op = "c"
		}
		e.applyOperator(op, e.Cursor, Pos{e.Cursor.Line, max(e.Cursor.Col, line-1)}, false, reg)
		return done
	case "Y":
		e.applyOperator("y", Pos{e.Cursor.Line, 0}, Pos{e.Cursor.Line, len(e.Buf.Line(e.Cursor.Line))}, true, reg)
		return done
	case "s":
		e.applyOperator("c", e.Cursor, Pos{e.Cursor.Line, e.Cursor.Col + atLeast(count) - 1}, false, reg)
		return done
	case "S":
		last := min(e.Cursor.Line+atLeast(count)-1, e.Buf.Lines()-1)
		e.applyOperator("c", Pos{e.Cursor.Line, 0}, Pos{last, len(e.Buf.Line(last))}, true, reg)
		return done
	case "r":
		if len(keys) < 2 {
			return needMore
		}
		return e.replaceChars(keys[1], atLeast(count))
	case "J":
		e.joinLines(max(2, atLeast(count)))
		return done
	case "~":
		e.toggleCase(atLeast(count))
		return done
	case "p", "P":
		e.paste(head == "p", atLeast(count), reg)
		return done
	case "ctrl+@", "ctrl+ ", "ctrl+space":
		e.toggleCheckbox()
		return done

	// ------------------------------------------------------ modes
	case "i", "a", "I", "A", "o", "O", "gi", "R", "v", "V":
		// In visual mode i and a select a text object.
		if visual && (head == "i" || head == "a") {
			if len(keys) < 2 {
				return needMore
			}
			a, z, linewise, ok := e.textObject(head == "a", keys[1])
			if !ok {
				e.fail("no text object here")
				return done
			}
			e.visualStart, e.Cursor = a, z
			if linewise {
				e.Mode = VisualLine
			}
			return done
		}
		return e.enterMode(head, visual)
	case "u":
		if p, ok := e.Buf.Undo(); ok {
			e.Cursor, e.Dirty = p, true
			e.changed()
			e.setMessage("undo")
		} else {
			e.fail("already at the oldest change")
		}
		return done
	case "ctrl+r":
		if p, ok := e.Buf.Redo(); ok {
			e.Cursor, e.Dirty = p, true
			e.changed()
			e.setMessage("redo")
		} else {
			e.fail("already at the newest change")
		}
		return done
	case ".":
		if len(e.lastChange) == 0 {
			e.fail("nothing to repeat")
			return done
		}
		keys := e.lastChange
		if count > 0 {
			// A count on "." replaces the one the change was made with.
			for len(keys) > 0 && len(keys[0]) == 1 && keys[0][0] >= '1' && keys[0][0] <= '9' {
				keys = keys[1:]
			}
			keys = append(strings.Split(itoa(count), ""), keys...)
		}
		e.replaying = true
		e.pending = nil
		e.Keys(keys...)
		e.replaying = false
		return done
	case ":", "/", "?":
		e.Mode, e.cmdKind, e.cmdline = Command, firstRune(head), nil
		if visual && head == ":" {
			e.cmdline = []rune("'<,'>")
		}
		return done

	// ------------------------------------------------------ links and files
	case "ctrl+]", "gf":
		return e.hook(e.Hooks.Follow, "following links is not available here")
	case "ctrl+o", "ctrl+t":
		return e.hook(e.Hooks.Back, "going back is not available here")
	case "gx":
		if e.Hooks.External == nil {
			e.fail("opening links is not available here")
			return done
		}
		if url, ok := e.linkUnderCursor(); ok {
			if err := e.Hooks.External(url); err != nil {
				e.fail(err.Error())
			}
			return done
		}
		e.fail("no link under the cursor")
		return done

	// ------------------------------------------------------ view
	case "z":
		if len(keys) < 2 {
			return needMore
		}
		switch keys[1] {
		case "z":
			e.Top = max(0, e.Cursor.Line-(e.Height-1)/2)
		case "t":
			e.Top = e.Cursor.Line
		case "b":
			e.Top = max(0, e.Cursor.Line-e.Height+1)
		default:
			return bad
		}
		return done
	case "ctrl+e":
		e.Top = min(e.Top+atLeast(count), max(0, e.Buf.Lines()-1))
		e.Cursor.Line = max(e.Cursor.Line, e.Top)
		e.Cursor = e.Buf.clamp(e.Cursor, false)
		return done
	case "ctrl+y":
		e.Top = max(0, e.Top-atLeast(count))
		e.Cursor.Line = min(e.Cursor.Line, e.bottom())
		e.Cursor = e.Buf.clamp(e.Cursor, false)
		return done

	}

	// Anything else is a movement.
	if vertical[head] {
		e.wantCol = max(e.wantCol, e.Cursor.Col)
	} else {
		e.wantCol = e.Cursor.Col
	}
	target, linewise, _, r := e.motion(move, count, false)
	if r != done {
		return r
	}
	if linewise && vertical[head] {
		e.Cursor = e.Buf.clamp(Pos{target.Line, max(e.wantCol, target.Col)}, false)
		return done
	}
	e.Cursor = e.Buf.clamp(target, false)
	e.wantCol = e.Cursor.Col
	return done
}

// vertical are the movements that keep the column they aim for.
var vertical = map[string]bool{
	"j": true, "k": true, "down": true, "up": true, "gj": true, "gk": true,
	"ctrl+d": true, "ctrl+u": true, "ctrl+f": true, "ctrl+b": true, "pgdown": true, "pgup": true,
}

func (e *Editor) hook(fn func() error, missing string) result {
	if fn == nil {
		e.fail(missing)
		return done
	}
	if err := fn(); err != nil {
		e.fail(err.Error())
	}
	return done
}

func (e *Editor) changed() {
	if e.Hooks.Changed != nil {
		e.Hooks.Changed()
	}
}

// enterMode handles the keys that change mode.
func (e *Editor) enterMode(head string, visual bool) result { //nolint:revive // one switch over the mode keys
	switch head {
	case "v":
		if e.Mode == Visual {
			e.Mode = Normal
		} else {
			e.Mode, e.visualStart = Visual, e.Cursor
		}
	case "V":
		if e.Mode == VisualLine {
			e.Mode = Normal
		} else {
			e.Mode, e.visualStart = VisualLine, e.Cursor
		}
	case "i":
		e.startInsert(Insert)
	case "a":
		e.Cursor = e.Buf.clamp(Pos{e.Cursor.Line, e.Cursor.Col + 1}, true)
		e.startInsert(Insert)
	case "I":
		e.Cursor = Pos{e.Cursor.Line, indentOf(e.Buf.Line(e.Cursor.Line))}
		e.startInsert(Insert)
	case "A":
		e.Cursor = Pos{e.Cursor.Line, len(e.Buf.Line(e.Cursor.Line))}
		e.startInsert(Insert)
	case "gi":
		e.Cursor = e.Buf.clamp(e.insertStart, true)
		e.startInsert(Insert)
	case "R":
		e.startInsert(Replace)
	case "o", "O":
		if head == "o" && visual {
			// In visual mode o swaps the ends of the selection.
			e.visualStart, e.Cursor = e.Cursor, e.visualStart
			return done
		}
		line := e.Cursor.Line
		at := Pos{line, len(e.Buf.Line(line))}
		text := "\n"
		if head == "O" {
			at = Pos{line, 0}
		}
		indent, marker, _, isList := listMarker(e.Buf.LineString(line))
		prefix := strings.Repeat(" ", indentOf(e.Buf.Line(line)))
		if isList {
			if n, ok := numbered(marker); ok && head == "o" {
				marker = itoa(n+1) + marker[len(marker)-2:]
			}
			prefix = indent + marker
		}
		e.holding = true
		e.change(func() {
			if head == "O" {
				e.Cursor = e.Buf.Replace(at, at, prefix+text)
				e.Cursor = Pos{line, len([]rune(prefix))}
				return
			}
			e.Cursor = e.Buf.Replace(at, at, text+prefix)
		})
		e.startInsert(Insert)
	}
	return done
}

// ---------------------------------------------------------------- operators

// applyOperator runs an operator over a range. The range is inclusive of z
// for charwise operators and covers whole lines when linewise.
func (e *Editor) applyOperator(op string, a, z Pos, linewise bool, reg rune) {
	a, z = sorted(a, z)
	if linewise {
		a = Pos{a.Line, 0}
		z = Pos{z.Line, len(e.Buf.Line(z.Line))}
	} else {
		z = Pos{z.Line, min(z.Col+1, len(e.Buf.Line(z.Line)))} // inclusive
	}
	text := e.Buf.Slice(a, z)
	if linewise {
		text += "\n"
	}

	switch op {
	case "y":
		e.yank(reg, text, linewise)
		e.Cursor = e.Buf.clamp(a, false)
		e.setMessage(count(strings.Count(text, "\n"), "line") + " yanked")
	case "d", "c":
		e.yank(reg, text, linewise)
		e.change(func() {
			if linewise && op == "d" {
				// The newline goes with the lines, unless they are the last.
				if z.Line+1 < e.Buf.Lines() {
					z = Pos{z.Line + 1, 0}
				} else if a.Line > 0 {
					a = Pos{a.Line - 1, len(e.Buf.Line(a.Line - 1))}
				}
			}
			e.Cursor = e.Buf.Replace(a, z, "")
			e.Cursor = e.Buf.clamp(e.Cursor, op == "c")
		})
		if op == "c" {
			e.Mode = Normal
			e.holding = true
			e.startInsert(Insert)
		} else if e.Mode == Visual || e.Mode == VisualLine {
			e.Mode = Normal
		}
	case ">", "<":
		n := 1
		if op == "<" {
			n = -1
		}
		e.change(func() { e.indent(a.Line, z.Line, n) })
		e.Mode = leaveVisual(e.Mode)
	case "gu", "gU", "g~":
		e.change(func() {
			e.Buf.Replace(a, z, mapCase(op, text))
			e.Cursor = a
		})
		e.Mode = leaveVisual(e.Mode)
	}
}

func leaveVisual(m Mode) Mode {
	if m == Visual || m == VisualLine {
		return Normal
	}
	return m
}

func mapCase(op, text string) string {
	switch op {
	case "gu":
		return strings.ToLower(text)
	case "gU":
		return strings.ToUpper(text)
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsUpper(r) {
			return unicode.ToLower(r)
		}
		return unicode.ToUpper(r)
	}, text)
}

func count(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return itoa(n) + " " + what + "s"
}

// yank stores text in a register: the one named, else the unnamed register
// and register 0.
func (e *Editor) yank(reg rune, text string, linewise bool) {
	r := register{text: text, linewise: linewise}
	switch reg {
	case 0:
		e.registers['"'] = r
		e.registers['0'] = r
	case '_':
	case '+':
		e.registers['+'] = r
		e.registers['"'] = r
		if e.Hooks.Clipboard != nil {
			e.Hooks.Clipboard(text)
		}
	default:
		if unicode.IsUpper(reg) {
			lower := unicode.ToLower(reg)
			old := e.registers[lower]
			r.text = old.text + text
			r.linewise = old.linewise || linewise
			reg = lower
		}
		e.registers[reg] = r
		e.registers['"'] = r
	}
}

// Register returns a register's text, for the host or a test.
func (e *Editor) Register(name rune) string { return e.registers[name].text }

// SetRegister fills a register, which is how a paste from the system
// clipboard arrives.
func (e *Editor) SetRegister(name rune, text string) {
	e.registers[name] = register{text: text, linewise: strings.HasSuffix(text, "\n")}
}

func (e *Editor) paste(after bool, count int, reg rune) {
	if reg == 0 {
		reg = '"'
	}
	r := e.registers[reg]
	if r.text == "" {
		e.fail("register is empty")
		return
	}
	text := strings.Repeat(r.text, count)
	e.change(func() {
		if r.linewise {
			at := Pos{e.Cursor.Line, 0}
			if after {
				at = Pos{e.Cursor.Line + 1, 0}
			}
			if at.Line >= e.Buf.Lines() {
				// Past the end: append after the last line instead.
				last := Pos{e.Buf.Lines() - 1, len(e.Buf.Line(e.Buf.Lines() - 1))}
				e.Buf.Replace(last, last, "\n"+strings.TrimSuffix(text, "\n"))
				e.Cursor = Pos{e.Buf.Lines() - 1, 0}
				return
			}
			e.Buf.Replace(at, at, text)
			e.Cursor = Pos{at.Line, indentOf(e.Buf.Line(at.Line))}
			return
		}
		at := e.Cursor
		if after && len(e.Buf.Line(at.Line)) > 0 {
			at.Col++
		}
		end := e.Buf.Replace(at, at, text)
		e.Cursor = e.Buf.clamp(Pos{end.Line, end.Col - 1}, false)
	})
}

func (e *Editor) replaceChars(k string, count int) result {
	if k == "esc" {
		return done
	}
	r := firstRune(k)
	if k == "enter" {
		r = '\n'
	}
	line := e.Buf.Line(e.Cursor.Line)
	if e.Cursor.Col+count > len(line) {
		e.fail("not enough characters on the line")
		return done
	}
	e.change(func() {
		end := Pos{e.Cursor.Line, e.Cursor.Col + count}
		e.Buf.Replace(e.Cursor, end, strings.Repeat(string(r), count))
		if r != '\n' {
			e.Cursor = Pos{e.Cursor.Line, e.Cursor.Col + count - 1}
		}
	})
	return done
}

func (e *Editor) joinLines(n int) {
	e.change(func() {
		for i := 0; i < n-1 && e.Cursor.Line+1 < e.Buf.Lines(); i++ {
			line := e.Buf.Line(e.Cursor.Line)
			next := e.Buf.Line(e.Cursor.Line + 1)
			join := " "
			if len(line) == 0 || isSpace(line[len(line)-1]) {
				join = ""
			}
			start := indentOf(next)
			if len(next) == 0 {
				join = ""
			}
			at := Pos{e.Cursor.Line, len(line)}
			e.Buf.Replace(at, Pos{e.Cursor.Line + 1, start}, join)
			e.Cursor = at
		}
	})
}

func (e *Editor) toggleCase(n int) {
	line := e.Buf.Line(e.Cursor.Line)
	end := min(e.Cursor.Col+n, len(line))
	if end <= e.Cursor.Col {
		return
	}
	e.change(func() {
		at := Pos{e.Cursor.Line, end}
		e.Buf.Replace(e.Cursor, at, mapCase("g~", string(line[e.Cursor.Col:end])))
		e.Cursor = e.Buf.clamp(at, false)
	})
}
