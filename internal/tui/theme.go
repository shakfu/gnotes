package tui

import (
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/shakfu/gwiki/internal/render"
)

// The palette follows the browser view's (internal/webwiki/assets/app.css) in
// xterm 256 colours, each with a value for light and dark backgrounds.
var (
	colourDim    = lipgloss.AdaptiveColor{Light: "243", Dark: "248"}
	colourAccent = lipgloss.AdaptiveColor{Light: "30", Dark: "80"}
	colourLink   = lipgloss.AdaptiveColor{Light: "26", Dark: "117"}
	colourError  = lipgloss.AdaptiveColor{Light: "124", Dark: "217"}
	colourOK     = lipgloss.AdaptiveColor{Light: "29", Dark: "121"}
	colourWarn   = lipgloss.AdaptiveColor{Light: "130", Dark: "221"}
	colourTag    = lipgloss.AdaptiveColor{Light: "99", Dark: "183"}
	colourCode   = lipgloss.AdaptiveColor{Light: "94", Dark: "216"}
	colourMatch  = lipgloss.AdaptiveColor{Light: "222", Dark: "94"}
	colourSelect = lipgloss.AdaptiveColor{Light: "254", Dark: "238"}
	colourBar    = lipgloss.AdaptiveColor{Light: "252", Dark: "236"}
	colourOnTab  = lipgloss.AdaptiveColor{Light: "231", Dark: "235"}
)

// term renders every style. It is separate from lipgloss's default renderer,
// which the command line's output would share.
var term = lipgloss.NewRenderer(os.Stdout)

// Styles by role, set by setTheme.
var (
	stylePlain    lipgloss.Style
	styleDim      lipgloss.Style
	styleBold     lipgloss.Style
	styleHeader   lipgloss.Style
	styleTag      lipgloss.Style
	styleWarn     lipgloss.Style
	styleOK       lipgloss.Style
	styleError    lipgloss.Style
	styleSelected lipgloss.Style
	styleMarker   lipgloss.Style
	styleBar      lipgloss.Style
	styleTab      lipgloss.Style // the active tab in the header bar

	styleEditHeading lipgloss.Style
	styleEditCode    lipgloss.Style
	styleEditLink    lipgloss.Style
	styleEditBroken  lipgloss.Style
	styleEditEmph    lipgloss.Style
	styleEditStrong  lipgloss.Style
	styleMatch       lipgloss.Style

	// readerStyles draw a page in the reader and the editor's preview.
	readerStyles render.Styles
)

// Selection markers in the first column of a row. A marker, not only a
// highlight, so the selection shows when colour is off.
const (
	markSelected = "▌"
	markInactive = "▏"
)

func init() { setTheme(term) }

// setTheme builds the styles for r. When colour is off, by NO_COLOR or a
// terminal without it, the styles keep bold, underline and reverse video:
// NO_COLOR asks for no colour, and without them nothing shows the selection,
// the cursor or a link.
func setTheme(r *lipgloss.Renderer) {
	colour := r.ColorProfile() != termenv.Ascii
	if !colour {
		r.SetColorProfile(termenv.ANSI)
	}
	s := r.NewStyle
	fg := func(c lipgloss.AdaptiveColor) lipgloss.Style {
		if colour {
			return s().Foreground(c)
		}
		return s()
	}
	bg := func(c lipgloss.AdaptiveColor) lipgloss.Style {
		if colour {
			return s().Background(c)
		}
		return s().Reverse(true)
	}

	stylePlain = s()
	styleDim = fg(colourDim)
	if !colour {
		styleDim = s().Faint(true)
	}
	styleBold = s().Bold(true)
	styleHeader = fg(colourAccent).Bold(true)
	styleTag = fg(colourTag)
	styleWarn = fg(colourWarn)
	styleOK = fg(colourOK)
	styleError = fg(colourError)
	styleSelected = s().Bold(true)
	if colour {
		styleSelected = styleSelected.Background(colourSelect)
	}
	styleMarker = fg(colourAccent).Bold(true)
	styleBar = bg(colourBar)
	// The active tab stands out of the bar: in the accent colour, or without
	// the bar's reverse video when colour is off.
	styleTab = s().Bold(true).Reverse(false)
	if colour {
		styleTab = styleTab.Background(colourAccent).Foreground(colourOnTab)
	}

	styleEditHeading = styleHeader
	styleEditCode = fg(colourCode)
	styleEditLink = fg(colourLink).Underline(true)
	styleEditBroken = fg(colourError).Underline(true)
	styleEditEmph = s().Italic(true)
	styleEditStrong = styleBold
	styleMatch = bg(colourMatch)

	readerStyles = render.Styles{
		Heading: [3]lipgloss.Style{
			styleHeader.Underline(true),
			styleHeader,
			styleBold,
		},
		Strong:   styleBold,
		Emphasis: styleEditEmph,
		// Dim alone: lipgloss repeats a strikethrough around every word.
		Strike:   styleDim,
		Code:     styleEditCode,
		Quote:    styleDim,
		Marker:   styleDim,
		HTML:     styleDim,
		Link:     styleEditLink,
		Broken:   styleEditBroken,
		Selected: s().Reverse(true),
	}
}

// selectRow draws a selected row: the marker in the first column and the rest
// highlighted. text is plain and already fits width.
func selectRow(text string, width int) string {
	rest := []rune(text)
	if len(rest) > 0 {
		rest = rest[1:]
	}
	return styleMarker.Render(markSelected) + styleSelected.Render(pad(string(rest), width-1))
}

// seg is a run of a bar's text and the style it is drawn in over the bar.
type seg struct {
	text  string
	style lipgloss.Style
}

func plain(text string) seg { return seg{text, stylePlain} }

// bar draws a filled line of width columns: left from the left edge, cut to
// fit, and right against the right edge, dropped when the line is too narrow.
func bar(width int, left, right []seg) string {
	rw := 0
	for _, s := range right {
		rw += ansi.StringWidth(s.text)
	}
	if rw+12 > width {
		right, rw = nil, 0
	}
	var b strings.Builder
	onBar := func(s lipgloss.Style) lipgloss.Style { return s.Inherit(styleBar) }
	drawn, used := drawSegs(left, width-rw, onBar)
	b.WriteString(drawn)
	if gap := width - used - rw; gap > 0 {
		b.WriteString(styleBar.Render(strings.Repeat(" ", gap)))
	}
	for _, s := range right {
		b.WriteString(s.style.Inherit(styleBar).Render(s.text))
	}
	return b.String()
}
