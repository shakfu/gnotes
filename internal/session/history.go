package session

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/shakfu/gnotes/internal/search"
	"github.com/shakfu/gnotes/internal/state"
	"github.com/shakfu/gnotes/internal/store"
	"github.com/shakfu/gnotes/internal/ulid"
)

// Entry is one line of history: what one write did to one node.
type Entry struct {
	Seq    int64
	At     time.Time
	Author string
	Node   string

	// Action names the change in the vocabulary of the commands, such as
	// add.note, set.status or remove.tag.
	Action string

	// Detail is the value set, rendered for reading: a title, a status, a tag,
	// a byte count for a body.
	Detail string
}

// created are the fields a creation writes along with the kind, which its
// history line already covers.
var created = []string{"parent", "rank", "title", "status"}

// History returns the last limit entries, oldest first, for one node or, with
// node empty, for the whole project. A limit of zero or less returns them all.
//
// Changes are grouped into entries: a creation's row for each field is one
// add line, and a move's parent and rank are one move line.
func (s *Session) History(node string, limit int) ([]Entry, error) {
	changes, err := store.History(s.Project, node, 0)
	if err != nil {
		return nil, err
	}

	var out []Entry
	for i := 0; i < len(changes); i++ {
		c := changes[i]
		e := Entry{Seq: c.Seq, At: c.At, Author: c.Author, Node: c.Node}
		// absorb consumes the changes that follow c in the same write: the
		// next seqs, to the same node, at the same time, each field in fields
		// at most once.
		absorb := func(fields ...string) []store.Change {
			taken := []store.Change{c}
			for i+1 < len(changes) {
				next, prev := changes[i+1], changes[i]
				if next.Seq != prev.Seq+1 || next.Node != c.Node || !next.At.Equal(c.At) ||
					!slices.Contains(fields, next.Field) ||
					slices.ContainsFunc(taken, func(x store.Change) bool { return x.Field == next.Field }) {
					break
				}
				i++
				taken = append(taken, next)
			}
			return taken[1:]
		}

		switch c.Field {
		case "kind":
			e.Action, e.Detail = "add."+c.New, c.New
			if c.Old != "" {
				e.Action, e.Detail = "set.kind", c.New
			}
			for _, x := range absorb(created...) {
				if x.Field == "title" {
					e.Detail = x.New
				}
			}
		case "parent", "rank":
			e.Action, e.Detail = "move.node", "reordered"
			for _, x := range append([]store.Change{c}, absorb("parent", "rank")...) {
				if x.Field == "parent" {
					e.Detail = "to " + s.title(x.New)
				}
			}
		case "title":
			e.Action, e.Detail = "edit.title", c.New
		case "body":
			e.Action, e.Detail = "edit.body", fmt.Sprintf("%d bytes", len(c.New))
		case "status", "priority", "due":
			e.Action, e.Detail = "set."+c.Field, c.New
			if c.New == "" {
				e.Detail = "none"
			}
		case "deleted_at":
			e.Action = "delete.node"
			if c.New == "" {
				e.Action = "restore.node"
			}
		case "tag", "assignee", "link":
			added, value := c.New != "", c.New
			if !added {
				value = c.Old
			}
			e.Action, e.Detail = "add."+c.Field, value
			if !added {
				e.Action = "remove." + c.Field
			}
			switch c.Field {
			case "assignee":
				e.Detail = s.State.Contributor(value)
			case "link":
				e.Action, e.Detail = "link.node", s.title(value)
				if !added {
					e.Action = "unlink.node"
				}
			}
		case "contributor":
			e.Action, e.Detail = "create.contributor", c.New
			if c.Old != "" {
				e.Action = "rename.contributor"
			}
		case "removed":
			e.Action, e.Detail = "remove.row", c.Old
		default:
			e.Action, e.Detail = "set."+c.Field, c.New
		}
		if e.Author != "" {
			e.Author = s.State.Contributor(e.Author)
		}
		out = append(out, e)
	}

	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

// title names a node for a history line: its title, or its short id when it
// no longer exists.
func (s *Session) title(id string) string {
	if n := s.State.Get(id); n != nil {
		return n.Title
	}
	return ulid.Short(id, 6)
}

// Search returns the notes and tasks matching query, best first.
//
// Every word must match, and the last also by prefix. A title containing the
// whole query ranks first; then BM25, with a title hit weighted above a tag
// hit, and a tag hit above a body hit. Ties break on id so a repeated search
// never reshuffles. A limit of zero returns every match.
func (s *Session) Search(query string, limit int, includeDeleted bool) ([]*state.Node, error) {
	match := search.Query(query)
	if match == "" {
		return nil, nil
	}
	hits, err := store.Search(s.Project, match)
	if err != nil {
		return nil, err
	}

	type scored struct {
		n      *state.Node
		phrase bool
		score  float64
	}
	phrase := strings.ToLower(strings.TrimSpace(query))
	var found []scored
	for _, h := range hits {
		n := s.State.Get(h.ID)
		if n == nil || (n.Deleted && !includeDeleted) || (n.Kind != state.KindNote && n.Kind != state.KindTask) {
			continue
		}
		found = append(found, scored{n, strings.Contains(strings.ToLower(n.Title), phrase), h.Score})
	}
	slices.SortFunc(found, func(a, b scored) int {
		switch {
		case a.phrase != b.phrase:
			if a.phrase {
				return -1
			}
			return 1
		case a.score != b.score:
			if a.score < b.score {
				return -1
			}
			return 1
		}
		return strings.Compare(a.n.ID, b.n.ID)
	})

	if limit > 0 && len(found) > limit {
		found = found[:limit]
	}
	out := make([]*state.Node, len(found))
	for i, f := range found {
		out[i] = f.n
	}
	return out, nil
}
