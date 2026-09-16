package tui

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/shakfu/gwiki/internal/display"
	"github.com/shakfu/gwiki/internal/render"
	"github.com/shakfu/gwiki/internal/vim"
)

// Editor styles: markdown as source, not as rendered text.
var (
	styleEditHeading = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("4"))
	styleEditCode    = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	styleEditLink    = lipgloss.NewStyle().Underline(true).Foreground(lipgloss.Color("6"))
	styleEditBroken  = lipgloss.NewStyle().Underline(true).Foreground(lipgloss.Color("1"))
	styleEditMarker  = lipgloss.NewStyle().Faint(true)
	styleEditEmph    = lipgloss.NewStyle().Italic(true)
	styleEditStrong  = lipgloss.NewStyle().Bold(true)
	styleGutter      = lipgloss.NewStyle().Faint(true)
	styleMatch       = lipgloss.NewStyle().Foreground(lipgloss.Color("0")).Background(lipgloss.Color("3"))
)

// span is a styled run of one line, in rune columns.
type span struct {
	from, to int
	style    lipgloss.Style
}

var (
	reCode     = regexp.MustCompile("`[^`]+`")
	reWikiLink = regexp.MustCompile(`\[\[[^\]]+\]\]`)
	reMDLink   = regexp.MustCompile(`!?\[[^\]]*\]\([^)]*\)`)
	reStrong   = regexp.MustCompile(`\*\*[^*]+\*\*`)
	reEmph     = regexp.MustCompile(`(^|[^*])\*([^*]+)\*`)
	reMarker   = regexp.MustCompile(`^\s*(?:[-*+]|\d+[.)])\s(?:\[[ xX]\]\s)?|^\s*>\s?`)
)

// editSpans styles one source line.
func (m *WikiModel) editSpans(line string, inCode bool) []span {
	runes := []rune(line)
	at := func(i int) int { return len([]rune(line[:i])) }
	if inCode {
		return []span{{0, len(runes), styleEditCode}}
	}
	if strings.HasPrefix(strings.TrimSpace(line), "#") {
		return []span{{0, len(runes), styleEditHeading}}
	}
	var out []span
	if loc := reMarker.FindStringIndex(line); loc != nil {
		out = append(out, span{at(loc[0]), at(loc[1]), styleEditMarker})
	}
	for _, re := range []struct {
		re    *regexp.Regexp
		style lipgloss.Style
	}{{reCode, styleEditCode}, {reStrong, styleEditStrong}, {reEmph, styleEditEmph}} {
		for _, loc := range re.re.FindAllStringIndex(line, -1) {
			out = append(out, span{at(loc[0]), at(loc[1]), re.style})
		}
	}
	for _, re := range []*regexp.Regexp{reWikiLink, reMDLink} {
		for _, loc := range re.FindAllStringIndex(line, -1) {
			style := styleEditLink
			if m.edit != nil && m.edit.broken[strings.TrimSpace(line[loc[0]:loc[1]])] {
				style = styleEditBroken
			}
			out = append(out, span{at(loc[0]), at(loc[1]), style})
		}
	}
	return out
}

// styleRow draws one display row: the styled text, the search matches, the
// selection and the cursor. Characters sharing a style are drawn as one run,
// since styling each one costs about as much as the rest of the frame.
func (m *WikiModel) styleRow(runes []rune, from, to int, spans []span, marks [][2]int, sel [2]int, cursor int) string {
	var b strings.Builder
	var run strings.Builder
	last, first := -2, true
	var lastStyle lipgloss.Style

	flush := func() {
		if run.Len() == 0 {
			return
		}
		b.WriteString(lastStyle.Render(run.String()))
		run.Reset()
	}
	for i := from; i < to && i < len(runes); i++ {
		r := runes[i]
		text := string(r)
		switch {
		case r == '\t':
			text = "    "
		case display.Control(r):
			text = "?"
		}
		style, key := lipgloss.NewStyle(), -1
		for j, s := range spans {
			if i >= s.from && i < s.to {
				style, key = s.style, j
			}
		}
		for _, mk := range marks {
			if i >= mk[0] && i < mk[1] {
				style, key = styleMatch, -2
			}
		}
		if (sel[0] <= i && i <= sel[1]) || i == cursor {
			style, key = style.Reverse(true), -3
		}
		if first || key != last {
			flush()
			lastStyle, last, first = style, key, false
		}
		run.WriteString(text)
	}
	flush()
	// The cursor past the last character needs a cell of its own.
	if cursor >= to && cursor >= len(runes) && cursor < to+1 {
		b.WriteString(lipgloss.NewStyle().Reverse(true).Render(" "))
	}
	return b.String()
}

// viewEdit draws the buffer, or the preview when it is on.
func (m *WikiModel) viewEdit() []string {
	e := m.edit
	h := m.bodyHeight()
	width := m.editorWidth()

	if e.preview {
		doc := render.Render([]byte(e.ed.Text()), render.Options{Width: max(10, m.width-2), Styles: m.styles, Selected: -1})
		top := min(e.ed.Top, max(0, len(doc.Lines)-1))
		out := []string{}
		for i := top; i < len(doc.Lines) && len(out) < h; i++ {
			out = append(out, " "+doc.Lines[i])
		}
		return out
	}

	cur := e.ed.Cursor
	selA, selZ, hasSel := e.ed.Selection()
	inCode := codeStateBefore(e.ed, e.ed.Top)

	var out []string
	for l := e.ed.Top; l < e.ed.Buf.Lines() && len(out) < h; l++ {
		text := e.ed.Buf.LineString(l)
		fenced := strings.HasPrefix(strings.TrimSpace(text), "```")
		spans := m.editSpans(text, inCode || fenced)
		if fenced {
			inCode = !inCode
		}
		runes := []rune(text)
		rows := vim.Wrap(runes, width)
		marks := e.ed.Matches(l)

		sel := [2]int{-1, -2}
		if hasSel && l >= selA.Line && l <= selZ.Line {
			sel = [2]int{0, len(runes)}
			if l == selA.Line {
				sel[0] = selA.Col
			}
			if l == selZ.Line {
				sel[1] = min(selZ.Col, len(runes))
			}
		}
		for r, start := range rows {
			if len(out) >= h {
				break
			}
			end := len(runes)
			if r+1 < len(rows) {
				end = rows[r+1]
			}
			number := "    "
			if r == 0 {
				number = fmt.Sprintf("%4d", l+1)
			}
			cursorCol := -1
			if l == cur.Line && cur.Col >= start && (cur.Col < end || r == len(rows)-1) {
				cursorCol = cur.Col
			}
			out = append(out, styleGutter.Render(number)+" "+m.styleRow(runes, start, end, spans, marks, sel, cursorCol))
		}
	}
	return out
}

// codeStateBefore reports whether a line starts inside a fenced code block.
func codeStateBefore(ed *vim.Editor, line int) bool {
	in := false
	for l := 0; l < line; l++ {
		if strings.HasPrefix(strings.TrimSpace(ed.Buf.LineString(l)), "```") {
			in = !in
		}
	}
	return in
}

// viewEditStatus is the editor's status line.
func (m *WikiModel) viewEditStatus() string {
	e := m.edit
	if line, ok := e.ed.CommandLine(); ok {
		return line + "_"
	}
	if e.ed.Message != "" {
		if e.ed.Err {
			return styleError.Render(display.Line(e.ed.Message))
		}
		return display.Line(e.ed.Message)
	}
	left := styleBold.Render(e.ed.Mode.String())
	if e.preview {
		left = styleBold.Render("PREVIEW")
	}
	left += "  " + display.Line(e.page)
	if e.ed.Dirty {
		left += styleDoing.Render(" [+]")
	}
	if e.outside {
		left += styleError.Render("  changed on disk; :e! loads it")
	}
	right := fmt.Sprintf("%d:%d", e.ed.Cursor.Line+1, e.ed.Cursor.Col+1)
	if p := e.ed.Pending(); p != "" {
		right = p + "  " + right
	}
	gap := m.width - ansi.StringWidth(left) - ansi.StringWidth(right)
	if gap < 1 {
		return left
	}
	return left + strings.Repeat(" ", gap) + right
}
