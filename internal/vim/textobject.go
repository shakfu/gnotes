package vim

import "strings"

// textObject returns the range of the object at the cursor, inclusive of both
// ends. around is set by "a" and includes the delimiters or trailing space.
func (e *Editor) textObject(around bool, kind string) (Pos, Pos, bool, bool) {
	cur := e.Cursor
	line := e.Buf.Line(cur.Line)
	switch kind {
	case "w", "W":
		if len(line) == 0 {
			return cur, cur, false, true
		}
		big := kind == "W"
		col := min(cur.Col, len(line)-1)
		c := classAt(line, col, big)
		a, z := col, col
		for a > 0 && classAt(line, a-1, big) == c {
			a--
		}
		for z+1 < len(line) && classAt(line, z+1, big) == c {
			z++
		}
		if around {
			end := z
			for end+1 < len(line) && isSpace(line[end+1]) {
				end++
			}
			if end == z { // no trailing space: take the leading space
				for a > 0 && isSpace(line[a-1]) {
					a--
				}
			}
			z = end
		}
		return Pos{cur.Line, a}, Pos{cur.Line, z}, false, true

	case "s":
		text := string(line)
		start, end := sentenceBounds(text, cur.Col)
		if !around {
			for end > start && isSpace([]rune(text)[end-1]) {
				end--
			}
		}
		return Pos{cur.Line, start}, Pos{cur.Line, max(start, end-1)}, false, true

	case "p":
		blank := func(l int) bool { return strings.TrimSpace(e.Buf.LineString(l)) == "" }
		here := blank(cur.Line)
		a, z := cur.Line, cur.Line
		for a > 0 && blank(a-1) == here {
			a--
		}
		for z+1 < e.Buf.Lines() && blank(z+1) == here {
			z++
		}
		if around {
			for z+1 < e.Buf.Lines() && blank(z+1) != here {
				z++
			}
		}
		return Pos{a, 0}, Pos{z, max(0, len(e.Buf.Line(z)))}, true, true

	case `"`, "'", "`":
		r := firstRune(kind)
		a, z, ok := pairOnLine(line, cur.Col, r, r)
		if !ok {
			return cur, cur, false, false
		}
		return trimPair(cur.Line, a, z, around)

	case "(", ")", "b", "[", "]", "{", "}", "B", "<", ">":
		open, close := bracketPair(kind)
		a, z, ok := e.enclosing(cur, open, close)
		if !ok {
			return cur, cur, false, false
		}
		if around {
			return a, z, false, true
		}
		return e.inside(a, z)

	case "l":
		return e.linkObject(around)
	}
	return cur, cur, false, false
}

func bracketPair(kind string) (rune, rune) {
	switch kind {
	case "(", ")", "b":
		return '(', ')'
	case "[", "]":
		return '[', ']'
	case "{", "}", "B":
		return '{', '}'
	}
	return '<', '>'
}

// trimPair turns a delimited range into the object: the delimiters for "a",
// what is between them for "i".
func trimPair(line, a, z int, around bool) (Pos, Pos, bool, bool) {
	if !around {
		if z-a < 2 {
			return Pos{line, a}, Pos{line, a}, false, false
		}
		return Pos{line, a + 1}, Pos{line, z - 1}, false, true
	}
	return Pos{line, a}, Pos{line, z}, false, true
}

// pairOnLine finds the quotes around or after a column.
func pairOnLine(line []rune, col int, open, close rune) (int, int, bool) {
	var starts []int
	for i, r := range line {
		if r == open && (i == 0 || line[i-1] != '\\') {
			starts = append(starts, i)
		}
	}
	for i := 0; i+1 < len(starts); i += 2 {
		a, z := starts[i], starts[i+1]
		if col <= z {
			return a, z, true
		}
	}
	return 0, 0, false
}

// enclosing finds the brackets around a position, across lines.
func (e *Editor) enclosing(cur Pos, open, close rune) (Pos, Pos, bool) {
	// Backwards to the unmatched opening bracket.
	depth := 0
	a := cur
	for {
		line := e.Buf.Line(a.Line)
		if a.Col < len(line) {
			switch line[a.Col] {
			case close:
				if a != cur {
					depth++
				}
			case open:
				if depth == 0 {
					goto forward
				}
				depth--
			}
		}
		if a.Col == 0 {
			if a.Line == 0 {
				return cur, cur, false
			}
			a = Pos{a.Line - 1, max(0, len(e.Buf.Line(a.Line-1))-1)}
			continue
		}
		a.Col--
	}
forward:
	depth = 0
	z := a
	for {
		line := e.Buf.Line(z.Line)
		if z.Col < len(line) {
			switch line[z.Col] {
			case open:
				depth++
			case close:
				depth--
				if depth == 0 {
					return a, z, true
				}
			}
		}
		z.Col++
		if z.Col >= len(line) {
			if z.Line+1 >= e.Buf.Lines() {
				return cur, cur, false
			}
			z = Pos{z.Line + 1, 0}
		}
	}
}

// inside is the range between two delimiters, dropping them.
func (e *Editor) inside(a, z Pos) (Pos, Pos, bool, bool) {
	// Brackets alone on their lines: the lines between them.
	if a.Line < z.Line && a.Col == len(e.Buf.Line(a.Line))-1 && z.Col == indentOf(e.Buf.Line(z.Line)) {
		if a.Line+1 > z.Line-1 {
			return a, z, false, false
		}
		return Pos{a.Line + 1, 0}, Pos{z.Line - 1, max(0, len(e.Buf.Line(z.Line-1)))}, true, true
	}
	start := Pos{a.Line, a.Col + 1}
	if start.Col > len(e.Buf.Line(a.Line))-1 && a.Line < z.Line {
		start = Pos{a.Line + 1, 0}
	}
	end := Pos{z.Line, z.Col - 1}
	if end.Col < 0 {
		if z.Line == 0 {
			return a, z, false, false
		}
		end = Pos{z.Line - 1, max(0, len(e.Buf.Line(z.Line-1))-1)}
	}
	if end.Before(start) {
		return start, start, false, false
	}
	return start, end, false, true
}

// linkObject is the markdown or wiki link at the cursor: its destination for
// "i", the whole link for "a".
func (e *Editor) linkObject(around bool) (Pos, Pos, bool, bool) {
	cur := e.Cursor
	line := e.Buf.LineString(cur.Line)
	runes := []rune(line)

	// A wiki link: [[target|label]].
	if a, z, ok := spanAround(line, cur.Col, "[[", "]]"); ok {
		if around {
			return Pos{cur.Line, a}, Pos{cur.Line, z - 1}, false, true
		}
		inner := string(runes[a+2 : z-2])
		end := a + 2 + len([]rune(inner))
		if i := strings.IndexByte(inner, '|'); i >= 0 {
			end = a + 2 + len([]rune(inner[:i]))
		}
		return Pos{cur.Line, a + 2}, Pos{cur.Line, max(a+2, end-1)}, false, true
	}

	// A markdown link: [label](destination).
	if a, z, ok := spanAround(line, cur.Col, "](", ")"); ok {
		if !around {
			return Pos{cur.Line, a + 2}, Pos{cur.Line, max(a+2, z-2)}, false, true
		}
		// Back to the opening bracket of the label.
		start := a
		for start > 0 && runes[start] != '[' {
			start--
		}
		if start > 0 && runes[start-1] == '!' {
			start--
		}
		return Pos{cur.Line, start}, Pos{cur.Line, z - 1}, false, true
	}
	return cur, cur, false, false
}

// spanAround finds the innermost open..close pair on a line that covers col,
// in runes, returning the start and the position just past the close.
func spanAround(line string, col int, open, close string) (int, int, bool) {
	runes := []rune(line)
	for a := min(col, len(runes)-1); a >= 0; a-- {
		if !strings.HasPrefix(string(runes[a:]), open) {
			continue
		}
		rest := string(runes[a+len([]rune(open)):])
		i := strings.Index(rest, close)
		if i < 0 {
			continue
		}
		z := a + len([]rune(open)) + len([]rune(rest[:i])) + len([]rune(close))
		if col < z {
			return a, z, true
		}
	}
	return 0, 0, false
}

// sentenceBounds is the sentence around a column, in runes.
func sentenceBounds(text string, col int) (int, int) {
	runes := []rune(text)
	end := func(i int) bool {
		return i > 0 && strings.ContainsRune(".!?", runes[i-1]) && (i >= len(runes) || isSpace(runes[i]))
	}
	start := 0
	for i := 1; i <= min(col, len(runes)); i++ {
		if end(i) {
			start = i
			for start < len(runes) && isSpace(runes[start]) {
				start++
			}
		}
	}
	stop := len(runes)
	for i := start + 1; i <= len(runes); i++ {
		if end(i) {
			stop = i
			for stop < len(runes) && isSpace(runes[stop]) {
				stop++
			}
			break
		}
	}
	return start, stop
}
