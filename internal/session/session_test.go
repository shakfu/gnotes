package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shakfu/gnotes/internal/event"
	"github.com/shakfu/gnotes/internal/rank"
	"github.com/shakfu/gnotes/internal/state"
	"github.com/shakfu/gnotes/internal/store"
	"github.com/shakfu/gnotes/internal/ulid"
)

var testClock = func() time.Time { return time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC) }

func newSession(t *testing.T) *Session {
	t.Helper()

	p, err := store.Init(t.TempDir(), "demo", testClock())
	if err != nil {
		t.Fatalf("store.Init: %v", err)
	}
	actor := store.Actor{ID: ulid.NewGenerator().New(), Name: "sa"}

	s, err := OpenProject(p, actor)
	if err != nil {
		t.Fatalf("OpenProject: %v", err)
	}
	s.SetClock(testClock)

	if err := s.Init("demo"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return s
}

// reopen commits and reloads from disk, proving that state survives the round
// trip rather than living only in memory.
func reopen(t *testing.T, s *Session) *Session {
	t.Helper()
	if err := s.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	again, err := OpenProject(s.Project, s.Actor)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	again.SetClock(testClock)
	if len(again.Problems) != 0 {
		t.Fatalf("problems after reload: %v", again.Problems)
	}
	return again
}

func TestInitCreatesWorkspaceAndContributor(t *testing.T) {
	s := newSession(t)

	if s.State.Workspace == "" {
		t.Fatal("no workspace")
	}
	if got := s.State.Contributor(s.Actor.ID); got != "sa" {
		t.Fatalf("Contributor = %q, want sa", got)
	}

	again := reopen(t, s)
	if again.State.Workspace != s.State.Workspace {
		t.Fatal("the workspace did not survive a reload")
	}
}

func TestInitRefusesASecondWorkspace(t *testing.T) {
	s := newSession(t)
	if err := s.Init("again"); err == nil {
		t.Fatal("Init created a second workspace")
	}
}

func TestNewNoteAndTaskRoundTripThroughDisk(t *testing.T) {
	s := newSession(t)

	nb, err := s.NewNotebook("work")
	if err != nil {
		t.Fatalf("NewNotebook: %v", err)
	}
	note, err := s.NewNote(nb.ID, "design sketch", "# Heading\n\nsome prose")
	if err != nil {
		t.Fatalf("NewNote: %v", err)
	}
	task, err := s.NewTask(nb.ID, "fix the lexer", "")
	if err != nil {
		t.Fatalf("NewTask: %v", err)
	}

	again := reopen(t, s)

	gotNote := again.State.Get(note.ID)
	if gotNote == nil || gotNote.Kind != state.KindNote || gotNote.Title != "design sketch" {
		t.Fatalf("note = %+v", gotNote)
	}
	if !strings.Contains(gotNote.Body, "some prose") {
		t.Fatalf("body = %q", gotNote.Body)
	}
	gotTask := again.State.Get(task.ID)
	if gotTask == nil || gotTask.Kind != state.KindTask || gotTask.Status != state.StatusOpen {
		t.Fatalf("task = %+v", gotTask)
	}
	// Notes and tasks are peers in one ordered list.
	if kids := again.State.Children(nb.ID); len(kids) != 2 {
		t.Fatalf("notebook has %d children, want 2", len(kids))
	}
}

// A notebook can be named rather than given by id, since that is what a person
// types.
func TestNewNoteAcceptsANotebookName(t *testing.T) {
	s := newSession(t)
	if _, err := s.NewNotebook("work"); err != nil {
		t.Fatal(err)
	}

	note, err := s.NewNote("work", "by name", "")
	if err != nil {
		t.Fatalf("NewNote: %v", err)
	}
	if s.State.Get(note.ID) == nil {
		t.Fatal("the note was not created")
	}
}

// The first note a user writes should not have to be preceded by a notebook
// command.
func TestDefaultNotebookIsCreatedOnDemand(t *testing.T) {
	s := newSession(t)

	nb, err := s.DefaultNotebook()
	if err != nil {
		t.Fatalf("DefaultNotebook: %v", err)
	}
	if nb.Title != "inbox" {
		t.Fatalf("default notebook = %q, want inbox", nb.Title)
	}

	// Once one exists it is reused rather than duplicated.
	again, err := s.DefaultNotebook()
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != nb.ID {
		t.Fatal("a second default notebook was created")
	}
}

func TestTaskFieldsRoundTrip(t *testing.T) {
	s := newSession(t)
	nb, _ := s.NewNotebook("work")
	task, _ := s.NewTask(nb.ID, "fix the lexer", "")

	if err := s.SetStatus(task, state.StatusDoing); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPriority(task, state.PriorityHigh); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDue(task, "2026-09-01"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddTag(task, "#Bug"); err != nil {
		t.Fatal(err)
	}
	if err := s.Assign(task, "me"); err != nil {
		t.Fatal(err)
	}

	got := reopen(t, s).State.Get(task.ID)
	if got.Status != state.StatusDoing || got.Priority != state.PriorityHigh {
		t.Fatalf("task = %+v", got)
	}
	if state.FormatDue(got.Due) != "2026-09-01" {
		t.Fatalf("Due = %v", got.Due)
	}
	if len(got.Tags) != 1 || got.Tags[0] != "bug" {
		t.Fatalf("Tags = %v", got.Tags)
	}
	if len(got.Assignees) != 1 || got.Assignees[0] != s.Actor.ID {
		t.Fatalf("Assignees = %v", got.Assignees)
	}
}

// A relative date must be resolved when the command is given, so the stored
// event means the same thing forever.
func TestSetDueResolvesRelativeWordsAtWriteTime(t *testing.T) {
	s := newSession(t)
	nb, _ := s.NewNotebook("work")
	task, _ := s.NewTask(nb.ID, "ship it", "")

	if err := s.SetDue(task, "tomorrow"); err != nil {
		t.Fatalf("SetDue: %v", err)
	}

	// The event on disk carries an absolute date, not the word.
	for _, e := range s.Log() {
		if e.Payload.Due != "" && strings.Contains(e.Payload.Due, "tomorrow") {
			t.Fatal("the log stored a relative date")
		}
	}
	if got := state.FormatDue(reopen(t, s).State.Get(task.ID).Due); got != "2026-08-18" {
		t.Fatalf("Due = %q, want 2026-08-18", got)
	}
}

func TestSetDueRejectsNonsense(t *testing.T) {
	s := newSession(t)
	nb, _ := s.NewNotebook("work")
	task, _ := s.NewTask(nb.ID, "ship it", "")

	if err := s.SetDue(task, "sometime soonish"); err == nil {
		t.Fatal("SetDue accepted nonsense")
	}
}

// Task verbs must refuse a note; this is the distinction between the kinds.
func TestTaskVerbsRefuseNotes(t *testing.T) {
	s := newSession(t)
	nb, _ := s.NewNotebook("work")
	note, _ := s.NewNote(nb.ID, "just a note", "")

	if err := s.SetStatus(note, state.StatusDone); err == nil {
		t.Error("SetStatus accepted a note")
	}
	if err := s.SetPriority(note, state.PriorityHigh); err == nil {
		t.Error("SetPriority accepted a note")
	}
	if err := s.SetDue(note, "2026-01-01"); err == nil {
		t.Error("SetDue accepted a note")
	}
	if err := s.Assign(note, "me"); err == nil {
		t.Error("Assign accepted a note")
	}
	// Nothing was written for any of them.
	if s.State.Get(note.ID).Status != state.StatusOpen {
		t.Error("a rejected verb still changed the note")
	}
}

// Notes and tasks both take tags and bodies; only task-specific fields are
// restricted.
func TestNotesTakeTagsAndBodies(t *testing.T) {
	s := newSession(t)
	nb, _ := s.NewNotebook("work")
	note, _ := s.NewNote(nb.ID, "design", "")

	if err := s.AddTag(note, "design"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetBody(note, "revised"); err != nil {
		t.Fatal(err)
	}

	got := reopen(t, s).State.Get(note.ID)
	if len(got.Tags) != 1 || got.Body != "revised" {
		t.Fatalf("note = %+v", got)
	}
}

// A command that changes nothing should write nothing, or the log fills with
// events that mean "no change".
func TestNoOpCommandsWriteNothing(t *testing.T) {
	s := newSession(t)
	nb, _ := s.NewNotebook("work")
	task, _ := s.NewTask(nb.ID, "ship it", "")
	if err := s.AddTag(task, "bug"); err != nil {
		t.Fatal(err)
	}
	if err := s.Commit(); err != nil {
		t.Fatal(err)
	}

	before := len(s.Log())
	if err := s.SetTitle(task, "ship it"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetStatus(task, state.StatusOpen); err != nil {
		t.Fatal(err)
	}
	if err := s.AddTag(task, "#Bug"); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveTag(task, "never-applied"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetBody(task, ""); err != nil {
		t.Fatal(err)
	}

	if got := len(s.Log()); got != before {
		t.Fatalf("%d events written for no-op commands", got-before)
	}
	if s.Pending() != 0 {
		t.Fatalf("%d events pending after no-op commands", s.Pending())
	}
}

func TestMoveReordersAndReparents(t *testing.T) {
	s := newSession(t)
	work, _ := s.NewNotebook("work")
	personal, _ := s.NewNotebook("personal")

	first, _ := s.NewNote(work.ID, "first", "")
	second, _ := s.NewNote(work.ID, "second", "")

	if err := s.Move(first, "", rank.After(second.ID)); err != nil {
		t.Fatalf("Move: %v", err)
	}
	if got := s.State.Children(work.ID); got[0].ID != second.ID {
		t.Fatalf("order = %v, want second first", got[0].Title)
	}

	if err := s.Move(first, "personal", rank.End()); err != nil {
		t.Fatalf("Move between notebooks: %v", err)
	}

	again := reopen(t, s)
	if got := again.State.Get(first.ID).Parent; got != personal.ID {
		t.Fatalf("Parent = %q, want the personal notebook", got)
	}
	if len(again.State.Children(work.ID)) != 1 {
		t.Fatal("the note is still in its old notebook")
	}
}

func TestDeleteAndRestore(t *testing.T) {
	s := newSession(t)
	nb, _ := s.NewNotebook("work")
	note, _ := s.NewNote(nb.ID, "temporary", "")

	if err := s.Delete(note); err != nil {
		t.Fatal(err)
	}
	if !s.State.Get(note.ID).Deleted {
		t.Fatal("the note was not deleted")
	}

	if err := s.Restore(note.ID); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if reopen(t, s).State.Get(note.ID).Deleted {
		t.Fatal("the note was not restored")
	}
}

func TestRestoreRejectsALiveNode(t *testing.T) {
	s := newSession(t)
	nb, _ := s.NewNotebook("work")
	note, _ := s.NewNote(nb.ID, "alive", "")

	if err := s.Restore(note.ID); err == nil {
		t.Fatal("Restore accepted a live node")
	}
	if err := s.Restore(ulid.NewGenerator().New()); err == nil {
		t.Fatal("Restore accepted an unknown id")
	}
}

func TestLinkAndUnlink(t *testing.T) {
	s := newSession(t)
	nb, _ := s.NewNotebook("work")
	note, _ := s.NewNote(nb.ID, "design sketch", "")
	task, _ := s.NewTask(nb.ID, "implement it", "")

	if err := s.Link(task, note); err != nil {
		t.Fatal(err)
	}
	again := reopen(t, s)
	if got := again.State.Get(task.ID).Links; len(got) != 1 || got[0] != note.ID {
		t.Fatalf("Links = %v", got)
	}
	if back := again.State.Backlinks(note.ID); len(back) != 1 || back[0].ID != task.ID {
		t.Fatalf("Backlinks = %v", back)
	}

	if err := again.Unlink(again.State.Get(task.ID), note.ID); err != nil {
		t.Fatal(err)
	}
	if len(reopen(t, again).State.Get(task.ID).Links) != 0 {
		t.Fatal("the link survived")
	}
}

func TestSelfLinkIsRejected(t *testing.T) {
	s := newSession(t)
	nb, _ := s.NewNotebook("work")
	note, _ := s.NewNote(nb.ID, "solo", "")

	if err := s.Link(note, note); err == nil {
		t.Fatal("Link accepted a self-reference")
	}
}

func TestAssignRejectsAnUnknownPerson(t *testing.T) {
	s := newSession(t)
	nb, _ := s.NewNotebook("work")
	task, _ := s.NewTask(nb.ID, "ship it", "")

	err := s.Assign(task, "nobody-by-that-name")
	if err == nil {
		t.Fatal("Assign invented a contributor")
	}
	// The message should list who does exist, so the typo is obvious.
	if !strings.Contains(err.Error(), "sa") {
		t.Fatalf("error = %v, want it to name the known contributors", err)
	}
}

// Ranks run out after enough insertions at the same spot. The rebalance must
// be automatic and recorded, so every machine agrees on the new order.
func TestRankExhaustionTriggersARecordedRebalance(t *testing.T) {
	s := newSession(t)
	nb, _ := s.NewNotebook("work")

	first, err := s.NewNote(nb.ID, "first", "")
	if err != nil {
		t.Fatal(err)
	}
	last, err := s.NewNote(nb.ID, "last", "")
	if err != nil {
		t.Fatal(err)
	}

	// Repeatedly insert straight after the first entry, which halves the gap
	// between it and its neighbour each time. Appends and prepends step instead
	// and would never run out.
	for i := 0; i < 120; i++ {
		n, err := s.NewNote(nb.ID, "note", "")
		if err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
		if err := s.Move(n, "", rank.After(first.ID)); err != nil {
			t.Fatalf("move %d: %v", i, err)
		}
	}

	rebalances := 0
	for _, e := range s.Log() {
		if e.Action == "rebalance.children" {
			rebalances++
		}
	}
	if rebalances == 0 {
		t.Fatal("the rank space never ran out; the test is not exercising rebalancing")
	}

	// The order must survive the rebalance and the round trip.
	again := reopen(t, s)
	kids := again.State.Children(nb.ID)
	if len(kids) != 122 {
		t.Fatalf("got %d children, want 122", len(kids))
	}
	if kids[0].ID != first.ID || kids[len(kids)-1].ID != last.ID {
		t.Fatal("the first and last entries moved during rebalancing")
	}
	for i := 1; i < len(kids); i++ {
		if kids[i-1].Rank >= kids[i].Rank {
			t.Fatalf("ranks not ascending at %d", i)
		}
	}
}

// Appending is the default placement and must not rewrite the notebook's ranks
// as it grows.
func TestAppendingDoesNotRebalance(t *testing.T) {
	s := newSession(t)
	s.NewNotebook("work")
	for i := 0; i < 1000; i++ {
		if _, err := s.NewNote("work", "n", ""); err != nil {
			t.Fatal(err)
		}
	}
	for _, e := range s.Log() {
		if e.Action == "rebalance.children" {
			t.Fatal("1000 appends triggered a rebalance")
		}
	}
}

// A malformed rank from another log must not block every insert under its
// parent.
func TestAMalformedSiblingRankIsRebalancedAway(t *testing.T) {
	s := newSession(t)
	nb, _ := s.NewNotebook("work")
	if err := s.Commit(); err != nil {
		t.Fatal(err)
	}

	bad := event.Event{ID: ulid.NewGenerator().New(), Ref: event.EdgeRef(s.Log()), Action: event.AddNote,
		Payload: event.Payload{ID: ulid.NewGenerator().New(), Parent: nb.ID, Title: "foreign", Rank: "NOT-A-RANK-zzzzzzzzzzzzzz"}}
	if _, err := store.Append(s.Project, store.Actor{ID: ulid.NewGenerator().New(), Name: "bob"}, []event.Event{bad}); err != nil {
		t.Fatal(err)
	}
	if err := s.Reload(); err != nil {
		t.Fatal(err)
	}

	for _, pos := range []rank.Position{rank.End(), rank.Start()} {
		n, err := s.NewNote("work", "mine", "")
		if err != nil {
			t.Fatalf("insert next to a malformed rank: %v", err)
		}
		if err := s.Move(n, "", pos); err != nil {
			t.Fatalf("move to %v: %v", pos.At, err)
		}
	}
	for _, kid := range s.State.Children(nb.ID) {
		if !rank.Valid(kid.Rank) {
			t.Fatalf("%q still has malformed rank %q", kid.Title, kid.Rank)
		}
	}
}

// Events are chained, so replay order is a property of the data. Every event
// this session wrote must refer to the one before it.
func TestEmittedEventsFormAChain(t *testing.T) {
	s := newSession(t)
	nb, _ := s.NewNotebook("work")
	s.NewNote(nb.ID, "one", "")
	s.NewTask(nb.ID, "two", "")

	log := s.Log()
	if len(log) < 4 {
		t.Fatalf("only %d events", len(log))
	}
	if log[0].Ref != "" {
		t.Fatal("the first event has a reference")
	}
	for i := 1; i < len(log); i++ {
		if log[i].Ref != log[i-1].ID {
			t.Fatalf("event %d does not refer to its predecessor", i)
		}
		if log[i].ID <= log[i-1].ID {
			t.Fatalf("event %d does not sort after its predecessor", i)
		}
	}
}

// Commit is what makes a multi-event command all-or-nothing on disk.
func TestNothingReachesDiskUntilCommit(t *testing.T) {
	s := newSession(t)
	nb, _ := s.NewNotebook("work")
	if _, err := s.NewNote(nb.ID, "staged", "body"); err != nil {
		t.Fatal(err)
	}
	if s.Pending() == 0 {
		t.Fatal("nothing was staged")
	}

	// A separate session sees nothing yet.
	other, err := OpenProject(s.Project, s.Actor)
	if err != nil {
		t.Fatal(err)
	}
	if len(other.State.Notebooks()) != 0 {
		t.Fatal("uncommitted events were visible to another session")
	}

	if err := s.Commit(); err != nil {
		t.Fatal(err)
	}
	if s.Pending() != 0 {
		t.Fatal("Commit left events pending")
	}

	other, _ = OpenProject(s.Project, s.Actor)
	if len(other.State.Notebooks()) != 1 {
		t.Fatal("committed events are not visible")
	}
}

func TestReloadDiscardsUncommittedWork(t *testing.T) {
	s := newSession(t)
	nb, _ := s.NewNotebook("work")
	if err := s.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.NewNote(nb.ID, "discard me", ""); err != nil {
		t.Fatal(err)
	}

	if err := s.Reload(); err != nil {
		t.Fatal(err)
	}
	if s.Pending() != 0 {
		t.Fatal("Reload kept pending events")
	}
	if len(s.State.Children(nb.ID)) != 0 {
		t.Fatal("Reload kept an uncommitted note")
	}
}

// Two sessions writing as different people must both survive the merge; this
// is the property the whole per-author-file design exists for.
func TestTwoAuthorsMergeWithoutConflict(t *testing.T) {
	alice := newSession(t)
	nb, _ := alice.NewNotebook("shared")
	if err := alice.Commit(); err != nil {
		t.Fatal(err)
	}

	bob, err := OpenProject(alice.Project, store.Actor{ID: ulid.NewGenerator().New(), Name: "bob"})
	if err != nil {
		t.Fatal(err)
	}
	bob.SetClock(testClock)

	// Both write without seeing each other's work.
	if _, err := alice.NewTask(nb.ID, "alice's task", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := bob.NewTask(nb.ID, "bob's task", ""); err != nil {
		t.Fatal(err)
	}
	if err := alice.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := bob.Commit(); err != nil {
		t.Fatal(err)
	}

	merged, err := OpenProject(alice.Project, alice.Actor)
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.Problems) != 0 {
		t.Fatalf("problems after merge: %v", merged.Problems)
	}

	titles := map[string]bool{}
	for _, n := range merged.State.Children(nb.ID) {
		titles[n.Title] = true
	}
	if !titles["alice's task"] || !titles["bob's task"] {
		t.Fatalf("children = %v, want both people's tasks", titles)
	}
	if got := merged.State.Contributor(bob.Actor.ID); got != "bob" {
		t.Fatalf("bob's name = %q", got)
	}
}

// A machine whose clock is a little behind must still write ids that sort after
// what is already in the log, so dates read in order.
func TestALaggingClockStillAppendsInOrder(t *testing.T) {
	s := newSession(t)
	if _, err := s.NewNotebook("work"); err != nil {
		t.Fatal(err)
	}
	if err := s.Commit(); err != nil {
		t.Fatal(err)
	}

	behind, err := OpenProject(s.Project, s.Actor)
	if err != nil {
		t.Fatal(err)
	}
	behind.SetClock(func() time.Time { return testClock().Add(-time.Minute) })

	last := behind.Log()[len(behind.Log())-1].ID
	if _, err := behind.NewNotebook("later"); err != nil {
		t.Fatal(err)
	}

	for _, e := range behind.Log() {
		if e.Ref == last && e.ID <= last {
			t.Fatalf("event %s does not sort after the edge %s", e.ID, last)
		}
	}
}

// Time travel is a prefix of the log, replayed. Nothing is stored for it.
func TestAtReplaysAPastState(t *testing.T) {
	s := newSession(t)
	nb, _ := s.NewNotebook("work")
	early, _ := s.NewNote(nb.ID, "early", "")

	cutoff := early.Created.UnixMilli()

	if _, err := s.NewNote(nb.ID, "later", ""); err != nil {
		t.Fatal(err)
	}

	past, problems := s.At(cutoff)
	if len(problems) != 0 {
		t.Fatalf("problems: %v", problems)
	}
	if past.Get(early.ID) == nil {
		t.Fatal("the past state is missing a node that existed then")
	}

	for _, n := range past.List(state.Filter{}, state.OrderTitle) {
		if n.Title == "later" {
			t.Fatal("the past state contains a node created afterwards")
		}
	}
	// The present is unchanged.
	if len(s.State.Children(nb.ID)) != 2 {
		t.Fatal("time travel mutated the present")
	}
}

func TestRenamingYourselfIsRecorded(t *testing.T) {
	s := newSession(t)
	if _, err := s.NewNotebook("work"); err != nil {
		t.Fatal(err)
	}
	if err := s.Commit(); err != nil {
		t.Fatal(err)
	}

	renamed, err := OpenProject(s.Project, store.Actor{ID: s.Actor.ID, Name: "Shakeeb"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := renamed.NewNotebook("more"); err != nil {
		t.Fatal(err)
	}

	if got := reopen(t, renamed).State.Contributor(s.Actor.ID); got != "Shakeeb" {
		t.Fatalf("Contributor = %q, want the new name", got)
	}
}

func TestOpenReportsAMissingProject(t *testing.T) {
	_, err := Open(t.TempDir(), store.Actor{ID: ulid.NewGenerator().New(), Name: "sa"})
	if err == nil {
		t.Fatal("Open succeeded outside a project")
	}
}

// titled reports whether any node in the tree carries the title.
func titled(s *state.State, title string) bool {
	for _, n := range s.Nodes {
		if n.Title == title {
			return true
		}
	}
	return false
}

func TestRollbackDropsAHalfStagedCommand(t *testing.T) {
	s := newSession(t)
	nb, err := s.NewNotebook("work")
	if err != nil {
		t.Fatalf("NewNotebook: %v", err)
	}
	if err := s.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	n, err := s.NewTask(nb.ID, "half staged", "")
	if err != nil {
		t.Fatalf("NewTask: %v", err)
	}
	if err := s.SetDue(n, "the twelfth of never"); err == nil {
		t.Fatal("an unparseable due date was accepted")
	}

	s.Rollback()

	if s.Pending() != 0 {
		t.Fatalf("Pending = %d, want 0", s.Pending())
	}
	if titled(s.State, "half staged") {
		t.Fatal("the rolled back task is still in the tree")
	}

	if _, err := s.NewNote(nb.ID, "kept", ""); err != nil {
		t.Fatalf("NewNote: %v", err)
	}
	again := reopen(t, s)

	if titled(again.State, "half staged") {
		t.Fatal("a rolled back event reached disk on the next commit")
	}
	if !titled(again.State, "kept") {
		t.Fatal("the later note was not written")
	}
}

func TestCommitFailureDropsTheStagedEvents(t *testing.T) {
	s := newSession(t)
	nb, err := s.NewNotebook("work")
	if err != nil {
		t.Fatalf("NewNotebook: %v", err)
	}
	if err := s.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	if _, err := s.NewNote(nb.ID, "unwritable", ""); err != nil {
		t.Fatalf("NewNote: %v", err)
	}

	// Append refuses to write for an actor with no name, which fails the
	// commit without putting anything on disk to clean up.
	name := s.Actor.Name
	s.Actor.Name = ""
	if err := s.Commit(); err == nil {
		t.Fatal("the commit succeeded with an unconfigured actor")
	}
	s.Actor.Name = name

	if s.Pending() != 0 {
		t.Fatalf("Pending = %d, want 0", s.Pending())
	}
	if titled(s.State, "unwritable") {
		t.Fatal("the failed commit left its node in the tree")
	}

	if _, err := s.NewNote(nb.ID, "kept", ""); err != nil {
		t.Fatalf("NewNote: %v", err)
	}
	again := reopen(t, s)

	if titled(again.State, "unwritable") {
		t.Fatal("an event from the failed commit reached disk")
	}
	if !titled(again.State, "kept") {
		t.Fatal("the later note was not written")
	}
}

// secondAuthor opens the same project as someone else, the way another
// process or another machine's synced log would appear.
func secondAuthor(t *testing.T, s *Session, name string) *Session {
	t.Helper()
	other, err := OpenProject(s.Project, store.Actor{ID: ulid.NewGenerator().New(), Name: name})
	if err != nil {
		t.Fatal(err)
	}
	other.SetClock(testClock)
	return other
}

func TestOwnCommitIsNotAnOutsideChange(t *testing.T) {
	s := newSession(t)
	if err := s.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.NewNotebook("work"); err != nil {
		t.Fatal(err)
	}
	if err := s.Commit(); err != nil {
		t.Fatal(err)
	}
	if s.Changed() {
		t.Fatal("the session's own append was reported as an outside change")
	}
}

func TestRefreshPicksUpAnOutsideWrite(t *testing.T) {
	s := newSession(t)
	s.NewNotebook("work")
	if err := s.Commit(); err != nil {
		t.Fatal(err)
	}

	bob := secondAuthor(t, s, "bob")
	if _, err := bob.NewNote("work", "from bob", ""); err != nil {
		t.Fatal(err)
	}
	if err := bob.Commit(); err != nil {
		t.Fatal(err)
	}

	reloaded, err := s.Refresh()
	if err != nil || !reloaded {
		t.Fatalf("Refresh = %v, %v; want a reload", reloaded, err)
	}
	if !titled(s.State, "from bob") {
		t.Fatal("the outside write is missing after Refresh")
	}
	if again, _ := s.Refresh(); again {
		t.Fatal("a second Refresh reloaded with nothing new on disk")
	}
}

// An outside write that lands before this session's own commit must not be
// absorbed as if the session had written it.
func TestOutsideWriteBeforeOwnCommitIsNotAbsorbed(t *testing.T) {
	s := newSession(t)
	s.NewNotebook("work")
	if err := s.Commit(); err != nil {
		t.Fatal(err)
	}

	bob := secondAuthor(t, s, "bob")
	bob.NewNote("work", "from bob", "")
	if err := bob.Commit(); err != nil {
		t.Fatal(err)
	}

	if _, err := s.NewNote("work", "mine", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Commit(); err != nil {
		t.Fatal(err)
	}
	if !s.Changed() {
		t.Fatal("bob's write was absorbed by this session's commit")
	}
	if _, err := s.Refresh(); err != nil {
		t.Fatal(err)
	}
	if !titled(s.State, "from bob") || !titled(s.State, "mine") {
		t.Fatal("want both notes after Refresh")
	}
}

// Another process of the same author appends to the same file.
func TestSameAuthorOtherProcessIsNotAbsorbed(t *testing.T) {
	s := newSession(t)
	s.NewNotebook("work")
	if err := s.Commit(); err != nil {
		t.Fatal(err)
	}

	twin, err := OpenProject(s.Project, s.Actor)
	if err != nil {
		t.Fatal(err)
	}
	twin.SetClock(testClock)
	twin.NewNote("work", "from the other process", "")
	if err := twin.Commit(); err != nil {
		t.Fatal(err)
	}

	s.NewNote("work", "mine", "")
	if err := s.Commit(); err != nil {
		t.Fatal(err)
	}
	if !s.Changed() {
		t.Fatal("the other process's append to the same log was absorbed")
	}
}

func TestFailedReloadIsRetried(t *testing.T) {
	s := newSession(t)
	s.NewNotebook("work")
	if err := s.Commit(); err != nil {
		t.Fatal(err)
	}

	bad := filepath.Join(s.Project.EventsDir(), ulid.NewGenerator().New()+".mallory.jsonl")
	if err := os.WriteFile(bad, []byte("not json\n{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Refresh(); err == nil {
		t.Fatal("Refresh read a corrupt log without error")
	}
	if !s.Changed() {
		t.Fatal("a failed reload marked the session up to date")
	}

	if err := os.Remove(bad); err != nil {
		t.Fatal(err)
	}
	bob := secondAuthor(t, s, "bob")
	bob.NewNote("work", "after the repair", "")
	if err := bob.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Refresh(); err != nil {
		t.Fatalf("Refresh after the repair: %v", err)
	}
	if !titled(s.State, "after the repair") {
		t.Fatal("the retry did not load the repaired project")
	}
}

func TestOneLineFieldsRefuseControlCharacters(t *testing.T) {
	s := newSession(t)
	nb, err := s.NewNotebook("work")
	if err != nil {
		t.Fatal(err)
	}
	for _, title := range []string{"two\nlines", "evil\x1b[2J", "bell\x07"} {
		if _, err := s.NewNote("work", title, ""); err == nil {
			t.Errorf("NewNote accepted %q", title)
		}
		if _, err := s.NewNotebook(title); err == nil {
			t.Errorf("NewNotebook accepted %q", title)
		}
		if err := s.SetTitle(nb, title); err == nil {
			t.Errorf("SetTitle accepted %q", title)
		}
	}
	note, err := s.NewNote("work", "fine", "a body\nmay have lines\x09and tabs")
	if err != nil {
		t.Fatalf("a multi-line body was refused: %v", err)
	}
	if err := s.AddTag(note, "bad\x1btag"); err == nil {
		t.Error("AddTag accepted an escape character")
	}
}

// Setting the same relative due date twice writes one event. The typed word
// resolves to local midnight, which never equalled the stored UTC date.
func TestSetDueTwiceWithARelativeWordIsANoOp(t *testing.T) {
	s := newSession(t)
	s.SetClock(func() time.Time { return time.Date(2026, 8, 17, 9, 0, 0, 0, time.FixedZone("PDT", -7*3600)) })
	s.NewNotebook("work")
	task, err := s.NewTask("work", "t", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetDue(task, "friday"); err != nil {
		t.Fatal(err)
	}
	before := len(s.Log())
	if err := s.SetDue(task, "friday"); err != nil {
		t.Fatal(err)
	}
	if len(s.Log()) != before {
		t.Fatal("setting the same due date again wrote an event")
	}
}

func TestUnlinkingAMissingLinkWritesNothing(t *testing.T) {
	s := newSession(t)
	s.NewNotebook("work")
	a, _ := s.NewNote("work", "a", "")
	b, _ := s.NewNote("work", "b", "")
	before := len(s.Log())
	if err := s.Unlink(a, b.ID); err != nil {
		t.Fatal(err)
	}
	if len(s.Log()) != before {
		t.Fatal("removing a link that does not exist wrote an event")
	}
}

// One machine with a clock years ahead must not date everyone's later events in
// its future, which would hide them from time travel for good.
func TestAFutureClockDoesNotDragLaterEventsWithIt(t *testing.T) {
	s := newSession(t)
	s.NewNotebook("work")
	if err := s.Commit(); err != nil {
		t.Fatal(err)
	}

	ahead := secondAuthor(t, s, "bob")
	ahead.SetClock(func() time.Time { return testClock().AddDate(5, 0, 0) })
	if _, err := ahead.NewNote("work", "from the future", ""); err != nil {
		t.Fatal(err)
	}
	if err := ahead.Commit(); err != nil {
		t.Fatal(err)
	}

	if err := s.Reload(); err != nil {
		t.Fatal(err)
	}
	n, err := s.NewNote("work", "written now", "")
	if err != nil {
		t.Fatal(err)
	}
	if limit := testClock().Add(ClockTolerance); n.Created.After(limit) {
		t.Fatalf("created %v, want no later than %v", n.Created, limit)
	}
	if err := s.Commit(); err != nil {
		t.Fatal(err)
	}

	past, _ := s.At(testClock().Add(time.Hour).UnixMilli())
	if past.Get(n.ID) == nil {
		t.Fatal("the note written now is hidden from a cutoff an hour from now")
	}
}
