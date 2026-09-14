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
	"slices"
	"time"

	"github.com/shakfu/gnotes/internal/event"
	"github.com/shakfu/gnotes/internal/rank"
	"github.com/shakfu/gnotes/internal/state"
	"github.com/shakfu/gnotes/internal/store"
	"github.com/shakfu/gnotes/internal/ulid"
)

// ClockTolerance is how far ahead of this machine's clock a new event may be
// dated to keep it after the last event in the log.
const ClockTolerance = 5 * time.Minute

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

	// Torn names the logs whose last record was incomplete when they were
	// read. One event was lost from each; see store.Load.
	Torn []string

	// log is every event known to this session, staged ones included, in
	// canonical order.
	log []event.Event

	// pending are the events staged since the last Commit.
	pending []event.Event

	// seen is the state of the logs on disk that the tree reflects: taken
	// before the last successful load, and advanced past this session's own
	// appends. Any other difference is a write by someone else.
	seen store.Snapshot

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
//
// A failed load leaves the session as it was, still marked out of date, so the
// next Refresh tries again.
func (s *Session) Reload() error {
	// Taken before reading, so a write that lands during the load is seen as a
	// change next time rather than absorbed unread.
	before := store.Snap(s.Project)
	loaded, err := store.Load(s.Project)
	if err != nil {
		return err
	}
	s.seen = before
	s.log = loaded.Events
	s.Skipped = loaded.Skipped
	s.Torn = loaded.Torn
	s.pending = nil
	s.State, s.Problems = state.Materialize(s.log)
	return nil
}

// Changed reports whether the logs on disk differ from what the tree reflects,
// which means another process or a sync has written.
func (s *Session) Changed() bool {
	return !store.Snap(s.Project).Equal(s.seen)
}

// Refresh reloads the session if another process has written, reporting
// whether it did. Staged events are discarded by a reload, so call it between
// commands, never inside one.
func (s *Session) Refresh() (bool, error) {
	if !s.Changed() {
		return false, nil
	}
	return true, s.Reload()
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
//
// A failed append rolls the staging back, so the events of a command that
// could not be written are never carried into the next one.
func (s *Session) Commit() error {
	if len(s.pending) == 0 {
		return nil
	}
	before := store.Snap(s.Project)
	grew, err := store.Append(s.Project, s.Actor, s.pending)
	if err != nil {
		s.Rollback()
		return err
	}
	s.pending = nil
	s.absorb(before, grew)
	return nil
}

// absorb advances seen past this session's own append, but only when nothing
// else changed: every other log must be as it was, and this author's log must
// have grown by exactly what was written. Otherwise seen is left behind, so the
// next Refresh reloads and picks up the other write.
func (s *Session) absorb(before store.Snapshot, grew int64) {
	if !before.Equal(s.seen) {
		return
	}
	after := store.Snap(s.Project)
	own := store.LogName(s.Actor)
	if after[own].Size != before[own].Size+grew {
		return
	}
	for name, stamp := range after {
		if name != own && before[name] != stamp {
			return
		}
	}
	files := len(before)
	if _, existed := before[own]; !existed {
		files++
	}
	if len(after) != files {
		return
	}
	s.seen = after
}

// Rollback discards the staged events and rebuilds the tree from what is
// committed, undoing a command that failed partway through.
//
// It re-materializes from the in-memory log rather than re-reading the disk,
// because a failed command wrote nothing: the committed prefix of the log is
// already the truth, and re-deriving from it cannot itself fail. Picking up
// another process's writes is Reload's job.
func (s *Session) Rollback() {
	if len(s.pending) == 0 {
		return
	}
	s.log = s.log[:len(s.log)-len(s.pending)]
	s.pending = nil
	s.State, s.Problems = state.Materialize(s.log)
}

// emit stages one event: it mints the id, chains it to the current edge of the
// log, applies it to the tree and records it for the next Commit.
//
// Applying immediately is what lets a command emit a creation event and then
// operate on the node it just created without a round trip through disk.
func (s *Session) emit(action event.Action, p event.Payload) (*event.Event, error) {
	ref := event.EdgeRef(s.log)

	// The new id sorts after the edge, so a clock slightly behind the one that
	// wrote the last event still dates events in order.
	//
	// The floor is capped at ClockTolerance past this machine's clock. An edge
	// further ahead comes from a clock that is wrong, and following it would
	// date every later event, by every author, in that future, which hides
	// them from --at. Replay order does not depend on the floor: an event
	// always follows the event it refs, whatever its id.
	var floor uint64
	if ref != "" {
		if ms, err := ulid.Time(ref); err == nil {
			floor = min(ms+1, uint64(s.now().Add(ClockTolerance).UnixMilli()))
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
