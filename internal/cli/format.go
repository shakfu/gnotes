package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/shakfu/gwiki/internal/display"
)

// ANSI styles, applied only when the output is a terminal.
const (
	ansiReset  = "\x1b[0m"
	ansiDim    = "\x1b[2m"
	ansiBold   = "\x1b[1m"
	ansiRed    = "\x1b[31m"
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
	ansiBlue   = "\x1b[34m"
	ansiCyan   = "\x1b[36m"
)

// style wraps s in an ANSI code when colour is on.
//
// s is made safe for a terminal first, colour or not. Nearly everything this
// program prints from pages passes through here or through table.write, so
// those two places keep a page title from sending escape sequences.
func (a *App) style(code, s string) string {
	s = display.Line(s)
	if !a.Color || s == "" {
		return s
	}
	return code + s + ansiReset
}

// table accumulates rows and prints them in aligned columns.
//
// The width of a column is the widest cell in it, measured in runes rather
// than bytes so that a title with an accented character does not throw the
// alignment off.
type table struct {
	rows [][]string
	// styled holds the display form of each cell when it differs from the
	// plain form, so that ANSI codes never count towards a column's width.
	styled [][]string
}

func (t *table) add(cells ...string) {
	t.rows = append(t.rows, cells)
	t.styled = append(t.styled, nil)
}

// addStyled adds a row whose cells are measured as plain but printed as
// styled. Both slices must be the same length.
func (t *table) addStyled(plain, styled []string) {
	t.rows = append(t.rows, plain)
	t.styled = append(t.styled, styled)
}

func (t *table) write(w io.Writer) {
	if len(t.rows) == 0 {
		return
	}

	widths := make([]int, 0, 8)
	for _, row := range t.rows {
		for i, cell := range row {
			row[i] = display.Line(cell)
			cell = row[i]
			if i >= len(widths) {
				widths = append(widths, 0)
			}
			if n := utf8.RuneCountInString(cell); n > widths[i] {
				widths[i] = n
			}
		}
	}

	var b, line strings.Builder
	for r, row := range t.rows {
		line.Reset()
		for i, cell := range row {
			out := cell
			if t.styled[r] != nil && i < len(t.styled[r]) && t.styled[r][i] != "" {
				out = t.styled[r][i]
			}
			line.WriteString(out)

			if i < len(row)-1 {
				line.WriteString(strings.Repeat(" ", widths[i]-utf8.RuneCountInString(cell)+2))
			}
		}
		// Trailing whitespace is trimmed rather than merely not added: the
		// final column is often empty, in which case the padding before it
		// would otherwise run off the end of the line and show up in every
		// diff and every copied paste.
		b.WriteString(strings.TrimRight(line.String(), " \t"))
		b.WriteByte('\n')
	}
	io.WriteString(w, b.String())
}

// writeJSON prints a value as indented JSON.
func (a *App) writeJSON(v any) error {
	enc := json.NewEncoder(a.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// plural returns the word with an s when n is not one, for messages that read
// like sentences.
func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}
