package display

import "testing"

func TestLineReplacesControlCharacters(t *testing.T) {
	cases := map[string]string{
		"plain title":             "plain title",
		"caf\u00e9 \u65e5\u672c":  "caf\u00e9 \u65e5\u672c",
		"evil\x1b]0;PWNED\x07":    "evil?]0;PWNED?",
		"clear\x1b[2J":            "clear?[2J",
		"two\nrows":               "two?rows",
		"tab\x09here":             "tab here",
		"c1\u009b31m":             "c1?31m",
		"del\x7f":                 "del?",
		"carriage\x0dreturn":      "carriage?return",
		"nul\x00byte":             "nul?byte",
		"osc52\x1b]52;c;aGk=\x07": "osc52?]52;c;aGk=?",
	}
	for in, want := range cases {
		if got := Line(in); got != want {
			t.Errorf("Line(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBlockKeepsLayout(t *testing.T) {
	in := "line one\n\x09indented\x1b[2J\x0d\n"
	want := "line one\n\x09indented?[2J?\n"
	if got := Block(in); got != want {
		t.Errorf("Block = %q, want %q", got, want)
	}
}
