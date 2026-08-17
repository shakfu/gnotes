package state

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/shakfu/gnotes/internal/event"
	"github.com/shakfu/gnotes/internal/ulid"
)

// State is the tree produced by replaying a log.
type State struct {
	// Workspace is the id of the root node, empty before the project is
	// initialised.
	Workspace string

	// Nodes is every node ever created, including deleted ones.
	Nodes map[string]*Node

	// Contributors is the registry of people who have written to the log.
	Contributors map[string]*Contributor

	// children maps a parent id to its children, kept sorted by rank.
	children map[string][]string

	// tags counts how many live nodes carry each tag, for completion and for
	// listing the tags actually in use.
	tags map[string]int

	// aliases redirects a superseded workspace id onto the surviving one. See
	// the InitWorkspace case in apply.
	aliases map[string]string

	// dirty lists parents whose children changed and need reordering. It lets
	// a bulk replay defer all the sorting to the end while a single applied
	// event still leaves the tree correctly ordered.
	dirty []string
}

// Problem is an event that could not be applied.
//
// A rejected event is not a crash. Logs merge from machines running different
// versions and syncing at different times, so replay routinely meets an event
// whose target has not arrived, or two people who deleted the same node. The
// event stays on disk and the reader is told, rather than the tool refusing to
// open the project.
type Problem struct {
	// EventID is the offending event.
	EventID string

	// Action is what it tried to do.
	Action event.Action

	// Reason is a human-readable explanation.
	Reason string
}

func (p Problem) String() string {
	return fmt.Sprintf("event %s (%s): %s", ulid.Short(p.EventID, 6), p.Action, p.Reason)
}

// Materialize replays events, which must already be in canonical order, and
// returns the resulting tree along with any events that could not be applied.
func Materialize(events []event.Event) (*State, []Problem) {
	s := &State{
		Nodes:        make(map[string]*Node, len(events)/4+8),
		Contributors: make(map[string]*Contributor, 4),
		children:     make(map[string][]string, len(events)/8+8),
		tags:         make(map[string]int, 16),
		aliases:      make(map[string]string, 1),
	}

	var problems []Problem
	for i := range events {
		if err := s.apply(&events[i]); err != nil {
			problems = append(problems, Problem{
				EventID: events[i].ID,
				Action:  events[i].Action,
				Reason:  err.Error(),
			})
		}
	}

	// Children accumulate in arrival order during replay and are ordered once
	// at the end. Sorting on every insert would be O(n log n) per event for no
	// benefit, since nothing reads the tree until replay finishes.
	for parent, kids := range s.children {
		s.sortChildren(parent, kids)
	}
	s.dirty = s.dirty[:0]

	return s, problems
}

// Apply folds one further event into an already-materialized tree.
//
// It exists so a session can act on a node it created a moment ago without
// writing to disk and replaying from scratch. Unlike the bulk path it reorders
// affected siblings immediately, since the caller will read the tree straight
// after.
func (s *State) Apply(e *event.Event) error {
	if err := s.apply(e); err != nil {
		return err
	}
	for _, parent := range s.dirty {
		s.sortChildren(parent, s.children[parent])
	}
	s.dirty = s.dirty[:0]
	return nil
}

// markDirty records that a parent's children need reordering.
func (s *State) markDirty(parent string) {
	if parent != "" {
		s.dirty = append(s.dirty, parent)
	}
}

// sortChildren orders siblings by rank, falling back to id so that two nodes
// that somehow share a rank still have a stable order rather than one that
// depends on the sort implementation.
func (s *State) sortChildren(parent string, kids []string) {
	slices.SortFunc(kids, func(a, b string) int {
		x, y := s.Nodes[a], s.Nodes[b]
		if c := strings.Compare(x.Rank, y.Rank); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	})
	s.children[parent] = kids
}

// apply folds one event into the state.
func (s *State) apply(e *event.Event) error {
	at, err := ulid.Timestamp(e.ID)
	if err != nil {
		return fmt.Errorf("event id is not a ULID")
	}
	p := &e.Payload

	switch e.Action {
	case event.InitWorkspace:
		if s.Workspace != "" {
			// Two machines can both initialise before ever syncing, and when
			// their logs meet there are two roots. The first in canonical
			// order survives and the later one becomes an alias for it, so
			// every notebook written against either id lands in the same tree.
			//
			// Aliasing rather than demoting the loser to a notebook: a
			// notebook cannot contain notebooks, so demotion would leave that
			// person's whole tree unattachable. The cost is the redundant
			// root's name, which nothing refers to.
			if p.ID != "" && p.ID != s.Workspace {
				s.aliases[p.ID] = s.Workspace
			}
			return nil
		}
		if err := s.addNode(e, at, KindWorkspace, "", p.Name); err != nil {
			return err
		}
		s.Workspace = p.ID
		return nil

	case event.AddNotebook:
		return s.addNode(e, at, KindNotebook, p.Parent, p.Name)

	case event.AddNote:
		return s.addNode(e, at, KindNote, p.Parent, p.Title)

	case event.AddTask:
		return s.addNode(e, at, KindTask, p.Parent, p.Title)

	case event.EditTitle:
		n, err := s.live(p.ID)
		if err != nil {
			return err
		}
		title := p.Title
		if title == "" {
			title = p.Name
		}
		if strings.TrimSpace(title) == "" {
			return fmt.Errorf("title cannot be empty")
		}
		n.Title = title
		s.touch(n, e, at)
		return nil

	case event.EditBody:
		n, err := s.live(p.ID)
		if err != nil {
			return err
		}
		if n.Kind != KindNote && n.Kind != KindTask {
			return fmt.Errorf("a %s has no body", n.Kind)
		}
		n.Body = p.Body
		s.touch(n, e, at)
		return nil

	case event.MoveNode:
		return s.move(e, at)

	case event.DeleteNode:
		n, err := s.node(p.ID)
		if err != nil {
			return err
		}
		if n.Kind == KindWorkspace {
			return fmt.Errorf("the workspace cannot be deleted")
		}
		if n.Deleted {
			// Two people deleting the same node is agreement, not a conflict.
			return nil
		}
		s.setDeleted(n, true)
		s.touch(n, e, at)
		return nil

	case event.RestoreNode:
		n, err := s.node(p.ID)
		if err != nil {
			return err
		}
		if !n.Deleted {
			return nil
		}
		// Restoring into a deleted parent would produce a node that is live
		// but unreachable from the tree.
		if parent, ok := s.Nodes[n.Parent]; ok && parent.Deleted {
			return fmt.Errorf("its %s was deleted; restore that first", parent.Kind)
		}
		s.setDeleted(n, false)
		s.touch(n, e, at)
		return nil

	case event.Rebalance:
		parent, err := s.node(p.Parent)
		if err != nil {
			return err
		}
		applied := 0
		for id, r := range p.Ranks {
			n, ok := s.Nodes[id]
			if !ok || n.Parent != parent.ID {
				// The respacing was computed against a view that has since
				// changed. Applying the entries that still make sense keeps
				// the surviving siblings correctly spaced.
				continue
			}
			n.Rank = r
			applied++
		}
		if applied == 0 {
			return fmt.Errorf("no children left to rebalance")
		}
		s.markDirty(parent.ID)
		return nil

	case event.AddTag:
		n, err := s.live(p.ID)
		if err != nil {
			return err
		}
		tag := NormalizeTag(p.Tag)
		if tag == "" {
			return fmt.Errorf("tag is empty")
		}
		if n.HasTag(tag) {
			return nil
		}
		n.Tags = append(n.Tags, tag)
		sort.Strings(n.Tags)
		s.tags[tag]++
		s.touch(n, e, at)
		return nil

	case event.RemoveTag:
		n, err := s.live(p.ID)
		if err != nil {
			return err
		}
		tag := NormalizeTag(p.Tag)
		i := slices.Index(n.Tags, tag)
		if i < 0 {
			return nil
		}
		n.Tags = slices.Delete(n.Tags, i, i+1)
		s.dropTag(tag)
		s.touch(n, e, at)
		return nil

	case event.SetStatus:
		n, err := s.task(p.ID)
		if err != nil {
			return err
		}
		status, ok := ParseStatus(p.Status)
		if !ok {
			return fmt.Errorf("unknown status %q", p.Status)
		}
		n.Status = status
		s.touch(n, e, at)
		return nil

	case event.SetPriority:
		n, err := s.task(p.ID)
		if err != nil {
			return err
		}
		priority, ok := ParsePriority(p.Priority)
		if !ok {
			return fmt.Errorf("unknown priority %q", p.Priority)
		}
		n.Priority = priority
		s.touch(n, e, at)
		return nil

	case event.SetDue:
		n, err := s.task(p.ID)
		if err != nil {
			return err
		}
		// Replay must not depend on when it runs, so relative words like
		// "tomorrow" are resolved when the event is written, never here. The
		// stored value is always an absolute date.
		due, ok := ParseDueAbsolute(p.Due)
		if !ok {
			return fmt.Errorf("unparseable due date %q", p.Due)
		}
		n.Due = due
		s.touch(n, e, at)
		return nil

	case event.AddAssignee:
		n, err := s.task(p.ID)
		if err != nil {
			return err
		}
		if p.Assignee == "" {
			return fmt.Errorf("assignee is empty")
		}
		if slices.Contains(n.Assignees, p.Assignee) {
			return nil
		}
		n.Assignees = append(n.Assignees, p.Assignee)
		sort.Strings(n.Assignees)
		s.touch(n, e, at)
		return nil

	case event.RemoveAssignee:
		n, err := s.task(p.ID)
		if err != nil {
			return err
		}
		i := slices.Index(n.Assignees, p.Assignee)
		if i < 0 {
			return nil
		}
		n.Assignees = slices.Delete(n.Assignees, i, i+1)
		s.touch(n, e, at)
		return nil

	case event.LinkNode:
		n, err := s.live(p.ID)
		if err != nil {
			return err
		}
		if p.Target == "" {
			return fmt.Errorf("link target is empty")
		}
		if p.Target == n.ID {
			return fmt.Errorf("a node cannot link to itself")
		}
		// The target is deliberately not required to exist. Links cross
		// notebooks and machines, and a link written before its target has
		// synced is pending, not wrong.
		if slices.Contains(n.Links, p.Target) {
			return nil
		}
		n.Links = append(n.Links, p.Target)
		sort.Strings(n.Links)
		s.touch(n, e, at)
		return nil

	case event.UnlinkNode:
		n, err := s.live(p.ID)
		if err != nil {
			return err
		}
		i := slices.Index(n.Links, p.Target)
		if i < 0 {
			return nil
		}
		n.Links = slices.Delete(n.Links, i, i+1)
		s.touch(n, e, at)
		return nil

	case event.CreateContributor:
		if p.ID == "" || strings.TrimSpace(p.Name) == "" {
			return fmt.Errorf("a contributor needs an id and a name")
		}
		if _, exists := s.Contributors[p.ID]; exists {
			return nil
		}
		s.Contributors[p.ID] = &Contributor{ID: p.ID, Name: p.Name}
		return nil

	case event.RenameContributor:
		c, ok := s.Contributors[p.ID]
		if !ok {
			return fmt.Errorf("no such contributor")
		}
		if strings.TrimSpace(p.Name) == "" {
			return fmt.Errorf("a contributor name cannot be empty")
		}
		c.Name = p.Name
		return nil
	}

	return fmt.Errorf("no handler for action %q", e.Action)
}

// addNode creates a node and files it under its parent.
func (s *State) addNode(e *event.Event, at time.Time, kind Kind, parent, title string) error {
	p := &e.Payload
	if p.ID == "" {
		return fmt.Errorf("a node needs an id")
	}
	if _, exists := s.Nodes[p.ID]; exists {
		// Replaying the same log twice, or a duplicated line, must not create
		// two nodes with one id.
		return fmt.Errorf("node already exists")
	}
	if strings.TrimSpace(title) == "" {
		return fmt.Errorf("a %s needs a title", kind)
	}
	parent = s.deref(parent)
	if err := s.checkContainment(kind, parent); err != nil {
		return err
	}

	n := &Node{
		ID:        p.ID,
		Kind:      kind,
		Parent:    parent,
		Rank:      p.Rank,
		Title:     title,
		Created:   at,
		Updated:   at,
		CreatedBy: e.UserID,
		UpdatedBy: e.UserID,
	}
	s.Nodes[n.ID] = n
	if parent != "" {
		s.children[parent] = append(s.children[parent], n.ID)
		s.markDirty(parent)
	}
	return nil
}

// checkContainment enforces the tree's shape: notebooks sit in the workspace,
// notes and tasks sit in a notebook as peers.
//
// Notebooks deliberately do not nest. A single level keeps every node exactly
// two hops from the root, which is what lets the interface be a fixed two-pane
// layout and the command line address a note by notebook and title.
func (s *State) checkContainment(kind Kind, parent string) error {
	if kind == KindWorkspace {
		if parent != "" {
			return fmt.Errorf("the workspace cannot have a parent")
		}
		return nil
	}

	container, err := s.node(parent)
	if err != nil {
		return fmt.Errorf("parent: %w", err)
	}
	if container.Deleted {
		return fmt.Errorf("its %s was deleted", container.Kind)
	}

	switch kind {
	case KindNotebook:
		if container.Kind != KindWorkspace {
			return fmt.Errorf("a notebook belongs in the workspace, not in a %s", container.Kind)
		}
	case KindNote, KindTask:
		if container.Kind != KindNotebook {
			return fmt.Errorf("a %s belongs in a notebook, not in a %s", kind, container.Kind)
		}
	}
	return nil
}

// move reparents or reorders a node.
func (s *State) move(e *event.Event, at time.Time) error {
	p := &e.Payload
	n, err := s.live(p.ID)
	if err != nil {
		return err
	}
	if n.Kind == KindWorkspace {
		return fmt.Errorf("the workspace cannot be moved")
	}
	if p.Rank == "" {
		return fmt.Errorf("a move needs a rank")
	}

	target := s.deref(p.Parent)
	if target == "" {
		target = n.Parent
	}
	if target != n.Parent {
		if err := s.checkContainment(n.Kind, target); err != nil {
			return err
		}
		// With a single level of notebooks a node cannot become its own
		// ancestor, but the check is cheap and would be the difference between
		// a bug and an infinite loop if nesting is ever allowed.
		if s.isDescendant(target, n.ID) {
			return fmt.Errorf("cannot move a node inside itself")
		}
		s.unlinkChild(n.Parent, n.ID)
		s.children[target] = append(s.children[target], n.ID)
		s.markDirty(n.Parent)
		n.Parent = target
	}
	s.markDirty(n.Parent)

	n.Rank = p.Rank
	s.touch(n, e, at)
	return nil
}

// isDescendant reports whether id is at or below ancestor.
func (s *State) isDescendant(id, ancestor string) bool {
	for cur := id; cur != ""; {
		if cur == ancestor {
			return true
		}
		n, ok := s.Nodes[cur]
		if !ok {
			return false
		}
		cur = n.Parent
	}
	return false
}

// unlinkChild removes a child from its parent's list.
func (s *State) unlinkChild(parent, child string) {
	kids := s.children[parent]
	if i := slices.Index(kids, child); i >= 0 {
		s.children[parent] = slices.Delete(kids, i, i+1)
	}
}

// setDeleted marks a node and everything beneath it, keeping the tag counts in
// step. Deleting a notebook has to take its notes and tasks with it, or they
// would survive as live nodes with no reachable parent.
func (s *State) setDeleted(n *Node, deleted bool) {
	if n.Deleted == deleted {
		return
	}
	n.Deleted = deleted

	for _, tag := range n.Tags {
		if deleted {
			s.dropTag(tag)
		} else {
			s.tags[tag]++
		}
	}
	for _, id := range s.children[n.ID] {
		if child, ok := s.Nodes[id]; ok {
			s.setDeleted(child, deleted)
		}
	}
}

// dropTag decrements a tag's use count and forgets it at zero, so the tag list
// only ever shows tags actually in use.
func (s *State) dropTag(tag string) {
	if s.tags[tag] <= 1 {
		delete(s.tags, tag)
		return
	}
	s.tags[tag]--
}

// touch records who last changed a node and when.
func (s *State) touch(n *Node, e *event.Event, at time.Time) {
	n.Updated = at
	n.UpdatedBy = e.UserID
}

// deref follows a superseded workspace id to the surviving one, leaving every
// other id alone.
func (s *State) deref(id string) string {
	if target, ok := s.aliases[id]; ok {
		return target
	}
	return id
}

// node returns a node by id, deleted or not.
func (s *State) node(id string) (*Node, error) {
	id = s.deref(id)
	n, ok := s.Nodes[id]
	if !ok {
		return nil, fmt.Errorf("no node %s", ulid.Short(id, 6))
	}
	return n, nil
}

// live returns a node that has not been deleted.
func (s *State) live(id string) (*Node, error) {
	n, err := s.node(id)
	if err != nil {
		return nil, err
	}
	if n.Deleted {
		return nil, fmt.Errorf("node %s was deleted", ulid.Short(id, 6))
	}
	return n, nil
}

// task returns a live node that is a task, rejecting task-only operations
// aimed at a note. This is the check that makes notes and tasks genuinely
// distinct kinds rather than one kind with optional fields.
func (s *State) task(id string) (*Node, error) {
	n, err := s.live(id)
	if err != nil {
		return nil, err
	}
	if n.Kind != KindTask {
		return nil, fmt.Errorf("that is a %s, not a task", n.Kind)
	}
	return n, nil
}
