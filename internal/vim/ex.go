package vim

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

func (e *Editor) commandKey(k string) {
	switch k {
	case "esc", "ctrl+[":
		e.Mode, e.cmdline, e.waiting = Normal, nil, pendingOp{}
		e.Message = ""
	case "enter":
		text := string(e.cmdline)
		kind := e.cmdKind
		e.Mode, e.cmdline = Normal, nil
		if text != "" {
			e.history = append(e.history, string(kind)+text)
		}
		if kind == ':' {
			e.exCommand(text)
			return
		}
		e.runSearch(kind, text)
	case "backspace":
		if len(e.cmdline) == 0 {
			e.Mode = Normal
			return
		}
		e.cmdline = e.cmdline[:len(e.cmdline)-1]
	case "ctrl+u":
		e.cmdline = nil
	case "ctrl+w":
		text := strings.TrimRight(string(e.cmdline), " ")
		if i := strings.LastIndexByte(text, ' '); i >= 0 {
			e.cmdline = []rune(text[:i+1])
		} else {
			e.cmdline = nil
		}
	case "up", "down":
		e.recall(k == "up")
	case "space":
		e.cmdline = append(e.cmdline, ' ')
	case "tab":
	default:
		if len([]rune(k)) == 1 {
			e.cmdline = append(e.cmdline, firstRune(k))
		}
	}
}

// recall walks the command history for lines of the same kind.
func (e *Editor) recall(back bool) {
	prefix := string(e.cmdKind)
	for i := len(e.history) - 1; i >= 0; i-- {
		if strings.HasPrefix(e.history[i], prefix) && string(e.cmdline) != e.history[i][1:] {
			if back {
				e.cmdline = []rune(e.history[i][1:])
				return
			}
		}
	}
	if !back {
		e.cmdline = nil
	}
}

// runSearch handles / and ?.
func (e *Editor) runSearch(kind rune, pattern string) {
	dir := 1
	if kind == '?' {
		dir = -1
	}
	if pattern == "" {
		pattern = e.Search
	}
	if pattern == "" {
		e.fail("no previous pattern")
		return
	}
	e.Search, e.searchDir, e.Highlight = pattern, dir, true
	p, ok := e.searchFrom(pattern, dir, 1)
	if !ok {
		e.waiting = pendingOp{}
		e.fail("pattern not found: " + pattern)
		return
	}
	if op := e.waiting; op.active {
		// The operator that started the search takes up to the match.
		e.waiting = pendingOp{}
		e.Cursor = op.from
		a, z := sorted(op.from, p)
		if z.Col > 0 {
			z.Col-- // a search is an exclusive motion
		} else if z.Line > a.Line {
			z = Pos{z.Line - 1, max(0, len(e.Buf.Line(z.Line-1)))}
		}
		e.applyOperator(op.op, a, z, false, op.reg)
		return
	}
	e.Cursor = e.Buf.clamp(p, false)
	e.wantCol = e.Cursor.Col
}

// exRange is the lines a command applies to.
type exRange struct {
	from, to int
	given    bool
}

// exCommand runs one ':' command.
func (e *Editor) exCommand(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	rng, rest := e.parseRange(text)
	if rest == "" {
		// A bare range moves to its last line.
		e.Cursor = e.Buf.clamp(Pos{rng.to, 0}, false)
		e.Cursor.Col = indentOf(e.Buf.Line(e.Cursor.Line))
		return
	}

	name := rest
	arg := ""
	if i := strings.IndexAny(rest, " /"); i > 0 {
		name, arg = rest[:i], strings.TrimSpace(rest[i:])
		if rest[i] == '/' {
			name, arg = rest[:i], rest[i:]
		}
	}
	force := strings.HasSuffix(name, "!")
	name = strings.TrimSuffix(name, "!")

	switch name {
	case "w", "write":
		e.save(force, false)
	case "wq", "x", "xit":
		e.save(force, true)
	case "q", "quit":
		e.quit(force)
	case "e", "edit":
		if e.Hooks.Reload == nil {
			e.fail("reloading is not available here")
			return
		}
		if e.Dirty && !force {
			e.fail("the page has unsaved changes; :e! reloads and loses them")
			return
		}
		if err := e.Hooks.Reload(); err != nil {
			e.fail(err.Error())
			return
		}
		e.setMessage("reloaded")
	case "s", "substitute":
		e.substitute(rng, arg)
	case "noh", "nohl", "nohlsearch":
		e.Highlight = false
	default:
		if e.Hooks.Command != nil {
			if handled, err := e.Hooks.Command(name, arg); handled {
				if err != nil {
					e.fail(err.Error())
				}
				return
			}
		}
		e.fail("not a command: " + name)
	}
}

func (e *Editor) save(force, quit bool) {
	if e.Hooks.Save == nil {
		e.fail("saving is not available here")
		return
	}
	if err := e.Hooks.Save(force); err != nil {
		e.fail(err.Error())
		return
	}
	e.Dirty = false
	e.setMessage("written")
	if quit {
		e.quit(force)
	}
}

func (e *Editor) quit(force bool) {
	if e.Hooks.Quit == nil {
		e.fail("quitting is not available here")
		return
	}
	if e.Dirty && !force {
		e.fail("the page has unsaved changes; :w writes them, :q! discards them")
		return
	}
	if err := e.Hooks.Quit(force); err != nil {
		e.fail(err.Error())
	}
}

// parseRange reads a leading range: %, a visual selection, line numbers, . or
// $. Without one it is the cursor's line.
func (e *Editor) parseRange(text string) (exRange, string) {
	here := e.Cursor.Line
	rng := exRange{from: here, to: here}
	rest := text

	if strings.HasPrefix(rest, "%") {
		return exRange{0, e.Buf.Lines() - 1, true}, strings.TrimSpace(rest[1:])
	}
	if strings.HasPrefix(rest, "'<,'>") {
		a, z := sorted(e.visualStart, e.Cursor)
		return exRange{a.Line, z.Line, true}, strings.TrimSpace(rest[5:])
	}
	one := func(s string) (int, string, bool) {
		switch {
		case strings.HasPrefix(s, "."):
			return here, s[1:], true
		case strings.HasPrefix(s, "$"):
			return e.Buf.Lines() - 1, s[1:], true
		}
		i := 0
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		if i == 0 {
			return 0, s, false
		}
		n, _ := strconv.Atoi(s[:i])
		return max(0, n-1), s[i:], true
	}
	if from, after, ok := one(rest); ok {
		rng = exRange{from, from, true}
		rest = after
		if strings.HasPrefix(rest, ",") {
			if to, after, ok := one(rest[1:]); ok {
				rng.to = to
				rest = after
			}
		}
	}
	return rng, strings.TrimSpace(rest)
}

// substitute runs :s/pattern/replacement/flags over a range.
func (e *Editor) substitute(rng exRange, arg string) {
	if arg == "" {
		e.fail("usage: :s/pattern/replacement/[g]")
		return
	}
	sep := arg[0]
	parts := splitUnescaped(arg[1:], rune(sep))
	if len(parts) < 2 {
		e.fail("usage: :s/pattern/replacement/[g]")
		return
	}
	pattern, replacement := parts[0], parts[1]
	flags := ""
	if len(parts) > 2 {
		flags = parts[2]
	}
	if pattern == "" {
		pattern = e.Search
	}
	if strings.Contains(flags, "i") {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		e.fail("bad pattern: " + err.Error())
		return
	}
	all := strings.Contains(flags, "g")
	repl := goReplacement(replacement)

	changed, lines := 0, 0
	e.change(func() {
		for l := rng.from; l <= rng.to && l < e.Buf.Lines(); l++ {
			text := e.Buf.LineString(l)
			matches := re.FindAllStringIndex(text, -1)
			if len(matches) == 0 {
				continue
			}
			if !all {
				matches = matches[:1]
			}
			out := text[:0]
			last := 0
			for _, m := range matches {
				out += text[last:m[0]] + re.ReplaceAllString(text[m[0]:m[1]], repl)
				last = m[1]
				changed++
			}
			out += text[last:]
			lines++
			e.Buf.Replace(Pos{l, 0}, Pos{l, len([]rune(text))}, out)
			e.Cursor = Pos{l, 0}
		}
	})
	if changed == 0 {
		e.fail("pattern not found: " + pattern)
		return
	}
	e.Search, e.Highlight = pattern, true
	e.Cursor = e.Buf.clamp(e.Cursor, false)
	e.setMessage(fmt.Sprintf("%s on %s", count(changed, "substitution"), count(lines, "line")))
}

// splitUnescaped splits on a separator that is not backslash-escaped.
func splitUnescaped(s string, sep rune) []string {
	var out []string
	var cur strings.Builder
	escaped := false
	for _, r := range s {
		switch {
		case escaped:
			if r != sep {
				cur.WriteRune('\\')
			}
			cur.WriteRune(r)
			escaped = false
		case r == '\\':
			escaped = true
		case r == sep:
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	out = append(out, cur.String())
	return out
}

// goReplacement turns vim's \1 and & into Go's ${1} and ${0}.
func goReplacement(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '$':
			b.WriteString("$$")
		case s[i] == '&':
			b.WriteString("${0}")
		case s[i] == '\\' && i+1 < len(s) && s[i+1] >= '0' && s[i+1] <= '9':
			b.WriteString("${" + string(s[i+1]) + "}")
			i++
		case s[i] == '\\' && i+1 < len(s):
			switch s[i+1] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case '&':
				b.WriteByte('&')
			default:
				b.WriteByte(s[i+1])
			}
			i++
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}
