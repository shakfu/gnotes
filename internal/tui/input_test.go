package tui

import (
	"strings"
	"testing"
)

func TestInputEditing(t *testing.T) {
	var in input
	in.set("hello world")

	if in.String() != "hello world" {
		t.Fatalf("set = %q", in.String())
	}

	in.backspace()
	if in.String() != "hello worl" {
		t.Fatalf("backspace = %q", in.String())
	}

	in.deleteWord()
	if in.String() != "hello " {
		t.Fatalf("deleteWord = %q", in.String())
	}

	in.home()
	in.deleteForward()
	if in.String() != "ello " {
		t.Fatalf("deleteForward at the start = %q", in.String())
	}

	in.end()
	in.insertString("nd")
	if in.String() != "ello nd" {
		t.Fatalf("insertString = %q", in.String())
	}

	in.deleteToStart()
	if !in.empty() {
		t.Fatalf("deleteToStart left %q", in.String())
	}
}

// The cursor indexes characters, not bytes, so a multi-byte character takes
// one keypress to cross.
func TestInputHandlesMultiByteCharacters(t *testing.T) {
	var in input
	in.set("naïve café")

	in.backspace()
	if got := in.String(); got != "naïve caf" {
		t.Fatalf("backspace over a multi-byte string gave %q", got)
	}

	in.home()
	for i := 0; i < 3; i++ {
		in.right()
	}
	in.insert('X')
	if got := in.String(); got != "naïXve caf" {
		t.Fatalf("insert after a multi-byte character gave %q", got)
	}
}

func TestInputCursorStopsAtBothEnds(t *testing.T) {
	var in input
	in.set("ab")

	for i := 0; i < 10; i++ {
		in.left()
	}
	if in.cursor != 0 {
		t.Fatalf("cursor = %d, want 0", in.cursor)
	}
	in.backspace() // must be a no-op, not a panic
	if in.String() != "ab" {
		t.Fatalf("backspace at the start changed the text: %q", in.String())
	}

	for i := 0; i < 10; i++ {
		in.right()
	}
	if in.cursor != 2 {
		t.Fatalf("cursor = %d, want 2", in.cursor)
	}
	in.deleteForward() // also a no-op
	if in.String() != "ab" {
		t.Fatalf("deleteForward at the end changed the text: %q", in.String())
	}
}

// A line longer than the field must scroll to keep the cursor visible.
func TestInputRenderFollowsTheCursor(t *testing.T) {
	var in input
	in.set(strings.Repeat("x", 100) + "END")

	got := in.render(":", 20)
	if !strings.Contains(got, "END") {
		t.Fatalf("the render does not show the cursor's neighbourhood: %q", got)
	}
	if len([]rune(got)) > 21 {
		t.Fatalf("the render is %d wide, want at most 21: %q", len([]rune(got)), got)
	}
}

func TestInputShowsTheCursorAndTheAnswer(t *testing.T) {
	var in input
	in.set("hello")
	in.home()
	if out := in.render("> ", 40); !strings.Contains(out, "\x1b[7mh\x1b[27m") {
		t.Fatalf("no cursor drawn at the start: %q", out)
	}

	in.set("yes")
	label := "move " + strings.Repeat("very long title ", 5) + "to? y/n: "
	if out := stripANSI(in.render(label, 50)); !strings.Contains(out, "yes") {
		t.Fatalf("a long label hid the answer: %q", out)
	}
}

func TestTruncateAndPad(t *testing.T) {
	if got := truncate("hello", 10); got != "hello" {
		t.Errorf("truncate short = %q", got)
	}
	if got := truncate("hello world", 8); len([]rune(got)) != 8 {
		t.Errorf("truncate = %q, want 8 runes", got)
	}
	if got := truncate("hello", 0); got != "" {
		t.Errorf("truncate to zero = %q", got)
	}
	if got := pad("ab", 5); got != "ab   " {
		t.Errorf("pad = %q", got)
	}
	if got := pad("abcdef", 3); got != "abcdef" {
		t.Errorf("pad must not shorten: %q", got)
	}
}

// stripANSI removes escape sequences so a rendered line can be measured in
// visible columns.
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			i++
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
