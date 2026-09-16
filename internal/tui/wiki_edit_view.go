package tui

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/shakfu/gwiki/internal/display"
	"github.com/shakfu/gwiki/internal/render"
	"github.com/shakfu/gwiki/internal/vim"
	"github.com/shakfu/gwiki/internal/wiki"
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
		out = append(out, span{at(loc[0]), at(loc[1]), styleDim})
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
		style, key := stylePlain, -1
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
		b.WriteString(stylePlain.Reverse(true).Render(" "))
	}
	return b.String()
}

// viewBuffer draws the page's buffer in width columns and height rows, or its
// preview when that is on.
func (m *WikiModel) viewBuffer(width, height int) []string {
	e := m.edit
	e.ed.Width, e.ed.Height = m.bufferWidth(), height
	if e.preview {
		broken := map[string]bool{}
		for _, l := range m.bufferLinks() {
			if l.Status != wiki.StatusOK {
				broken[l.Form+"\x00"+l.Target+"\x00"+l.Anchor] = true
			}
		}
		doc := render.Render([]byte(e.ed.Text()), render.Options{Width: max(10, width-2), Styles: readerStyles, Selected: -1, Broken: func(l render.Link) bool {
			return broken[string(l.Form)+"\x00"+l.Target+"\x00"+l.Anchor]
		}})
		top := min(e.ed.Top, max(0, len(doc.Lines)-1))
		out := []string{}
		for i := top; i < len(doc.Lines) && len(out) < height; i++ {
			out = append(out, " "+doc.Lines[i])
		}
		return out
	}

	cur := e.ed.Cursor
	selA, selZ, hasSel := e.ed.Selection()
	inCode := codeStateBefore(e.ed, e.ed.Top)
	// The cursor is drawn only while the buffer has the focus.
	focused := m.focus == focusContent

	var out []string
	for l := e.ed.Top; l < e.ed.Buf.Lines() && len(out) < height; l++ {
		text := e.ed.Buf.LineString(l)
		fenced := strings.HasPrefix(strings.TrimSpace(text), "```")
		spans := m.editSpans(text, inCode || fenced)
		if fenced {
			inCode = !inCode
		}
		runes := []rune(text)
		rows := vim.Wrap(runes, m.bufferWidth())
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
			if len(out) >= height {
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
			if focused && l == cur.Line && cur.Col >= start && (cur.Col < end || r == len(rows)-1) {
				cursorCol = cur.Col
			}
			out = append(out, styleDim.Render(number)+" "+m.styleRow(runes, start, end, spans, marks, sel, cursorCol))
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

// viewEditStatus is the editor's status bar, or its command line. A message
// is shown beside the mode, the page and the position rather than in place of
// them.
func (m *WikiModel) viewEditStatus() string {
	e := m.edit
	if line, ok := e.ed.CommandLine(); ok {
		return line + "_"
	}
	label := e.ed.Mode.String()
	if e.preview {
		label = "PREVIEW"
	}
	left := []seg{plain(" "), {label, styleHeader}, plain("  " + display.Line(e.page))}
	if e.ed.Dirty {
		left = append(left, seg{" [+]", styleWarn})
	}
	if e.outside {
		left = append(left, seg{"  changed on disk; :e! loads it", styleError})
	}
	message, failed := e.ed.Message, e.ed.Err
	if message == "" {
		message, failed = m.status, m.statusErr
	}
	switch l, onLink := m.linkAtCursor(); {
	case message != "":
		style := stylePlain
		if failed {
			style = styleError
		}
		left = append(left, seg{"  " + display.Line(message), style})
	case onLink && e.ed.Mode == vim.Normal:
		left = append(left, seg{"  → " + display.Line(l.Written()), stylePlain})
		if l.Status != wiki.StatusOK {
			left = append(left, seg{"  " + strings.ReplaceAll(l.Status, "-", " "), styleError})
		} else if l.Resolved != "" {
			left = append(left, seg{"  " + display.Line(l.Resolved), styleDim})
		}
	}
	right := fmt.Sprintf("%d:%d ", e.ed.Cursor.Line+1, e.ed.Cursor.Col+1)
	if p := e.ed.Pending(); p != "" {
		right = p + "  " + right
	}
	return bar(m.width, left, []seg{{right, styleDim}})
}
