// Package session is the write side of gnotes: it turns an intention like
// "add a task to this notebook" into the events that record it.
//
// It is the only place that mints event ids, chains references and resolves
// ranks. Both the command line and the interactive interface go through it, so
// the two cannot drift apart in what they write to the log.
package session

import (
	"errors"
	"fmt"
	"time"

	"github.com/shakfu/gnotes/internal/event"
	"github.com/shakfu/gnotes/internal/rank"
	"github.com/shakfu/gnotes/internal/state"
	"github.com/shakfu/gnotes/internal/store"
	"github.com/shakfu/gnotes/internal/ulid"
)

// Session is an open project together with the identity writing to it.
//
// Events are staged in memory and applied to the in-memory tree as they are
// made, so a command that emits several events sees its own earlier ones.
// Commit writes the batch to disk in one append, which is what makes a
// multi-event command all-or-nothing.
type Session struct {
	Project *store.Project
	Actor   store.Actor

	// State is the materialized tree. It is replaced on Reload and mutated in
	// place as events are staged.
	State *state.State

	// Problems are events that could not be applied during the last load.
	Problems []state.Problem

	// Skipped counts events written by a newer gnotes.
	Skipped map[event.Action]int

	// log is every event known to this session, staged ones included, in
	// canonical order.
	log []event.Event

	// pending are the events staged since the last Commit.
	pending []event.Event

	gen *ulid.Generator

	// now is the clock, replaced in tests so that relative dates are stable.
	now func() time.Time
}

// Open discovers the project containing dir, loads its log and materializes
// the tree.
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

// Reload re-reads the log from disk, discarding any uncommitted events.
func (s *Session) Reload() error {
	loaded, err := store.Load(s.Project)
	if err != nil {
		return err
	}
	s.log = loaded.Events
	s.Skipped = loaded.Skipped
	s.pending = nil
	s.State, s.Problems = state.Materialize(s.log)
	return nil
}

// SetClock replaces the session's clock.
//
// The event id generator is replaced along with it. An event's id encodes when
// it happened, so a session whose clock was overridden only for parsing would
// still stamp events with the wall clock, and its history would not match its
// own idea of the present.
func (s *Session) SetClock(f func() time.Time) {
	s.now = f
	s.gen = ulid.NewGeneratorAt(f)
}

// Now returns the session's current time.
func (s *Session) Now() time.Time { return s.now() }

// Pending reports how many events are staged but not yet written.
func (s *Session) Pending() int { return len(s.pending) }

// Log returns every event this session knows about, in canonical order.
func (s *Session) Log() []event.Event { return s.log }

// Commit appends the staged events to the actor's log.
func (s *Session) Commit() error {
	if len(s.pending) == 0 {
		return nil
	}
	if err := store.Append(s.Project, s.Actor, s.pending); err != nil {
		return err
	}
	s.pending = nil
	return nil
}

// emit stages one event: it mints the id, chains it to the current edge of the
// log, applies it to the tree and records it for the next Commit.
//
// Applying immediately is what lets a command emit a creation event and then
// operate on the node it just created without a round trip through disk.
func (s *Session) emit(action event.Action, p event.Payload) (*event.Event, error) {
	ref := event.EdgeRef(s.log)

	// The new id must sort after the edge. On a machine whose clock is behind
	// the one that wrote the last event, taking the wall clock alone would
	// produce an id that sorts before events already in the log, silently
	// reordering replay.
	var floor uint64
	if ref != "" {
		if ms, err := ulid.Time(ref); err == nil {
			floor = ms + 1
		}
	}

	e := event.Event{
		ID:       s.gen.NewAfter(floor),
		Ref:      ref,
		Action:   action,
		Payload:  p,
		UserID:   s.Actor.ID,
		UserName: s.Actor.Name,
	}

	if err := s.State.Apply(&e); err != nil {
		return nil, err
	}
	s.log = append(s.log, e)
	s.pending = append(s.pending, e)
	return &s.log[len(s.log)-1], nil
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
		return errors.New("no user configured; run 'gnotes init'")
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
func (s *Session) place(parent, moving string, pos rank.Position) (string, error) {
	r, err := rank.Resolve(s.State.Siblings(parent, moving), pos)
	if err == nil {
		return r, nil
	}
	if !errors.Is(err, rank.ErrExhausted) {
		return "", err
	}

	if err := s.rebalance(parent); err != nil {
		return "", err
	}
	r, err = rank.Resolve(s.State.Siblings(parent, moving), pos)
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
