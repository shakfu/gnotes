package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// cell is one column of a row: a run of styled segments.
type cell []seg

func text(s string, style lipgloss.Style) cell { return cell{{s, style}} }

func (c cell) width() int {
	w := 0
	for _, s := range c {
		w += ansi.StringWidth(s.text)
	}
	return w
}

// drawSegs draws segments cut to room columns, each style passed through
// over, and returns the width drawn.
func drawSegs(segs []seg, room int, over func(lipgloss.Style) lipgloss.Style) (string, int) {
	var b strings.Builder
	used := 0
	for _, s := range segs {
		text := truncate(s.text, room-used)
		if text == "" {
			break
		}
		b.WriteString(over(s.style).Render(text))
		used += ansi.StringWidth(text)
	}
	return b.String(), used
}

func same(s lipgloss.Style) lipgloss.Style { return s }

// column describes one column of aligned rows.
type column struct {
	title string
	right bool // aligned right

	// shrink orders the columns cut when the rows do not fit: highest first,
	// and never when zero. min is the narrowest a column is cut to; a column
	// cut to zero is left out.
	shrink, min int
}

// table is rows of cells in columns. Every row starts with a one-column
// gutter, which holds the selection marker.
type table struct {
	cols   []column
	rows   [][]cell
	widths []int
}

// columnGap separates columns.
const columnGap = "  "

// layout sizes the columns for width: each as wide as its widest cell, and
// cut in shrink order until the rows fit. A column with no text is left out,
// title and all.
func (t *table) layout(width int) {
	t.widths = make([]int, len(t.cols))
	for _, r := range t.rows {
		for i, c := range r {
			if i < len(t.cols) {
				t.widths[i] = max(t.widths[i], c.width())
			}
		}
	}
	for i, c := range t.cols {
		if t.widths[i] > 0 {
			t.widths[i] = max(t.widths[i], ansi.StringWidth(c.title))
		}
	}
	for t.total() > width {
		cut := -1
		for i, c := range t.cols {
			if c.shrink > 0 && t.widths[i] > c.min && (cut < 0 || c.shrink > t.cols[cut].shrink) {
				cut = i
			}
		}
		if cut < 0 {
			break
		}
		t.widths[cut] = max(t.cols[cut].min, t.widths[cut]-(t.total()-width))
	}
}

// total is the width of a row: the gutter, the columns and the gaps.
func (t *table) total() int {
	n, shown := 1, 0
	for _, w := range t.widths {
		if w > 0 {
			n += w
			shown++
		}
	}
	return n + len(columnGap)*max(0, shown-1)
}

// draw renders one row's cells in the laid-out columns.
func (t *table) draw(cells []cell) string {
	var b strings.Builder
	b.WriteByte(' ')
	first := true
	for i, w := range t.widths {
		if w == 0 {
			continue
		}
		if !first {
			b.WriteString(columnGap)
		}
		first = false
		var c cell
		if i < len(cells) {
			c = cells[i]
		}
		s, used := drawSegs(c, w, same)
		space := strings.Repeat(" ", w-used)
		if t.cols[i].right {
			b.WriteString(space + s)
		} else {
			b.WriteString(s + space)
		}
	}
	return strings.TrimRight(b.String(), " ")
}

// header renders the column titles, dim.
func (t *table) header() string {
	cells := make([]cell, len(t.cols))
	for i, c := range t.cols {
		cells[i] = text(c.title, styleDim)
	}
	return t.draw(cells)
}
