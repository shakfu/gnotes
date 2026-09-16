package vim

import (
	"regexp"
	"strings"
)

// motion returns where a movement lands. linewise and inclusive describe the
// range an operator would take; forOp is set when an operator is waiting, so
// that cw can behave as ce does in vim.
func (e *Editor) motion(keys []string, count int, forOp bool) (Pos, bool, bool, result) {
	n := atLeast(count)
	cur := e.Cursor
	line := e.Buf.Line(cur.Line)
	head := keys[0]
	if head == "g" {
		if len(keys) < 2 {
			return cur, false, false, needMore
		}
		head = "g" + keys[1]
	}

	switch head {
	case "h", "left", "backspace":
		return Pos{cur.Line, max(0, cur.Col-n)}, false, false, done
	case "l", "right", " ", "space":
		limit := len(line)
		if !forOp {
			limit = max(0, len(line)-1)
		}
		return Pos{cur.Line, min(limit, cur.Col+n)}, false, false, done
	case "0", "home":
		return Pos{cur.Line, 0}, false, false, done
	case "^":
		return Pos{cur.Line, indentOf(line)}, false, false, done
	case "$", "end":
		last := min(cur.Line+n-1, e.Buf.Lines()-1)
		return Pos{last, max(0, len(e.Buf.Line(last))-1)}, false, true, done
	case "g_":
		text := strings.TrimRight(string(line), " \t")
		return Pos{cur.Line, max(0, len([]rune(text))-1)}, false, true, done
	case "j", "down", "ctrl+n":
		return Pos{min(cur.Line+n, e.Buf.Lines()-1), e.wantCol}, true, false, done
	case "k", "up", "ctrl+p":
		return Pos{max(cur.Line-n, 0), e.wantCol}, true, false, done
	case "gj", "gk":
		return e.displayLine(head == "gj", n), false, false, done
	case "gg":
		target := 0
		if count > 0 {
			target = min(count-1, e.Buf.Lines()-1)
		}
		return Pos{target, indentOf(e.Buf.Line(target))}, true, false, done
	case "G":
		target := e.Buf.Lines() - 1
		if count > 0 {
			target = min(count-1, target)
		}
		return Pos{target, indentOf(e.Buf.Line(target))}, true, false, done
	case "H", "M", "L":
		top, bottom := e.Top, e.bottom()
		target := top
		switch head {
		case "M":
			target = (top + bottom) / 2
		case "L":
			target = bottom
		}
		return Pos{target, indentOf(e.Buf.Line(target))}, true, false, done
	case "ctrl+d", "ctrl+u", "ctrl+f", "ctrl+b", "pgdown", "pgup":
		step := max(1, e.Height/2)
		if head == "ctrl+f" || head == "ctrl+b" || head == "pgdown" || head == "pgup" {
			step = max(1, e.Height-2)
		}
		if head == "ctrl+u" || head == "ctrl+b" || head == "pgup" {
			step = -step
		}
		e.Top = max(0, min(e.Top+step, max(0, e.Buf.Lines()-1)))
		target := max(0, min(cur.Line+step, e.Buf.Lines()-1))
		return Pos{target, e.wantCol}, true, false, done
	case "w", "W":
		return e.wordForward(n, head == "W", forOp), false, false, done
	case "b", "B":
		return e.wordBack(n, head == "B"), false, false, done
	case "e", "E":
		return e.wordEnd(n, head == "E"), false, true, done
	case "ge":
		return e.wordEndBack(n), false, true, done
	case "{", "}":
		return e.paragraph(head == "}", n), false, false, done
	case "f", "F", "t", "T":
		if len(keys) < 2 {
			return cur, false, false, needMore
		}
		e.lastFind = [2]string{head, keys[1]}
		p, ok := e.find(head, keys[1], n, false)
		if !ok {
			e.fail("not found on the line: " + keys[1])
			return cur, false, false, bad
		}
		return p, false, head == "f" || head == "t", done
	case ";", ",":
		if e.lastFind[0] == "" {
			e.fail("no previous find")
			return cur, false, false, bad
		}
		cmd := e.lastFind[0]
		if head == "," {
			cmd = map[string]string{"f": "F", "F": "f", "t": "T", "T": "t"}[cmd]
		}
		p, ok := e.find(cmd, e.lastFind[1], n, true)
		if !ok {
			e.fail("not found on the line: " + e.lastFind[1])
			return cur, false, false, bad
		}
		return p, false, cmd == "f" || cmd == "t", done
	case "%":
		p, ok := e.matchBracket()
		if !ok {
			e.fail("no bracket to match")
			return cur, false, false, bad
		}
		return p, false, true, done
	case "n", "N":
		dir := e.searchDir
		if head == "N" {
			dir = -dir
		}
		p, ok := e.searchFrom(e.Search, dir, n)
		if !ok {
			e.fail("pattern not found: " + e.Search)
			return cur, false, false, bad
		}
		return p, false, false, done
	case "*", "#":
		word := e.wordUnderCursor()
		if word == "" {
			e.fail("no word under the cursor")
			return cur, false, false, bad
		}
		e.Search = `\b` + regexp.QuoteMeta(word) + `\b`
		e.searchDir = 1
		if head == "#" {
			e.searchDir = -1
		}
		p, ok := e.searchFrom(e.Search, e.searchDir, n)
		if !ok {
			e.fail("pattern not found: " + word)
			return cur, false, false, bad
		}
		return p, false, false, done
	}
	return cur, false, false, bad
}

// opRange is the range an operator covers: a text object, or the cursor to a
// motion's target.
func (e *Editor) opRange(op string, keys []string, count int) (Pos, Pos, bool, result) {
	cur := e.Cursor
	// A count between the operator and the motion multiplies the outer one.
	if inner, i := readCount(keys, 0); i > 0 {
		count *= inner
		keys = keys[i:]
		if len(keys) == 0 {
			return cur, cur, false, needMore
		}
	}
	// An operator waiting on a search: d/word runs when the search does.
	if keys[0] == "/" || keys[0] == "?" {
		e.waiting = pendingOp{op: op, from: cur, active: true}
		e.Mode, e.cmdKind, e.cmdline = Command, firstRune(keys[0]), nil
		return cur, cur, false, done
	}
	if keys[0] == "i" || keys[0] == "a" {
		if len(keys) < 2 {
			return cur, cur, false, needMore
		}
		a, z, linewise, ok := e.textObject(keys[0] == "a", keys[1])
		if !ok {
			e.fail("no text object here")
			return cur, cur, false, bad
		}
		return a, z, linewise, done
	}

	// cw acts like ce when the cursor is on a word, as vim does.
	if op == "c" && (keys[0] == "w" || keys[0] == "W") {
		line := e.Buf.Line(cur.Line)
		if cur.Col < len(line) && !isSpace(line[cur.Col]) {
			keys = append([]string{map[string]string{"w": "e", "W": "E"}[keys[0]]}, keys[1:]...)
		}
	}

	target, linewise, inclusive, r := e.motion(keys, count, true)
	if r != done {
		return cur, cur, false, r
	}
	if linewise {
		return Pos{min(cur.Line, target.Line), 0}, Pos{max(cur.Line, target.Line), 0}, true, done
	}
	a, z := sorted(cur, target)
	if !inclusive {
		// An exclusive motion stops one short of its target.
		if z.Col > 0 {
			z.Col--
		} else if z.Line > a.Line {
			z = Pos{z.Line - 1, max(0, len(e.Buf.Line(z.Line-1)))}
			// dw at the end of a line stops there rather than joining lines.
			if keys[0] == "w" || keys[0] == "W" {
				z.Col = max(0, z.Col-1)
			}
		}
	}
	return a, z, false, done
}

// ---------------------------------------------------------------- words

// wordForward is w: the start of the nth next word.
func (e *Editor) wordForward(n int, big, forOp bool) Pos {
	p := e.Cursor
	for i := 0; i < n; i++ {
		line := e.Buf.Line(p.Line)
		if p.Col >= len(line) {
			if p.Line+1 >= e.Buf.Lines() {
				return Pos{p.Line, len(line)}
			}
			p = Pos{p.Line + 1, 0}
			if len(e.Buf.Line(p.Line)) == 0 {
				continue // a blank line is a word
			}
			if !isSpace(firstOf(e.Buf.Line(p.Line))) {
				continue
			}
		}
		line = e.Buf.Line(p.Line)
		if p.Col < len(line) && !isSpace(line[p.Col]) {
			class := class(line, p.Col, big)
			for p.Col < len(line) && !isSpace(line[p.Col]) && class == classAt(line, p.Col, big) {
				p.Col++
			}
		}
		// The last word an operator takes stops at the end of the line.
		if forOp && i == n-1 && p.Col >= len(line) {
			return p
		}
		p = e.skipSpace(p)
	}
	return p
}

func firstOf(line []rune) rune {
	if len(line) == 0 {
		return ' '
	}
	return line[0]
}

func class(line []rune, col int, big bool) int { return classAt(line, col, big) }

func classAt(line []rune, col int, big bool) int {
	if col < 0 || col >= len(line) {
		return 0
	}
	c := classOf(line[col])
	if big && c > 0 {
		return 2
	}
	return c
}

// skipSpace moves forward over whitespace, including line ends.
func (e *Editor) skipSpace(p Pos) Pos {
	for {
		line := e.Buf.Line(p.Line)
		for p.Col < len(line) && isSpace(line[p.Col]) {
			p.Col++
		}
		if p.Col < len(line) || p.Line+1 >= e.Buf.Lines() {
			return p
		}
		p = Pos{p.Line + 1, 0}
		if len(e.Buf.Line(p.Line)) == 0 {
			return p
		}
	}
}

// wordBack is b.
func (e *Editor) wordBack(n int, big bool) Pos {
	p := e.Cursor
	for i := 0; i < n; i++ {
		p = e.stepBack(p)
		for {
			line := e.Buf.Line(p.Line)
			if p.Col < len(line) && isSpace(line[p.Col]) || len(line) == 0 && p.Col == 0 && p.Line > 0 {
				if len(line) == 0 {
					break
				}
				p = e.stepBack(p)
				continue
			}
			break
		}
		line := e.Buf.Line(p.Line)
		if p.Col < len(line) {
			c := classAt(line, p.Col, big)
			for p.Col > 0 && classAt(line, p.Col-1, big) == c && !isSpace(line[p.Col-1]) {
				p.Col--
			}
		}
	}
	return p
}

// stepBack moves one character back, over line ends.
func (e *Editor) stepBack(p Pos) Pos {
	if p.Col > 0 {
		return Pos{p.Line, p.Col - 1}
	}
	if p.Line == 0 {
		return p
	}
	return Pos{p.Line - 1, max(0, len(e.Buf.Line(p.Line-1))-1)}
}

// wordEnd is e.
func (e *Editor) wordEnd(n int, big bool) Pos {
	p := e.Cursor
	for i := 0; i < n; i++ {
		p = e.stepForward(p)
		p = e.skipSpace(p)
		line := e.Buf.Line(p.Line)
		if p.Col < len(line) {
			c := classAt(line, p.Col, big)
			for p.Col+1 < len(line) && classAt(line, p.Col+1, big) == c && !isSpace(line[p.Col+1]) {
				p.Col++
			}
		}
	}
	return p
}

// wordEndBack is ge: back over this word, then the space, to the end of the
// word before.
func (e *Editor) wordEndBack(n int) Pos {
	p := e.Cursor
	at := func(p Pos) rune {
		line := e.Buf.Line(p.Line)
		if p.Col < len(line) {
			return line[p.Col]
		}
		return ' '
	}
	for i := 0; i < n; i++ {
		start := p
		for !isSpace(at(p)) {
			q := e.stepBack(p)
			if q == p {
				return start
			}
			p = q
		}
		for isSpace(at(p)) {
			q := e.stepBack(p)
			if q == p {
				return start
			}
			p = q
		}
	}
	return p
}

func (e *Editor) stepForward(p Pos) Pos {
	line := e.Buf.Line(p.Line)
	if p.Col+1 <= len(line)-1 || p.Line+1 >= e.Buf.Lines() {
		return Pos{p.Line, min(p.Col+1, max(0, len(line)-1))}
	}
	if p.Col+1 < len(line) {
		return Pos{p.Line, p.Col + 1}
	}
	return Pos{p.Line + 1, 0}
}

// wordUnderCursor is the word * and # search for.
func (e *Editor) wordUnderCursor() string {
	line := e.Buf.Line(e.Cursor.Line)
	i := e.Cursor.Col
	if i >= len(line) {
		return ""
	}
	if classOf(line[i]) != 2 {
		for i < len(line) && classOf(line[i]) != 2 {
			i++
		}
		if i >= len(line) {
			return ""
		}
	}
	start := i
	for start > 0 && classOf(line[start-1]) == 2 {
		start--
	}
	end := i
	for end < len(line) && classOf(line[end]) == 2 {
		end++
	}
	return string(line[start:end])
}

// paragraph is { and }: the next blank line.
func (e *Editor) paragraph(forward bool, n int) Pos {
	l := e.Cursor.Line
	for i := 0; i < n; i++ {
		step := 1
		if !forward {
			step = -1
		}
		l += step
		for l > 0 && l < e.Buf.Lines()-1 && !(len(e.Buf.Line(l)) == 0 && len(e.Buf.Line(l-step)) > 0) {
			l += step
		}
		l = max(0, min(l, e.Buf.Lines()-1))
	}
	return Pos{l, 0}
}

// find is f, F, t and T on the cursor's line.
func (e *Editor) find(cmd, target string, n int, repeat bool) (Pos, bool) {
	r := firstRune(target)
	line := e.Buf.Line(e.Cursor.Line)
	col := e.Cursor.Col
	forward := cmd == "f" || cmd == "t"
	// A repeated t stops at the same place unless it steps over the target.
	if repeat {
		switch {
		case cmd == "t" && col+1 < len(line) && line[col+1] == r:
			col++
		case cmd == "T" && col > 0 && line[col-1] == r:
			col--
		}
	}
	for i := 0; i < n; i++ {
		found := -1
		if forward {
			start := col + 1
			if cmd == "t" && i > 0 {
				start++
			}
			for j := start; j < len(line); j++ {
				if line[j] == r {
					found = j
					break
				}
			}
		} else {
			start := col - 1
			if cmd == "T" && i > 0 {
				start--
			}
			for j := start; j >= 0; j-- {
				if line[j] == r {
					found = j
					break
				}
			}
		}
		if found < 0 {
			return e.Cursor, false
		}
		col = found
	}
	switch cmd {
	case "t":
		col--
	case "T":
		col++
	}
	return Pos{e.Cursor.Line, col}, true
}

var brackets = map[rune]struct {
	match   rune
	forward bool
}{
	'(': {')', true}, ')': {'(', false},
	'[': {']', true}, ']': {'[', false},
	'{': {'}', true}, '}': {'{', false},
}

// matchBracket is %: the bracket matching the first one at or after the
// cursor on its line.
func (e *Editor) matchBracket() (Pos, bool) {
	line := e.Buf.Line(e.Cursor.Line)
	col := -1
	for i := e.Cursor.Col; i < len(line); i++ {
		if _, ok := brackets[line[i]]; ok {
			col = i
			break
		}
	}
	if col < 0 {
		return e.Cursor, false
	}
	open := line[col]
	b := brackets[open]
	depth := 0
	p := Pos{e.Cursor.Line, col}
	for {
		line := e.Buf.Line(p.Line)
		if p.Col >= 0 && p.Col < len(line) {
			switch line[p.Col] {
			case open:
				depth++
			case b.match:
				depth--
				if depth == 0 {
					return p, true
				}
			}
		}
		if b.forward {
			p.Col++
			if p.Col >= len(line) {
				if p.Line+1 >= e.Buf.Lines() {
					return e.Cursor, false
				}
				p = Pos{p.Line + 1, 0}
			}
			continue
		}
		p.Col--
		if p.Col < 0 {
			if p.Line == 0 {
				return e.Cursor, false
			}
			p = Pos{p.Line - 1, max(0, len(e.Buf.Line(p.Line-1))-1)}
		}
	}
}

// displayLine is gj and gk: a line as drawn, wrapped to the width.
func (e *Editor) displayLine(down bool, n int) Pos {
	width := max(1, e.Width)
	p := e.Cursor
	for i := 0; i < n; i++ {
		rows := Wrap(e.Buf.Line(p.Line), width)
		row := rowOf(rows, p.Col)
		offset := p.Col - rows[row]
		switch {
		case down && row+1 < len(rows):
			p.Col = min(rows[row+1]+offset, len(e.Buf.Line(p.Line)))
		case down:
			if p.Line+1 >= e.Buf.Lines() {
				return p
			}
			p = Pos{p.Line + 1, offset}
		case row > 0:
			p.Col = rows[row-1] + offset
		default:
			if p.Line == 0 {
				return p
			}
			p.Line--
			last := Wrap(e.Buf.Line(p.Line), width)
			p.Col = last[len(last)-1] + offset
		}
		p = e.Buf.clamp(p, false)
	}
	return p
}

func rowOf(rows []int, col int) int {
	row := 0
	for i, start := range rows {
		if start <= col {
			row = i
		}
	}
	return row
}

// Wrap returns the column each display row of a line starts at, breaking at
// spaces where it can. The host draws lines the same way.
func Wrap(line []rune, width int) []int {
	rows := []int{0}
	if width <= 0 {
		return rows
	}
	start := 0
	for start+width < len(line) {
		brk := -1
		for i := start + width; i > start; i-- {
			if isSpace(line[i-1]) {
				brk = i
				break
			}
		}
		if brk <= start {
			brk = start + width
		}
		rows = append(rows, brk)
		start = brk
	}
	return rows
}

// ---------------------------------------------------------------- search

// searchFrom finds the nth match of pattern from the cursor in a direction.
func (e *Editor) searchFrom(pattern string, dir, n int) (Pos, bool) {
	if pattern == "" {
		return e.Cursor, false
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		e.fail("bad pattern: " + err.Error())
		return e.Cursor, false
	}
	p := e.Cursor
	for i := 0; i < n; i++ {
		next, ok := e.searchOnce(re, p, dir)
		if !ok {
			return e.Cursor, false
		}
		p = next
	}
	return p, true
}

// searchOnce finds the next match, wrapping around the buffer.
func (e *Editor) searchOnce(re *regexp.Regexp, from Pos, dir int) (Pos, bool) {
	lines := e.Buf.Lines()
	for step := 0; step <= lines; step++ {
		l := ((from.Line+dir*step)%lines + lines) % lines
		text := e.Buf.LineString(l)
		matches := re.FindAllStringIndex(text, -1)
		if len(matches) == 0 {
			continue
		}
		runes := []rune(text)
		for i := range matches {
			if dir < 0 {
				i = len(matches) - 1 - i
			}
			col := len([]rune(text[:matches[i][0]]))
			switch {
			case step == 0 && dir > 0 && col <= from.Col:
				continue
			case step == 0 && dir < 0 && col >= from.Col:
				continue
			}
			_ = runes
			return Pos{l, col}, true
		}
	}
	return from, false
}

// Matches returns the ranges of the search pattern in a line, for the host to
// highlight. It is empty when highlighting is off.
func (e *Editor) Matches(line int) [][2]int {
	if e.Search == "" || !e.Highlight {
		return nil
	}
	re, err := regexp.Compile(e.Search)
	if err != nil {
		return nil
	}
	text := e.Buf.LineString(line)
	var out [][2]int
	for _, m := range re.FindAllStringIndex(text, -1) {
		out = append(out, [2]int{len([]rune(text[:m[0]])), len([]rune(text[:m[1]]))})
	}
	return out
}

// linkUnderCursor is the destination of the markdown or wiki link the cursor
// is in.
func (e *Editor) linkUnderCursor() (string, bool) {
	a, z, _, ok := e.textObject(false, "l")
	if !ok {
		return "", false
	}
	text := e.Buf.Slice(a, Pos{z.Line, z.Col + 1})
	if dest, _, found := strings.Cut(text, "|"); found {
		return strings.TrimSpace(dest), true
	}
	return strings.TrimSpace(text), true
}
