package state

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/shakfu/gnotes/internal/rank"
	"github.com/shakfu/gnotes/internal/ulid"
)

// Get returns a node by exact id, or nil. A superseded workspace id resolves
// to the surviving root.
func (s *State) Get(id string) *Node { return s.Nodes[s.deref(id)] }

// Children returns a parent's live children in rank order.
//
// The returned slice is freshly built, so a caller may sort or filter it
// without disturbing the tree.
func (s *State) Children(parent string) []*Node {
	kids := s.children[s.deref(parent)]
	out := make([]*Node, 0, len(kids))
	for _, id := range kids {
		if n := s.Nodes[id]; n != nil && !n.Deleted {
			out = append(out, n)
		}
	}
	return out
}

// Subtree returns a node and every node below it, deleted or not, or nil when
// the node does not exist.
func (s *State) Subtree(id string) []*Node {
	root := s.Get(id)
	if root == nil {
		return nil
	}
	out := []*Node{root}
	for i := 0; i < len(out); i++ {
		for _, child := range s.children[out[i].ID] {
			if n := s.Nodes[child]; n != nil {
				out = append(out, n)
			}
		}
	}
	return out
}

// LinkTarget resolves a reference to one of n's links, for removing it.
//
// A link may point at a node that is deleted or has not synced, which Resolve
// cannot find. The reference is therefore matched against the link ids first,
// as a full id or a suffix of at least HandleLen, and only then resolved as
// usual.
func (s *State) LinkTarget(n *Node, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	upper := ulid.Canonical(strings.ToUpper(ref))
	if len(ref) >= HandleLen {
		var matches []string
		for _, id := range n.Links {
			if strings.HasSuffix(id, upper) {
				matches = append(matches, id)
			}
		}
		if len(matches) == 1 {
			return matches[0], nil
		}
	}
	target, err := s.Resolve(ref)
	if err != nil {
		return "", err
	}
	return target.ID, nil
}

// Siblings returns a parent's live children as rank placements, which is what
// rank.Resolve needs to work out where a new or moved node goes.
func (s *State) Siblings(parent string, excluding string) []rank.Sibling {
	kids := s.children[s.deref(parent)]
	out := make([]rank.Sibling, 0, len(kids))
	for _, id := range kids {
		n := s.Nodes[id]
		if n == nil || n.Deleted || id == excluding {
			continue
		}
		out = append(out, rank.Sibling{ID: n.ID, Rank: n.Rank})
	}
	return out
}

// Notebooks returns the live notebooks in rank order.
func (s *State) Notebooks() []*Node { return s.Children(s.Workspace) }

// Tags returns every tag in use with its live node count, ordered by count and
// then alphabetically so the most-used tags come first.
func (s *State) Tags() []TagCount {
	out := make([]TagCount, 0, len(s.tags))
	for tag, n := range s.tags {
		out = append(out, TagCount{Tag: tag, Count: n})
	}
	slices.SortFunc(out, func(a, b TagCount) int {
		if a.Count != b.Count {
			return b.Count - a.Count
		}
		return strings.Compare(a.Tag, b.Tag)
	})
	return out
}

// TagCount is a tag and how many live nodes carry it.
type TagCount struct {
	Tag   string
	Count int
}

// Contributor returns a display name for an id, falling back to a short form
// of the id itself so an unsynced person still renders as something stable
// rather than as an empty column.
func (s *State) Contributor(id string) string {
	if c, ok := s.Contributors[id]; ok {
		return c.Name
	}
	if id == "" {
		return "unknown"
	}
	return ulid.Short(id, 6)
}

// FindContributor looks a person up by name, case-insensitively, returning
// their id.
func (s *State) FindContributor(name string) (string, bool) {
	name = strings.TrimSpace(name)
	for id, c := range s.Contributors {
		if strings.EqualFold(c.Name, name) {
			return id, true
		}
	}
	// An id passed where a name was expected should still work.
	if _, ok := s.Contributors[ulid.Canonical(name)]; ok {
		return ulid.Canonical(name), true
	}
	return "", false
}

// ErrAmbiguous reports a reference matching more than one node.
type ErrAmbiguous struct {
	Ref     string
	Matches []*Node
}

func (e *ErrAmbiguous) Error() string {
	names := make([]string, 0, len(e.Matches))
	for _, n := range e.Matches {
		if len(names) == 4 {
			names = append(names, fmt.Sprintf("and %d more", len(e.Matches)-4))
			break
		}
		names = append(names, fmt.Sprintf("%s %q", ulid.Short(n.ID, 6), n.Title))
	}
	return fmt.Sprintf("%q matches several nodes: %s", e.Ref, strings.Join(names, ", "))
}

// ErrNoMatch reports a reference matching nothing.
type ErrNoMatch struct{ Ref string }

func (e *ErrNoMatch) Error() string { return fmt.Sprintf("nothing matches %q", e.Ref) }

// HandleLen is the number of trailing id characters shown as a handle, and the
// shortest id suffix a reference is matched against.
const HandleLen = 6

// Resolve turns a user-typed reference into a live node.
//
// It tries progressively looser interpretations and stops at the first that
// matches anything: the full id, an id suffix, the exact title, a title
// prefix, then a title substring. Stopping at the first successful tier keeps
// a handle from being reported as ambiguous merely because some title contains
// it, and keeps "parser" resolvable next to "parser rewrite".
//
// Ids are matched by suffix rather than prefix because the leading characters
// of a ULID are its timestamp, so everything created in the same session
// shares them. A suffix shorter than HandleLen is not tried: two or three
// letters of a title are often valid id characters, and matching them against
// ids sent commands to entries the user never named.
//
// The workspace is only matched when kinds names it, so a word that happens to
// match the project name cannot rename or tag the root.
func (s *State) Resolve(ref string, kinds ...Kind) (*Node, error) {
	return s.resolve(ref, false, kinds)
}

// ResolveDeleted is Resolve over deleted nodes, for restoring one. It has the
// same tiers and reports ambiguity the same way rather than guessing.
func (s *State) ResolveDeleted(ref string, kinds ...Kind) (*Node, error) {
	return s.resolve(ref, true, kinds)
}

func (s *State) resolve(ref string, deleted bool, kinds []Kind) (*Node, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, &ErrNoMatch{Ref: ref}
	}

	wanted := func(n *Node) bool {
		if n.Deleted != deleted {
			return false
		}
		if len(kinds) == 0 {
			return n.Kind != KindWorkspace
		}
		return slices.Contains(kinds, n.Kind)
	}

	if n, ok := s.Nodes[ulid.Canonical(ref)]; ok && wanted(n) {
		return n, nil
	}

	upper := strings.ToUpper(ref)
	lower := strings.ToLower(ref)
	bySuffixOK := len(ref) >= HandleLen

	var bySuffix, byTitle, byPrefix, byContains []*Node
	for _, n := range s.Nodes {
		if !wanted(n) {
			continue
		}
		if bySuffixOK && strings.HasSuffix(n.ID, upper) {
			bySuffix = append(bySuffix, n)
			continue
		}
		title := strings.ToLower(n.Title)
		switch {
		case title == lower:
			byTitle = append(byTitle, n)
		case strings.HasPrefix(title, lower):
			byPrefix = append(byPrefix, n)
		case strings.Contains(title, lower):
			byContains = append(byContains, n)
		}
	}

	for _, tier := range [][]*Node{bySuffix, byTitle, byPrefix, byContains} {
		switch len(tier) {
		case 0:
			continue
		case 1:
			return tier[0], nil
		default:
			// Sorted so the message is the same every time it is printed.
			slices.SortFunc(tier, func(a, b *Node) int { return strings.Compare(a.ID, b.ID) })
			return nil, &ErrAmbiguous{Ref: ref, Matches: tier}
		}
	}
	return nil, &ErrNoMatch{Ref: ref}
}

// Filter narrows a listing. A zero Filter matches every live note and task.
type Filter struct {
	// Kinds limits results to these kinds; empty means notes and tasks.
	Kinds []Kind

	// Notebook limits results to one notebook by id.
	Notebook string

	// Tags requires every listed tag to be present.
	Tags []string

	// Status and Priority apply only to tasks. Nil means no constraint, which
	// is why they are pointers: the zero Status is a meaningful value.
	Status   *Status
	Priority *Priority

	// Assignee requires a contributor id.
	Assignee string

	// Text matches the title, case-insensitively. Body text is the search
	// index's job, not this one's.
	Text string

	// DueBefore keeps tasks due strictly before this instant.
	DueBefore time.Time

	// Overdue keeps unfinished tasks whose due date has passed, relative to
	// the Now field.
	Overdue bool

	// Now anchors the relative date tests. It defaults to the wall clock.
	Now time.Time

	// IncludeDeleted brings tombstoned nodes back into the listing.
	IncludeDeleted bool
}

// Order names a sort for a listing.
type Order uint8

// The available sorts.
const (
	// OrderRank is the user's own arrangement, and is the default because it
	// is the only one the user controls directly.
	OrderRank Order = iota
	OrderCreated
	OrderUpdated
	OrderTitle
	OrderDue
	OrderPriority
)

// ParseOrder reads a sort name.
func ParseOrder(s string) (Order, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "rank", "manual", "order":
		return OrderRank, true
	case "created", "age", "new":
		return OrderCreated, true
	case "updated", "modified", "recent":
		return OrderUpdated, true
	case "title", "name", "alpha":
		return OrderTitle, true
	case "due", "deadline":
		return OrderDue, true
	case "priority", "prio":
		return OrderPriority, true
	}
	return 0, false
}

// List returns the nodes matching f, sorted by order.
func (s *State) List(f Filter, order Order) []*Node {
	now := f.Now
	if now.IsZero() {
		now = time.Now()
	}

	kinds := f.Kinds
	if len(kinds) == 0 {
		kinds = []Kind{KindNote, KindTask}
	}
	text := strings.ToLower(f.Text)

	tags := make([]string, len(f.Tags))
	for i, t := range f.Tags {
		tags[i] = NormalizeTag(t)
	}

	var out []*Node
	for _, n := range s.Nodes {
		if n.Deleted && !f.IncludeDeleted {
			continue
		}
		if !slices.Contains(kinds, n.Kind) {
			continue
		}
		if f.Notebook != "" && n.Parent != f.Notebook {
			continue
		}
		if text != "" && !strings.Contains(strings.ToLower(n.Title), text) {
			continue
		}
		if !hasAllTags(n, tags) {
			continue
		}
		if f.Assignee != "" && !slices.Contains(n.Assignees, f.Assignee) {
			continue
		}
		// Task constraints exclude notes outright: a note has no status, so
		// asking for open items should not return every note in the project.
		if f.Status != nil && (n.Kind != KindTask || n.Status != *f.Status) {
			continue
		}
		if f.Priority != nil && (n.Kind != KindTask || n.Priority != *f.Priority) {
			continue
		}
		if f.Overdue && !n.Overdue(now) {
			continue
		}
		if !f.DueBefore.IsZero() && (n.Due.IsZero() || !n.Due.Before(f.DueBefore)) {
			continue
		}
		out = append(out, n)
	}

	s.sortNodes(out, order)
	return out
}

// hasAllTags reports whether n carries every requested tag.
func hasAllTags(n *Node, tags []string) bool {
	for _, t := range tags {
		if t != "" && !n.HasTag(t) {
			return false
		}
	}
	return true
}

// sortNodes orders a listing. Every comparison falls through to the id so that
// two equal entries never swap places between runs.
func (s *State) sortNodes(nodes []*Node, order Order) {
	// Node maps iterate in random order, so the listing above arrives
	// shuffled. Sorting must therefore be total, not merely correct on the
	// primary key.
	cmp := func(a, b *Node) int { return strings.Compare(a.ID, b.ID) }

	switch order {
	case OrderRank:
		// Rank only orders siblings, so nodes from different notebooks are
		// grouped by their notebook's own rank first.
		cmp = func(a, b *Node) int {
			if a.Parent != b.Parent {
				if c := strings.Compare(s.parentRank(a), s.parentRank(b)); c != 0 {
					return c
				}
			}
			if c := strings.Compare(a.Rank, b.Rank); c != 0 {
				return c
			}
			return strings.Compare(a.ID, b.ID)
		}
	case OrderCreated:
		cmp = func(a, b *Node) int { return chain(a.Created.Compare(b.Created), a, b) }
	case OrderUpdated:
		// Most recently touched first, which is what "recent" means to a
		// reader.
		cmp = func(a, b *Node) int { return chain(b.Updated.Compare(a.Updated), a, b) }
	case OrderTitle:
		cmp = func(a, b *Node) int {
			return chain(strings.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title)), a, b)
		}
	case OrderDue:
		cmp = func(a, b *Node) int {
			// Undated tasks sort last: a list ordered by deadline is asking
			// about the things that have one.
			switch {
			case a.Due.IsZero() && b.Due.IsZero():
				return strings.Compare(a.ID, b.ID)
			case a.Due.IsZero():
				return 1
			case b.Due.IsZero():
				return -1
			}
			return chain(a.Due.Compare(b.Due), a, b)
		}
	case OrderPriority:
		cmp = func(a, b *Node) int { return chain(int(b.Priority)-int(a.Priority), a, b) }
	}

	slices.SortFunc(nodes, cmp)
}

// chain falls back to id order when the primary comparison is a tie.
func chain(primary int, a, b *Node) int {
	if primary != 0 {
		return primary
	}
	return strings.Compare(a.ID, b.ID)
}

// parentRank returns the rank of a node's parent, for grouping a cross-
// notebook listing by notebook.
func (s *State) parentRank(n *Node) string {
	if p, ok := s.Nodes[n.Parent]; ok {
		return p.Rank
	}
	return ""
}

// Backlinks returns the live nodes that link to id. Links are stored on the
// node that declares them, so finding the other direction means a scan; the
// alternative is a reverse index that has to be kept in step through every
// delete and restore, which is not worth it at this size.
func (s *State) Backlinks(id string) []*Node {
	var out []*Node
	for _, n := range s.Nodes {
		if !n.Deleted && slices.Contains(n.Links, id) {
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Path returns the titles from the workspace down to n, for showing where a
// node lives.
func (s *State) Path(n *Node) []string {
	var parts []string
	for cur := n; cur != nil; cur = s.Nodes[cur.Parent] {
		parts = append(parts, cur.Title)
		if cur.Parent == "" {
			break
		}
	}
	slices.Reverse(parts)
	return parts
}

// Counts summarises a project.
type Counts struct {
	Notebooks int
	Notes     int
	Tasks     int
	Open      int
	Doing     int
	Done      int
	Overdue   int
}

// Summary counts the live nodes by kind and task status.
func (s *State) Summary(now time.Time) Counts {
	var c Counts
	for _, n := range s.Nodes {
		if n.Deleted {
			continue
		}
		switch n.Kind {
		case KindNotebook:
			c.Notebooks++
		case KindNote:
			c.Notes++
		case KindTask:
			c.Tasks++
			switch n.Status {
			case StatusOpen:
				c.Open++
			case StatusDoing:
				c.Doing++
			case StatusDone:
				c.Done++
			}
			if n.Overdue(now) {
				c.Overdue++
			}
		}
	}
	return c
}
