package vim

import (
	"strings"
	"testing"
)

// edit runs keys over text and returns the editor. The cursor starts at the
// first occurrence of mark, which is removed from the text; without a mark it
// starts at the beginning.
func edit(t *testing.T, text string, keys []string, mark string) *Editor {
	t.Helper()
	cursor := Pos{}
	if mark != "" {
		i := strings.Index(text, mark)
		if i < 0 {
			t.Fatalf("mark %q is not in the text", mark)
		}
		before := text[:i]
		cursor = Pos{strings.Count(before, "\n"), len([]rune(before[strings.LastIndexByte(before, '\n')+1:]))}
		text = before + text[i+len(mark):]
	}
	e := New(text)
	e.SetCursor(cursor)
	e.Keys(keys...)
	return e
}

// keys splits a string into keypresses, with <name> for a named key.
func keys(s string) []string {
	var out []string
	for i := 0; i < len(s); {
		if s[i] == '<' {
			j := strings.IndexByte(s[i:], '>')
			if j > 0 {
				out = append(out, s[i+1:i+j])
				i += j + 1
				continue
			}
		}
		r := []rune(s[i:])[0]
		out = append(out, string(r))
		i += len(string(r))
	}
	return out
}

// A case: the text, where the cursor starts (@), the keys, and what results.
type testCase struct {
	name  string
	text  string
	keys  string
	want  string
	at    Pos
	check func(t *testing.T, e *Editor)
}

func runCases(t *testing.T, cases []testCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := edit(t, c.text, keys(c.keys), "@")
			if c.want != "" && e.Text() != c.want {
				t.Errorf("text =\n%q\nwant\n%q", e.Text(), c.want)
			}
			if (c.at != Pos{}) && e.Cursor != c.at {
				t.Errorf("cursor = %+v, want %+v", e.Cursor, c.at)
			}
			if c.check != nil {
				c.check(t, e)
			}
		})
	}
}

func TestMotions(t *testing.T) {
	const text = "@alpha beta, gamma\nsecond line here\n\nlast\n"
	runCases(t, []testCase{
		{name: "w", text: text, keys: "w", at: Pos{0, 6}},
		{name: "3w", text: text, keys: "3w", at: Pos{0, 12}},
		{name: "w over a line end", text: text, keys: "4w", at: Pos{1, 0}},
		{name: "e", text: text, keys: "e", at: Pos{0, 4}},
		{name: "b from the middle", text: "alpha be@ta\n", keys: "b", at: Pos{0, 6}},
		{name: "ge", text: "alpha be@ta\n", keys: "ge", at: Pos{0, 4}},
		{name: "dollar", text: text, keys: "$", at: Pos{0, 16}},
		{name: "caret", text: "   inde@nted\n", keys: "^", at: Pos{0, 3}},
		{name: "zero", text: "   inde@nted\n", keys: "0", at: Pos{0, 0}},
		{name: "G", text: text, keys: "G", at: Pos{3, 0}},
		{name: "2G", text: text, keys: "2G", at: Pos{1, 0}},
		{name: "gg from below", text: "one\ntw@o\n", keys: "gg", at: Pos{0, 0}},
		{name: "j keeps the column", text: "alpha be@ta\nxy\nlonger line\n", keys: "jj", at: Pos{2, 8}},
		{name: "f", text: text, keys: "fg", at: Pos{0, 12}},
		{name: "t then ;", text: "a.b.c@.d\n", keys: "0t.;", at: Pos{0, 2}},
		{name: "F", text: "a.b.c@.d\n", keys: "F.", at: Pos{0, 3}},
		{name: "percent", text: "call(a@rg, x)\n", keys: "%", at: Pos{0, 4}},
		{name: "percent back", text: "call(arg, x@)\n", keys: "%", at: Pos{0, 4}},
		{name: "paragraph", text: "one\ntw@o\n\nfour\n", keys: "}", at: Pos{2, 0}},
	})
}

func TestOperators(t *testing.T) {
	runCases(t, []testCase{
		{name: "dw", text: "@alpha beta gamma\n", keys: "dw", want: "beta gamma\n"},
		{name: "d2w", text: "@alpha beta gamma\n", keys: "d2w", want: "gamma\n"},
		{name: "dw stops at the line end", text: "alpha @beta\nnext\n", keys: "dw", want: "alpha \nnext\n"},
		{name: "de", text: "@alpha beta\n", keys: "de", want: " beta\n"},
		{name: "db", text: "alpha be@ta\n", keys: "db", want: "alpha ta\n"},
		{name: "dd", text: "one\n@two\nthree\n", keys: "dd", want: "one\nthree\n", at: Pos{1, 0}},
		{name: "2dd", text: "one\n@two\nthree\n", keys: "2dd", want: "one\n"},
		{name: "dd on the last line", text: "one\n@two\n", keys: "dd", want: "one\n", at: Pos{0, 0}},
		{name: "d$", text: "alpha @beta gamma\n", keys: "d$", want: "alpha \n"},
		{name: "dfx", text: "@alpha, beta\n", keys: "df,", want: " beta\n"},
		{name: "cw becomes ce", text: "@alpha beta\n", keys: "cwxy<esc>", want: "xy beta\n"},
		{name: "cc keeps the line", text: "one\n@two\n", keys: "ccnew<esc>", want: "one\nnew\n"},
		{name: "yy and p", text: "@one\ntwo\n", keys: "yyp", want: "one\none\ntwo\n", at: Pos{1, 0}},
		{name: "yw and P", text: "@one two\n", keys: "ywP", want: "one one two\n"},
		{name: "x", text: "a@bc\n", keys: "x", want: "ac\n"},
		{name: "3x", text: "a@bcde\n", keys: "3x", want: "ae\n"},
		{name: "X", text: "ab@c\n", keys: "X", want: "ac\n"},
		{name: "D", text: "alpha @beta\n", keys: "D", want: "alpha \n"},
		{name: "C", text: "alpha @beta\n", keys: "Cnew<esc>", want: "alpha new\n"},
		{name: "J", text: "@one\ntwo\n", keys: "J", want: "one two\n", at: Pos{0, 3}},
		{name: "3J", text: "@one\ntwo\nthree\n", keys: "3J", want: "one two three\n"},
		{name: "r", text: "@abc\n", keys: "rx", want: "xbc\n"},
		{name: "3r", text: "@abc\n", keys: "3rx", want: "xxx\n"},
		{name: "tilde", text: "@abc\n", keys: "2~", want: "ABc\n", at: Pos{0, 2}},
		{name: "shift right", text: "@one\ntwo\n", keys: ">>", want: "  one\ntwo\n"},
		{name: "shift left", text: "    @one\n", keys: "<<", want: "  one\n"},
		{name: "2>>", text: "@one\ntwo\nthree\n", keys: "2>>", want: "  one\n  two\nthree\n"},
		{name: "gUiw", text: "one tw@o three\n", keys: "gUiw", want: "one TWO three\n"},
		{name: "guiw", text: "@One\n", keys: "guiw", want: "one\n"},
		{name: "g~ over a word", text: "@aBc def\n", keys: "g~w", want: "AbC def\n"},
	})
}

func TestTextObjects(t *testing.T) {
	runCases(t, []testCase{
		{name: "diw", text: "one tw@o three\n", keys: "diw", want: "one  three\n"},
		{name: "daw", text: "one tw@o three\n", keys: "daw", want: "one three\n"},
		{name: "ci quotes", text: `say "he@llo there" now` + "\n", keys: `ci"bye<esc>`, want: "say \"bye\" now\n"},
		{name: "ca quotes", text: `say "he@llo" now` + "\n", keys: `da"`, want: "say  now\n"},
		{name: "di paren", text: "call(a@rg, x) end\n", keys: "di(", want: "call() end\n"},
		{name: "da paren", text: "call(a@rg, x) end\n", keys: "da(", want: "call end\n"},
		{name: "dib across lines", text: "f(\n  a@rg,\n)\n", keys: "dib", want: "f(\n)\n"},
		{name: "di bracket", text: "see [la@bel](url)\n", keys: "di[", want: "see [](url)\n"},
		{name: "dip", text: "one\ntw@o\n\nthree\n", keys: "dip", want: "\nthree\n"},
		{name: "dap", text: "one\ntw@o\n\nthree\n", keys: "dap", want: "three\n"},
		{name: "dis", text: "First one. Sec@ond two. Third.\n", keys: "dis", want: "First one.  Third.\n"},
		{name: "cil on a wiki link", text: "see [[Design sk@etch]] now\n", keys: "cilOther<esc>", want: "see [[Other]] now\n"},
		{name: "cil keeps a label", text: "see [[Design@|the sketch]]\n", keys: "cilOther<esc>", want: "see [[Other|the sketch]]\n"},
		{name: "cil on a markdown link", text: "see [label](ol@d.md) now\n", keys: "cilnew.md<esc>", want: "see [label](new.md) now\n"},
		{name: "dal on a markdown link", text: "see [label](ol@d.md) now\n", keys: "dal", want: "see  now\n"},
		{name: "dal on an image", text: "see ![alt](p@ic.png)\n", keys: "dal", want: "see \n"},
	})
}

func TestInsertMode(t *testing.T) {
	runCases(t, []testCase{
		{name: "i", text: "b@c\n", keys: "ia<esc>", want: "bac\n", at: Pos{0, 1}},
		{name: "a", text: "@ab\n", keys: "aX<esc>", want: "aXb\n"},
		{name: "A", text: "@ab\n", keys: "AX<esc>", want: "abX\n"},
		{name: "I", text: "  te@xt\n", keys: "IX<esc>", want: "  Xtext\n"},
		{name: "o", text: "@one\n", keys: "otwo<esc>", want: "one\ntwo\n", at: Pos{1, 2}},
		{name: "O", text: "on@e\n", keys: "Ozero<esc>", want: "zero\none\n"},
		{name: "backspace", text: "ab@c\n", keys: "ix<backspace><backspace><esc>", want: "ac\n"},
		{name: "ctrl-w", text: "@one two\n", keys: "Athree<ctrl+w><esc>", want: "one \n"},
		{name: "tab inserts spaces", text: "@x\n", keys: "i<tab><esc>", want: "  x\n"},
		{name: "enter splits", text: "ab@cd\n", keys: "i<enter><esc>", want: "ab\ncd\n"},
		{name: "list continues", text: "- one@\n", keys: "A<enter>two<esc>", want: "- one\n- two\n"},
		{name: "numbered list continues", text: "3. one@\n", keys: "A<enter>two<esc>", want: "3. one\n4. two\n"},
		{name: "checklist continues unticked", text: "- [x] one@\n", keys: "A<enter>two<esc>", want: "- [x] one\n- [ ] two\n"},
		{name: "quote continues", text: "> one@\n", keys: "A<enter>two<esc>", want: "> one\n> two\n"},
		{name: "empty item ends the list", text: "- one\n- @\n", keys: "A<enter>text<esc>", want: "- one\n\ntext\n"},
		{name: "indented list keeps the indent", text: "  - one@\n", keys: "A<enter>two<esc>", want: "  - one\n  - two\n"},
		{name: "o continues a list", text: "- on@e\n", keys: "otwo<esc>", want: "- one\n- two\n"},
		{name: "R overwrites", text: "@abcd\n", keys: "RXY<esc>", want: "XYcd\n"},
		{name: "R backspace restores", text: "@abcd\n", keys: "RXY<backspace><backspace><esc>", want: "abcd\n"},
		{name: "checkbox toggles", text: "- [ ] tid@y\n", keys: "<ctrl+@>", want: "- [x] tidy\n"},
		{name: "checkbox toggles back", text: "- [x] tid@y\n", keys: "<ctrl+@>", want: "- [ ] tidy\n"},
	})
}

func TestVisual(t *testing.T) {
	runCases(t, []testCase{
		{name: "v d", text: "@abcdef\n", keys: "vlld", want: "def\n"},
		{name: "v y and p", text: "@abc\n", keys: "vly$p", want: "abcab\n"},
		{name: "V d", text: "one\n@two\nthree\n", keys: "Vd", want: "one\nthree\n"},
		{name: "V j d", text: "one\n@two\nthree\nfour\n", keys: "Vjd", want: "one\nfour\n"},
		{name: "V >", text: "@one\ntwo\n", keys: "Vj>", want: "  one\n  two\n"},
		{name: "v U", text: "@abc def\n", keys: "vegU", want: "ABC def\n"},
		{name: "v o swaps ends", text: "ab@cdef\n", keys: "vllohd", want: "af\n"},
		{name: "v iw", text: "one tw@o three\n", keys: "viwd", want: "one  three\n"},
		{name: "esc leaves visual", text: "@abc\n", keys: "vl<esc>x", want: "ac\n"},
	})
}

func TestRepeatUndoAndRegisters(t *testing.T) {
	runCases(t, []testCase{
		{name: "dot repeats a delete", text: "@a b c d\n", keys: "dw..", want: "d\n"},
		{name: "dot repeats an insert", text: "@one\ntwo\n", keys: "IX<esc>j0.", want: "Xone\nXtwo\n"},
		{name: "dot takes a new count", text: "@abcdef\n", keys: "x3.", want: "ef\n"},
		{name: "u undoes", text: "@abc\n", keys: "xxu", want: "bc\n"},
		{name: "u then redo", text: "@abc\n", keys: "xxu<ctrl+r>", want: "c\n"},
		{name: "u undoes a whole insert", text: "@abc\n", keys: "ixyz<esc>u", want: "abc\n"},
		{name: "named register", text: "@one\ntwo\n", keys: `"ayyj"ap`, want: "one\ntwo\none\n"},
		{name: "delete then paste", text: "@one\ntwo\n", keys: "ddp", want: "two\none\n"},
		{name: "black hole keeps the register", text: "@one two\n", keys: `yw"_dwP`, want: "one two\n"},
	})
}

func TestSearchAndEx(t *testing.T) {
	const text = "@alpha\nbeta\ngamma beta\n"
	runCases(t, []testCase{
		{name: "search forward", text: text, keys: "/beta<enter>", at: Pos{1, 0}},
		{name: "search then n", text: text, keys: "/beta<enter>n", at: Pos{2, 6}},
		{name: "search wraps", text: text, keys: "/alpha<enter>n", at: Pos{0, 0}},
		{name: "backward search", text: "one\ntwo\nth@ree\n", keys: "?two<enter>", at: Pos{1, 0}},
		{name: "star", text: "beta x\nbe@ta\n", keys: "*", at: Pos{0, 0}},
		{name: "d then search", text: "@one two three\n", keys: "d/three<enter>", want: "three\n"},
		{name: "substitute on the line", text: "@a a a\n", keys: ":s/a/b<enter>", want: "b a a\n"},
		{name: "substitute all", text: "@a a a\n", keys: ":s/a/b/g<enter>", want: "b b b\n"},
		{name: "substitute the file", text: "@a\na\n", keys: ":%s/a/b/g<enter>", want: "b\nb\n"},
		{name: "substitute a group", text: "@one two\n", keys: `:s/(\w+) (\w+)/\2 \1<enter>`, want: "two one\n"},
		{name: "substitute a visual range", text: "@a\na\na\n", keys: "Vj:s/a/b<enter>", want: "b\nb\na\n"},
		{name: "goto line", text: "@one\ntwo\nthree\n", keys: ":2<enter>", at: Pos{1, 0}},
		{name: "unknown command", text: "@x\n", keys: ":nope<enter>", check: func(t *testing.T, e *Editor) {
			if !e.Err || !strings.Contains(e.Message, "not a command") {
				t.Errorf("message = %q", e.Message)
			}
		}},
	})
}

func TestHooks(t *testing.T) {
	e := New("text\n")
	var saved, quit, reloaded, followed, back bool
	var clip string
	e.Hooks = Hooks{
		Save:      func(force bool) error { saved = true; return nil },
		Quit:      func(force bool) error { quit = true; return nil },
		Reload:    func() error { reloaded = true; return nil },
		Follow:    func() error { followed = true; return nil },
		Back:      func() error { back = true; return nil },
		Clipboard: func(text string) { clip = text },
		Command: func(name, arg string) (bool, error) {
			return name == "preview", nil
		},
	}
	e.Keys(keys(":w<enter>")...)
	if !saved || e.Dirty {
		t.Errorf("save = %v, dirty = %v", saved, e.Dirty)
	}
	e.Keys(keys("ix<esc>:q<enter>")...)
	if quit || !strings.Contains(e.Message, "unsaved changes") {
		t.Errorf("quit with unsaved changes: %v %q", quit, e.Message)
	}
	e.Keys(keys(":q!<enter>")...)
	if !quit {
		t.Error("q! did not quit")
	}
	e.Keys(keys(":e!<enter>")...)
	if !reloaded {
		t.Error("e! did not reload")
	}
	e.Keys(keys("<ctrl+]>")...)
	if !followed {
		t.Error("ctrl-] did not follow")
	}
	e.Keys(keys("<ctrl+o>")...)
	if !back {
		t.Error("ctrl-o did not go back")
	}
	e.Keys(keys(`"+yy`)...)
	if clip != "xtext\n" {
		t.Errorf("clipboard = %q", clip)
	}
	e.Keys(keys(":preview<enter>")...)
	if e.Err {
		t.Errorf("preview: %q", e.Message)
	}

	// Without a hook the command says so rather than doing nothing.
	bare := New("x\n")
	bare.Keys(keys(":w<enter>")...)
	if !bare.Err || !strings.Contains(bare.Message, "not available") {
		t.Errorf("save without a hook: %q", bare.Message)
	}
}

func TestWrapAndDisplayLines(t *testing.T) {
	line := []rune("alpha beta gamma delta")
	if rows := Wrap(line, 12); len(rows) != 2 || rows[1] != 11 {
		t.Fatalf("Wrap = %v", rows)
	}
	e := New("alpha beta gamma delta\nnext\n")
	e.Width = 12
	e.Keys("g", "j")
	if e.Cursor != (Pos{0, 11}) {
		t.Fatalf("gj = %+v", e.Cursor)
	}
	e.Keys("g", "k")
	if e.Cursor != (Pos{0, 0}) {
		t.Fatalf("gk = %+v", e.Cursor)
	}
}

func TestScrollingKeepsTheCursorVisible(t *testing.T) {
	var lines []string
	for i := 0; i < 100; i++ {
		lines = append(lines, "line")
	}
	e := New(strings.Join(lines, "\n") + "\n")
	e.Height = 10
	e.Keys("G")
	if e.Cursor.Line != 99 || e.Top != 90 {
		t.Fatalf("G: cursor %d, top %d", e.Cursor.Line, e.Top)
	}
	e.Keys("g", "g")
	if e.Top != 0 {
		t.Fatalf("gg: top %d", e.Top)
	}
	e.Keys("z", "z")
	e.Keys("5", "0", "G", "z", "z")
	if e.Top != 45 {
		t.Fatalf("zz: top %d", e.Top)
	}
	e.Keys("z", "t")
	if e.Top != 49 {
		t.Fatalf("zt: top %d", e.Top)
	}
}

func TestPendingKeysAndCounts(t *testing.T) {
	e := New("alpha beta gamma\n")
	e.Keys("2")
	if e.Pending() != "2" {
		t.Fatalf("pending = %q", e.Pending())
	}
	e.Keys("d")
	if e.Pending() != "2d" {
		t.Fatalf("pending = %q", e.Pending())
	}
	e.Keys("w")
	if e.Text() != "gamma\n" || e.Pending() != "" {
		t.Fatalf("2dw = %q, pending %q", e.Text(), e.Pending())
	}
	// An unknown command is dropped rather than left pending.
	e.Keys("Q")
	if e.Pending() != "" {
		t.Fatalf("after an unknown command: %q", e.Pending())
	}
}

func TestDirtyAndChangedHook(t *testing.T) {
	e := New("one\n")
	changes := 0
	e.Hooks.Changed = func() { changes++ }
	e.Keys(keys("x")...)
	if !e.Dirty || changes != 1 {
		t.Fatalf("dirty %v, changes %d", e.Dirty, changes)
	}
	e.Keys(keys("ihello<esc>")...)
	if changes != 6 {
		t.Fatalf("changes = %d, want one per typed key", changes)
	}
}
