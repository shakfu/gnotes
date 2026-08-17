package session

import (
	"errors"
	"fmt"
	"strings"

	"github.com/shakfu/gnotes/internal/event"
	"github.com/shakfu/gnotes/internal/rank"
	"github.com/shakfu/gnotes/internal/state"
	"github.com/shakfu/gnotes/internal/ulid"
)

// NewNotebook creates a notebook in the workspace.
func (s *Session) NewNotebook(name string) (*state.Node, error) {
	if s.State.Workspace == "" {
		return nil, errors.New("this project has no workspace; run 'gnotes init'")
	}
	if err := s.ensureContributor(); err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("a notebook needs a name")
	}

	r, err := s.place(s.State.Workspace, "", rank.End())
	if err != nil {
		return nil, err
	}
	e, err := s.emit(event.AddNotebook, event.Payload{
		ID: s.gen.New(), Parent: s.State.Workspace, Name: name, Rank: r,
	})
	if err != nil {
		return nil, err
	}
	return s.State.Get(e.Payload.ID), nil
}

// NewNote creates a note in a notebook.
func (s *Session) NewNote(notebook, title, body string) (*state.Node, error) {
	return s.newEntry(event.AddNote, notebook, title, body)
}

// NewTask creates a task in a notebook.
func (s *Session) NewTask(notebook, title, body string) (*state.Node, error) {
	return s.newEntry(event.AddTask, notebook, title, body)
}

// newEntry creates a note or a task, which differ only in their action.
func (s *Session) newEntry(action event.Action, notebook, title, body string) (*state.Node, error) {
	if err := s.ensureContributor(); err != nil {
		return nil, err
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, errors.New("a title is required")
	}

	nb, err := s.State.Resolve(notebook, state.KindNotebook)
	if err != nil {
		return nil, err
	}

	r, err := s.place(nb.ID, "", rank.End())
	if err != nil {
		return nil, err
	}
	e, err := s.emit(action, event.Payload{
		ID: s.gen.New(), Parent: nb.ID, Title: title, Rank: r,
	})
	if err != nil {
		return nil, err
	}
	id := e.Payload.ID

	// The body is a separate event rather than a field on the creation event,
	// so that editing a body and creating a node are the same operation to
	// replay and produce the same history.
	if body != "" {
		if _, err := s.emit(event.EditBody, event.Payload{ID: id, Body: body}); err != nil {
			return nil, err
		}
	}
	return s.State.Get(id), nil
}

// DefaultNotebook returns the notebook a new entry goes to when none is named:
// the first by rank, created on demand if the project has none.
//
// Creating one rather than refusing means the very first note a user writes
// does not have to be preceded by a notebook command.
func (s *Session) DefaultNotebook() (*state.Node, error) {
	if nbs := s.State.Notebooks(); len(nbs) > 0 {
		return nbs[0], nil
	}
	return s.NewNotebook("inbox")
}

// SetTitle renames a node.
func (s *Session) SetTitle(n *state.Node, title string) error {
	title = strings.TrimSpace(title)
	if title == "" {
		return errors.New("a title cannot be empty")
	}
	if n.Title == title {
		return nil
	}
	if err := s.ensureContributor(); err != nil {
		return err
	}
	// Notebooks are named, notes and tasks are titled. Both fields are sent so
	// the log line reads correctly whichever kind it names.
	p := event.Payload{ID: n.ID, Title: title}
	if n.Kind == state.KindNotebook {
		p = event.Payload{ID: n.ID, Name: title}
	}
	_, err := s.emit(event.EditTitle, p)
	return err
}

// SetBody replaces a node's markdown content.
func (s *Session) SetBody(n *state.Node, body string) error {
	if n.Body == body {
		return nil
	}
	if err := s.ensureContributor(); err != nil {
		return err
	}
	_, err := s.emit(event.EditBody, event.Payload{ID: n.ID, Body: body})
	return err
}

// SetStatus moves a task along.
func (s *Session) SetStatus(n *state.Node, status state.Status) error {
	if n.Kind != state.KindTask {
		return fmt.Errorf("%q is a %s, not a task", n.Title, n.Kind)
	}
	if n.Status == status {
		return nil
	}
	if err := s.ensureContributor(); err != nil {
		return err
	}
	_, err := s.emit(event.SetStatus, event.Payload{ID: n.ID, Status: status.String()})
	return err
}

// SetPriority ranks a task.
func (s *Session) SetPriority(n *state.Node, p state.Priority) error {
	if n.Kind != state.KindTask {
		return fmt.Errorf("%q is a %s, not a task", n.Title, n.Kind)
	}
	if n.Priority == p {
		return nil
	}
	if err := s.ensureContributor(); err != nil {
		return err
	}
	// A cleared priority is the empty string, which ParsePriority reads back
	// as PriorityNone.
	_, err := s.emit(event.SetPriority, event.Payload{ID: n.ID, Priority: p.String()})
	return err
}

// SetDue dates a task. The due string is whatever the user typed; relative
// words are resolved here, against the session clock, so that the event
// records an absolute date.
func (s *Session) SetDue(n *state.Node, due string) error {
	if n.Kind != state.KindTask {
		return fmt.Errorf("%q is a %s, not a task", n.Title, n.Kind)
	}
	when, ok := state.ParseDue(due, s.now())
	if !ok {
		return fmt.Errorf("could not read %q as a date; try 2026-08-17, tomorrow, or friday", due)
	}
	if n.Due.Equal(when) {
		return nil
	}
	if err := s.ensureContributor(); err != nil {
		return err
	}
	_, err := s.emit(event.SetDue, event.Payload{ID: n.ID, Due: state.FormatDue(when)})
	return err
}

// AddTag attaches a tag, normalising it first.
func (s *Session) AddTag(n *state.Node, tag string) error {
	norm := state.NormalizeTag(tag)
	if norm == "" {
		return fmt.Errorf("%q is not a usable tag", tag)
	}
	if n.HasTag(norm) {
		return nil
	}
	if err := s.ensureContributor(); err != nil {
		return err
	}
	_, err := s.emit(event.AddTag, event.Payload{ID: n.ID, Tag: norm})
	return err
}

// RemoveTag detaches a tag.
func (s *Session) RemoveTag(n *state.Node, tag string) error {
	norm := state.NormalizeTag(tag)
	if !n.HasTag(norm) {
		return nil
	}
	if err := s.ensureContributor(); err != nil {
		return err
	}
	_, err := s.emit(event.RemoveTag, event.Payload{ID: n.ID, Tag: norm})
	return err
}

// Assign puts a person on a task, looking them up by name or id.
func (s *Session) Assign(n *state.Node, who string) error {
	if n.Kind != state.KindTask {
		return fmt.Errorf("%q is a %s, not a task", n.Title, n.Kind)
	}
	id, err := s.contributorID(who)
	if err != nil {
		return err
	}
	if err := s.ensureContributor(); err != nil {
		return err
	}
	_, err = s.emit(event.AddAssignee, event.Payload{ID: n.ID, Assignee: id})
	return err
}

// Unassign takes a person off a task.
func (s *Session) Unassign(n *state.Node, who string) error {
	if n.Kind != state.KindTask {
		return fmt.Errorf("%q is a %s, not a task", n.Title, n.Kind)
	}
	id, err := s.contributorID(who)
	if err != nil {
		return err
	}
	if err := s.ensureContributor(); err != nil {
		return err
	}
	_, err = s.emit(event.RemoveAssignee, event.Payload{ID: n.ID, Assignee: id})
	return err
}

// contributorID resolves a person by name, by id, or by the word "me".
//
// A name that matches nobody is an error rather than a new contributor:
// contributor records are created when a person first writes to the project,
// and inventing one here would let a typo become a permanent phantom.
func (s *Session) contributorID(who string) (string, error) {
	who = strings.TrimSpace(who)
	if who == "" {
		return "", errors.New("name someone to assign")
	}
	if strings.EqualFold(who, "me") {
		return s.Actor.ID, nil
	}
	if id, ok := s.State.FindContributor(who); ok {
		return id, nil
	}
	if ulid.Valid(who) {
		return ulid.Canonical(who), nil
	}

	known := make([]string, 0, len(s.State.Contributors))
	for _, c := range s.State.Contributors {
		known = append(known, c.Name)
	}
	if len(known) == 0 {
		return "", fmt.Errorf("nobody named %q; this project has no contributors yet", who)
	}
	return "", fmt.Errorf("nobody named %q; this project has %s", who, strings.Join(known, ", "))
}

// Link records a reference from one node to another, which is how a task
// points at the note it came out of.
func (s *Session) Link(from, to *state.Node) error {
	if from.ID == to.ID {
		return errors.New("a node cannot link to itself")
	}
	if err := s.ensureContributor(); err != nil {
		return err
	}
	_, err := s.emit(event.LinkNode, event.Payload{ID: from.ID, Target: to.ID})
	return err
}

// Unlink removes a reference.
func (s *Session) Unlink(from *state.Node, target string) error {
	if err := s.ensureContributor(); err != nil {
		return err
	}
	_, err := s.emit(event.UnlinkNode, event.Payload{ID: from.ID, Target: target})
	return err
}

// Move reparents or reorders a node. An empty notebook keeps it where it is
// and only changes its position among its siblings.
func (s *Session) Move(n *state.Node, notebook string, pos rank.Position) error {
	if err := s.ensureContributor(); err != nil {
		return err
	}

	parent := n.Parent
	if notebook != "" {
		nb, err := s.State.Resolve(notebook, state.KindNotebook)
		if err != nil {
			return err
		}
		parent = nb.ID
	}
	if n.Kind == state.KindNotebook {
		parent = s.State.Workspace
	}

	r, err := s.place(parent, n.ID, pos)
	if err != nil {
		return err
	}
	_, err = s.emit(event.MoveNode, event.Payload{ID: n.ID, Parent: parent, Rank: r})
	return err
}

// Delete tombstones a node and everything under it.
func (s *Session) Delete(n *state.Node) error {
	if err := s.ensureContributor(); err != nil {
		return err
	}
	_, err := s.emit(event.DeleteNode, event.Payload{ID: n.ID})
	return err
}

// Restore brings a tombstoned node back.
func (s *Session) Restore(id string) error {
	n := s.State.Get(id)
	if n == nil {
		return fmt.Errorf("no node %s", ulid.Short(id, 6))
	}
	if !n.Deleted {
		return fmt.Errorf("%q is not deleted", n.Title)
	}
	if err := s.ensureContributor(); err != nil {
		return err
	}
	_, err := s.emit(event.RestoreNode, event.Payload{ID: n.ID})
	return err
}

// At returns the tree as it stood at a past moment, replayed from the same
// log. Nothing is stored for this; it is a prefix of the events already in
// memory.
func (s *Session) At(cutoff int64) (*state.State, []state.Problem) {
	before, _ := event.SplitAt(s.log, uint64(cutoff))
	return state.Materialize(before)
}
