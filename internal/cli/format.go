package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/shakfu/gnotes/internal/state"
	"github.com/shakfu/gnotes/internal/ulid"
)

// refLen is how many trailing characters of a ULID are shown as a node's
// handle. Six characters of Crockford base32 is thirty bits, enough that a
// collision inside one project is not a practical concern, and short enough to
// retype.
const refLen = 6

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
func (a *App) style(code, s string) string {
	if !a.Color || s == "" {
		return s
	}
	return code + s + ansiReset
}

// ref renders a node's short handle.
func ref(n *state.Node) string { return ulid.Short(n.ID, refLen) }

// marker is the leading glyph for a row: an empty or filled box for a task,
// depending on its status, and a plain rule for a note.
//
// ASCII rather than symbols, so the output is the same width in every terminal
// and safe to pipe anywhere.
func marker(n *state.Node) string {
	if n.Kind != state.KindTask {
		return "-"
	}
	switch n.Status {
	case state.StatusDone:
		return "[x]"
	case state.StatusDoing:
		return "[~]"
	default:
		return "[ ]"
	}
}

// markerStyle colours a marker by what it says.
func (a *App) markerStyle(n *state.Node) string {
	m := marker(n)
	switch {
	case n.Kind != state.KindTask:
		return a.style(ansiDim, m)
	case n.Status == state.StatusDone:
		return a.style(ansiGreen, m)
	case n.Status == state.StatusDoing:
		return a.style(ansiYellow, m)
	}
	return m
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

// listNodes prints nodes as a table: handle, marker, title, then the metadata
// that is actually set.
func (a *App) listNodes(s sessionState, nodes []*state.Node, showNotebook bool) {
	if len(nodes) == 0 {
		fmt.Fprintln(a.Stdout, "nothing to show")
		return
	}
	now := a.Now()

	var t table
	for _, n := range nodes {
		plain := []string{ref(n), marker(n), n.Title}
		styled := []string{a.style(ansiDim, ref(n)), a.markerStyle(n), a.titleStyle(n)}

		if showNotebook {
			nb := ""
			if parent := s.Get(n.Parent); parent != nil {
				nb = parent.Title
			}
			plain = append(plain, nb)
			styled = append(styled, a.style(ansiBlue, nb))
		}

		meta := a.meta(n, now)
		plain = append(plain, meta)
		styled = append(styled, a.metaStyled(n, now))
		t.addStyled(plain, styled)
	}
	t.write(a.Stdout)
}

// sessionState is the slice of the tree the formatter needs, so that printing
// works equally on a present and a time-travelled state.
type sessionState interface {
	Get(id string) *state.Node
	Contributor(id string) string
}

// titleStyle dims a completed task's title, since a done item is context
// rather than something to act on.
func (a *App) titleStyle(n *state.Node) string {
	if n.Kind == state.KindTask && n.Status == state.StatusDone {
		return a.style(ansiDim, n.Title)
	}
	return n.Title
}

// meta renders the trailing annotations: tags, priority, due date, assignees.
// Only fields that are set appear, so an unadorned note prints as one clean
// line.
func (a *App) meta(n *state.Node, now time.Time) string {
	return strings.Join(a.metaParts(n, now, false), " ")
}

func (a *App) metaStyled(n *state.Node, now time.Time) string {
	return strings.Join(a.metaParts(n, now, true), " ")
}

func (a *App) metaParts(n *state.Node, now time.Time, styled bool) []string {
	paint := func(code, s string) string {
		if !styled {
			return s
		}
		return a.style(code, s)
	}

	var parts []string
	for _, tag := range n.Tags {
		parts = append(parts, paint(ansiCyan, "#"+tag))
	}
	if n.Priority != state.PriorityNone {
		code := ansiDim
		if n.Priority == state.PriorityHigh {
			code = ansiRed
		}
		parts = append(parts, paint(code, "!"+n.Priority.String()))
	}
	if !n.Due.IsZero() {
		due := "due:" + state.FormatDue(n.Due)
		code := ansiDim
		if n.Overdue(now) {
			code = ansiRed
			due += "!"
		}
		parts = append(parts, paint(code, due))
	}
	if len(n.Links) > 0 {
		parts = append(parts, paint(ansiDim, fmt.Sprintf("->%d", len(n.Links))))
	}
	return parts
}

// showNode prints one node in full, including its body.
func (a *App) showNode(s sessionState, n *state.Node, path []string) {
	a.printf("%s  %s\n", a.style(ansiDim, ref(n)), a.style(ansiBold, n.Title))
	a.printf("%s\n", a.style(ansiDim, strings.Join(path, " / ")))
	a.printf("\n")

	var t table
	t.add("kind", n.Kind.String())
	if n.Kind == state.KindTask {
		t.add("status", n.Status.String())
		if n.Priority != state.PriorityNone {
			t.add("priority", n.Priority.String())
		}
		if !n.Due.IsZero() {
			due := state.FormatDue(n.Due)
			if n.Overdue(a.Now()) {
				due += "  (overdue)"
			}
			t.add("due", due)
		}
		if len(n.Assignees) > 0 {
			names := make([]string, len(n.Assignees))
			for i, id := range n.Assignees {
				names[i] = s.Contributor(id)
			}
			t.add("assigned", strings.Join(names, ", "))
		}
	}
	if len(n.Tags) > 0 {
		t.add("tags", "#"+strings.Join(n.Tags, " #"))
	}
	if len(n.Links) > 0 {
		t.add("links", strings.Join(a.linkLabels(s, n.Links), ", "))
	}
	t.add("created", fmt.Sprintf("%s by %s", n.Created.Local().Format("2006-01-02 15:04"), s.Contributor(n.CreatedBy)))
	if !n.Updated.Equal(n.Created) {
		t.add("updated", fmt.Sprintf("%s by %s", n.Updated.Local().Format("2006-01-02 15:04"), s.Contributor(n.UpdatedBy)))
	}
	if n.Deleted {
		t.add("deleted", "yes")
	}
	t.write(a.Stdout)

	if n.Body != "" {
		a.printf("\n%s\n", n.Body)
	}
}

// linkLabels renders link targets as a handle and a title, or as a note that
// the target has not arrived yet.
func (a *App) linkLabels(s sessionState, ids []string) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		if target := s.Get(id); target != nil {
			out[i] = fmt.Sprintf("%s %s", ulid.Short(id, refLen), target.Title)
			continue
		}
		// Links are allowed to point at nodes that have not synced yet.
		out[i] = fmt.Sprintf("%s (not synced)", ulid.Short(id, refLen))
	}
	return out
}

// jsonNode is the machine-readable shape of a node.
//
// It is a separate type from state.Node rather than tags on it, because this
// is an interface other programs depend on and it should not change every time
// an internal field does.
type jsonNode struct {
	ID        string   `json:"id"`
	Ref       string   `json:"ref"`
	Kind      string   `json:"kind"`
	Title     string   `json:"title"`
	Body      string   `json:"body,omitempty"`
	Notebook  string   `json:"notebook,omitempty"`
	Tags      []string `json:"tags,omitempty"`
	Links     []string `json:"links,omitempty"`
	Status    string   `json:"status,omitempty"`
	Priority  string   `json:"priority,omitempty"`
	Due       string   `json:"due,omitempty"`
	Assignees []string `json:"assignees,omitempty"`
	Deleted   bool     `json:"deleted,omitempty"`
	Created   string   `json:"created"`
	Updated   string   `json:"updated"`
	CreatedBy string   `json:"createdBy,omitempty"`
}

// toJSON converts a node for output, resolving ids to names where a reader
// would otherwise have to make a second lookup.
func toJSON(s sessionState, n *state.Node) jsonNode {
	j := jsonNode{
		ID:      n.ID,
		Ref:     ref(n),
		Kind:    n.Kind.String(),
		Title:   n.Title,
		Body:    n.Body,
		Tags:    n.Tags,
		Links:   n.Links,
		Deleted: n.Deleted,
		Created: n.Created.UTC().Format(time.RFC3339),
		Updated: n.Updated.UTC().Format(time.RFC3339),
	}
	if parent := s.Get(n.Parent); parent != nil {
		j.Notebook = parent.Title
	}
	if n.CreatedBy != "" {
		j.CreatedBy = s.Contributor(n.CreatedBy)
	}
	if n.Kind == state.KindTask {
		j.Status = n.Status.String()
		j.Priority = n.Priority.String()
		j.Due = state.FormatDue(n.Due)
		for _, id := range n.Assignees {
			j.Assignees = append(j.Assignees, s.Contributor(id))
		}
	}
	return j
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
