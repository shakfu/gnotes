package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// truncate shortens a string to n display columns, ending in an ellipsis when
// it had to cut. Columns, not runes or bytes: a wide character takes two, and
// escape sequences take none.
func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if ansi.StringWidth(s) <= n {
		return s
	}
	if n == 1 {
		return "."
	}
	return ansi.Truncate(s, n, "…")
}

// pad extends a string to n display columns.
func pad(s string, n int) string {
	if d := n - ansi.StringWidth(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

// clip shortens a possibly styled string to n visible columns. A reset is
// appended when it cuts, so a colour cut short does not bleed into the rest of
// the line.
func clip(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if ansi.StringWidth(s) <= n {
		return s
	}
	return ansi.Truncate(s, n, "…") + "\x1b[0m"
}
