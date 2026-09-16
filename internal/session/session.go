// Package session is the write side of gwiki: it turns an intention like
// "add a task to this notebook" into changes to the stored tree.
//
// A command stages operations, each applied at once to the in-memory tree and
// checked against its rules, so a command sees its own earlier steps. Commit
// writes the nodes those operations changed in one transaction. It is the only
// place that mints ids and resolves ranks; every front end goes through it, so
// none can mean something different by an operation.
package session

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/shakfu/gwiki/internal/event"
	"github.com/shakfu/gwiki/internal/rank"
	"github.com/shakfu/gwiki/internal/state"
	"github.com/shakfu/gwiki/internal/store"
	"github.com/shakfu/gwiki/internal/ulid"
)

// Session is an open project together with the identity writing to it.
type Session struct {
	Project *store.Project
	Actor   store.Actor

	// State is the tree. It is replaced on Reload and mutated in place as
	// operations are staged.
	State *state.State

	// Problems are stored rows that did not fit the tree at the last load.
	Problems []state.Problem

	// pending counts the operations staged since the last Commit.
	pending int

	// nodes, contributors and loadProblems are the stored rows as of the last
	// load or commit, so Rollback can rebuild the tree without the database.
	nodes        map[string]state.Node
	contributors map[string]state.Contributor
	loadProblems []state.Problem

	// seen is the database head the tree reflects: read by the last successful
	// load, and advanced past this session's own commits. Any other difference
	// is a write by another process.
	seen store.Snapshot

	gen *ulid.Generator

	// now is the clock, replaced in tests so that relative dates are stable.
	now func() time.Time
}

// Open discovers the project containing dir and loads it.
func Open(dir string, actor store.Actor) (*Session, error) {
	p, err := store.Discover(dir)
	if err != nil {
		return nil, err
	}
	return OpenProject(p, actor)
}

// OpenProject loads an already-located project.
func OpenProject(p *store.Project, actor store.Actor) (*Session, error) {
	s := &Session{
		Project: p,
		Actor:   actor,
		gen:     ulid.NewGenerator(),
		now:     time.Now,
	}
	return s, s.Reload()
}

// Reload re-reads the database, discarding any uncommitted operations.
//
// A failed load leaves the session as it was, still marked out of date, so the
// next Refresh tries again.
func (s *Session) Reload() error {
	rows, err := store.Load(s.Project)
	if err != nil {
		return err
	}
	s.seen = rows.Head
	s.nodes = make(map[string]state.Node, len(rows.Nodes))
	for _, n := range rows.Nodes {
		s.nodes[n.ID] = n
	}
	s.contributors = make(map[string]state.Contributor, len(rows.Contributors))
	for _, c := range rows.Contributors {
		s.contributors[c.ID] = c
	}
	s.loadProblems = rows.Problems
	s.rebuild()
	return nil
}

// rebuild replaces the tree with one built from the committed rows.
func (s *Session) rebuild() {
	nodes := make([]state.Node, 0, len(s.nodes))
	for _, n := range s.nodes {
		nodes = append(nodes, n)
	}
	contributors := make([]state.Contributor, 0, len(s.contributors))
	for _, c := range s.contributors {
		contributors = append(contributors, c)
	}
	st, problems := state.Build(nodes, contributors)
	s.State, s.Problems, s.pending = st, append(slices.Clone(s.loadProblems), problems...), 0
}

// Changed reports whether the database differs from what the tree reflects,
// which means another process has written.
func (s *Session) Changed() bool {
	return store.Snap(s.Project) != s.seen
}

// Refresh reloads the session if another process has written, reporting
// whether it did. Staged operations are discarded by a reload, so call it
// between commands, never inside one.
func (s *Session) Refresh() (bool, error) {
	if !s.Changed() {
		return false, nil
	}
	return true, s.Reload()
}

// SetClock replaces the session's clock, and the id generator with it, so ids
// and stored times agree with the session's idea of the present.
func (s *Session) SetClock(f func() time.Time) {
	s.now = f
	s.gen = ulid.NewGeneratorAt(f)
}

// Now returns the session's current time.
func (s *Session) Now() time.Time { return s.now() }

// Pending reports how many operations are staged but not yet written.
func (s *Session) Pending() int { return s.pending }

// Commit writes every node and contributor the staged operations changed, in
// one transaction.
//
// A failed write rolls the staging back, so the operations of a command that
// could not be written are never carried into the next one.
func (s *Session) Commit() error {
	if s.pending == 0 {
		return nil
	}
	nodes, contributors := s.State.TakeChanged()
	prev, head, err := store.Write(s.Project, s.Actor, s.now(), nodes, contributors)
	if err != nil {
		s.Rollback()
		return err
	}
	// Only when nothing else was written since the load. Otherwise seen stays
	// behind, and the next Refresh loads the other write.
	if prev == s.seen {
		s.seen = head
	}
	for _, n := range nodes {
		c := *n
		c.Tags, c.Links, c.Assignees = slices.Clone(n.Tags), slices.Clone(n.Links), slices.Clone(n.Assignees)
		s.nodes[n.ID] = c
	}
	for _, c := range contributors {
		s.contributors[c.ID] = *c
	}
	s.pending = 0
	return nil
}

// Rollback discards the staged operations and rebuilds the tree from the
// committed rows, undoing a command that failed partway through.
func (s *Session) Rollback() {
	if s.pending == 0 {
		return
	}
	s.rebuild()
}

// emit stages one operation: it mints the id, applies it to the tree, and
// counts it for the next Commit.
func (s *Session) emit(action event.Action, p event.Payload) (*event.Event, error) {
	e := &event.Event{
		ID:       s.gen.New(),
		Action:   action,
		Payload:  p,
		UserID:   s.Actor.ID,
		UserName: s.Actor.Name,
	}
	if err := s.State.Apply(e); err != nil {
		return nil, err
	}
	s.pending++
	return e, nil
}

// Init creates the workspace and registers the actor, for a project whose log
// is still empty.
func (s *Session) Init(name string) error {
	if s.State.Workspace != "" {
		return errors.New("this project already has a workspace")
	}
	if _, err := s.emit(event.InitWorkspace, event.Payload{
		ID: s.gen.New(), Name: name, Rank: rank.Mid(),
	}); err != nil {
		return err
	}
	return s.ensureContributor()
}

// ensureContributor registers the actor in the project the first time they
// write to it, so their name renders for everyone who syncs the log.
func (s *Session) ensureContributor() error {
	if !s.Actor.Valid() {
		return errors.New("no user configured; run 'gwiki notes init'")
	}
	if c, ok := s.State.Contributors[s.Actor.ID]; ok {
		if c.Name == s.Actor.Name {
			return nil
		}
		// The person renamed themselves. Recording it keeps every historical
		// event attributed to the name they use now.
		_, err := s.emit(event.RenameContributor, event.Payload{ID: s.Actor.ID, Name: s.Actor.Name})
		return err
	}
	_, err := s.emit(event.CreateContributor, event.Payload{ID: s.Actor.ID, Name: s.Actor.Name})
	return err
}

// place resolves the rank for a node arriving at pos under parent,
// rebalancing the parent's children first if the space between two neighbours
// has run out.
//
// The rebalance is itself an event, so every machine replaying the log arrives
// at the same ranks. Respacing locally without recording it would leave two
// machines disagreeing about sibling order.
//
// A sibling with a malformed rank, which only a damaged or foreign log can
// contain, also triggers the rebalance. Placing against it would fail every
// insert under the parent, since no rank can be computed next to it.
func (s *Session) place(parent, moving string, pos rank.Position) (string, error) {
	siblings := s.State.Siblings(parent, moving)
	malformed := slices.ContainsFunc(siblings, func(sib rank.Sibling) bool { return !rank.Valid(sib.Rank) })

	if !malformed {
		r, err := rank.Resolve(siblings, pos)
		if err == nil {
			return r, nil
		}
		if !errors.Is(err, rank.ErrExhausted) {
			return "", err
		}
	}

	if err := s.rebalance(parent); err != nil {
		return "", err
	}
	r, err := rank.Resolve(s.State.Siblings(parent, moving), pos)
	if err != nil {
		return "", fmt.Errorf("could not make room under this parent: %w", err)
	}
	return r, nil
}

// rebalance respaces every child of a parent across the whole rank space.
func (s *Session) rebalance(parent string) error {
	kids := s.State.Children(parent)
	if len(kids) == 0 {
		return errors.New("nothing to rebalance")
	}

	spaced, err := rank.Spaced(len(kids))
	if err != nil {
		return err
	}
	ranks := make(map[string]string, len(kids))
	for i, n := range kids {
		ranks[n.ID] = spaced[i]
	}

	_, err = s.emit(event.Rebalance, event.Payload{Parent: parent, Ranks: ranks})
	return err
}
