package state

import (
	"strings"
	"testing"
	"time"

	"github.com/shakfu/gnotes/internal/event"
	"github.com/shakfu/gnotes/internal/rank"
	"github.com/shakfu/gnotes/internal/ulid"
)

// builder assembles a log the way a session would, chaining refs and handing
// out ranks, so tests can describe intent rather than bookkeeping.
type builder struct {
	t      *testing.T
	g      *ulid.Generator
	events []event.Event
	ref    string
	user   string
	ranks  map[string]string
}

func newBuilder(t *testing.T) *builder {
	t.Helper()
	g := ulid.NewGenerator()
	return &builder{t: t, g: g, user: g.New()}
}

func (b *builder) emit(a event.Action, p event.Payload) string {
	b.t.Helper()
	e := event.Event{ID: b.g.New(), Ref: b.ref, Action: a, Payload: p, UserID: b.user}
	b.ref = e.ID
	b.events = append(b.events, e)
	return p.ID
}

// node emits a creation event, assigning the next rank under parent.
func (b *builder) node(a event.Action, parent, title string) string {
	b.t.Helper()
	id := b.g.New()

	// Ranks only have to ascend within a parent for these tests; the rank
	// package has its own coverage for the placement arithmetic.
	r, err := rank.Between(b.lastRank(parent), "")
	if err != nil {
		b.t.Fatalf("rank: %v", err)
	}
	b.ranks[parent] = r

	p := event.Payload{ID: id, Parent: parent, Rank: r}
	if a == event.AddNotebook {
		p.Name = title
	} else {
		p.Title = title
	}
	b.emit(a, p)
	return id
}

func (b *builder) lastRank(parent string) string {
	if b.ranks == nil {
		b.ranks = map[string]string{}
	}
	return b.ranks[parent]
}

func (b *builder) workspace(name string) string {
	b.t.Helper()
	id := b.g.New()
	b.emit(event.InitWorkspace, event.Payload{ID: id, Name: name, Rank: rank.Mid()})
	return id
}

func (b *builder) build() (*State, []Problem) {
	b.t.Helper()
	return Materialize(event.Sorted(b.events))
}

// mustBuild materializes and fails on any rejected event.
func (b *builder) mustBuild() *State {
	b.t.Helper()
	s, problems := b.build()
	if len(problems) > 0 {
		b.t.Fatalf("unexpected problems: %v", problems)
	}
	return s
}

// project is a small fixture: one workspace, one notebook, one note, one task.
type project struct {
	*builder
	ws, nb, note, task string
}

func newProject(t *testing.T) *project {
	t.Helper()
	b := newBuilder(t)
	p := &project{builder: b}
	p.ws = b.workspace("demo")
	p.nb = b.node(event.AddNotebook, p.ws, "parser rewrite")
	p.note = b.node(event.AddNote, p.nb, "design sketch")
	p.task = b.node(event.AddTask, p.nb, "fix the lexer")
	return p
}

func TestMaterializeBuildsTheTree(t *testing.T) {
	p := newProject(t)
	s := p.mustBuild()

	if s.Workspace != p.ws {
		t.Fatalf("Workspace = %q, want %q", s.Workspace, p.ws)
	}
	if got := s.Get(p.ws); got == nil || got.Kind != KindWorkspace || got.Title != "demo" {
		t.Fatalf("workspace node = %+v", got)
	}

	notebooks := s.Notebooks()
	if len(notebooks) != 1 || notebooks[0].ID != p.nb {
		t.Fatalf("Notebooks = %v", notebooks)
	}

	kids := s.Children(p.nb)
	if len(kids) != 2 {
		t.Fatalf("notebook has %d children, want 2", len(kids))
	}
	// Notes and tasks are peers, interleaved by rank rather than grouped.
	if kids[0].Kind != KindNote || kids[1].Kind != KindTask {
		t.Fatalf("children = %v, %v; want a note then a task", kids[0].Kind, kids[1].Kind)
	}
}

func TestChildrenAreOrderedByRank(t *testing.T) {
	b := newBuilder(t)
	ws := b.workspace("demo")
	nb := b.node(event.AddNotebook, ws, "nb")

	var want []string
	for _, title := range []string{"first", "second", "third", "fourth"} {
		want = append(want, b.node(event.AddNote, nb, title))
	}
	s := b.mustBuild()

	got := s.Children(nb)
	if len(got) != len(want) {
		t.Fatalf("got %d children, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].ID != want[i] {
			t.Fatalf("child %d = %q, want %q", i, got[i].Title, s.Get(want[i]).Title)
		}
	}
}

// A rank change must reorder siblings without touching anything else.
func TestMoveReordersWithinANotebook(t *testing.T) {
	b := newBuilder(t)
	ws := b.workspace("demo")
	nb := b.node(event.AddNotebook, ws, "nb")
	a := b.node(event.AddNote, nb, "a")
	c := b.node(event.AddNote, nb, "c")

	// Put a after c.
	s := b.mustBuild()
	newRank, err := rank.Between(s.Get(c).Rank, "")
	if err != nil {
		t.Fatal(err)
	}
	b.emit(event.MoveNode, event.Payload{ID: a, Rank: newRank})

	s = b.mustBuild()
	got := s.Children(nb)
	if got[0].ID != c || got[1].ID != a {
		t.Fatalf("order = %q, %q; want c then a", got[0].Title, got[1].Title)
	}
}

func TestMoveBetweenNotebooks(t *testing.T) {
	b := newBuilder(t)
	ws := b.workspace("demo")
	from := b.node(event.AddNotebook, ws, "from")
	to := b.node(event.AddNotebook, ws, "to")
	note := b.node(event.AddNote, from, "wanderer")

	b.emit(event.MoveNode, event.Payload{ID: note, Parent: to, Rank: rank.Mid()})
	s := b.mustBuild()

	if len(s.Children(from)) != 0 {
		t.Fatal("the note is still listed under its old notebook")
	}
	kids := s.Children(to)
	if len(kids) != 1 || kids[0].ID != note {
		t.Fatalf("new notebook children = %v", kids)
	}
	if s.Get(note).Parent != to {
		t.Fatalf("Parent = %q, want %q", s.Get(note).Parent, to)
	}
}

// The tree's shape is a rule, not a convention: violating events are rejected
// rather than producing a tree the interface cannot render.
func TestContainmentRulesAreEnforced(t *testing.T) {
	p := newProject(t)

	cases := []struct {
		name   string
		action event.Action
		parent string
		want   string
	}{
		{"note directly in the workspace", event.AddNote, p.ws, "belongs in a notebook"},
		{"task directly in the workspace", event.AddTask, p.ws, "belongs in a notebook"},
		{"notebook inside a notebook", event.AddNotebook, p.nb, "belongs in the workspace"},
		{"note inside a note", event.AddNote, p.note, "belongs in a notebook"},
		{"task inside a task", event.AddTask, p.task, "belongs in a notebook"},
		{"note in a nonexistent notebook", event.AddNote, ulid.NewGenerator().New(), "no node"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := *p.builder
			b.events = append([]event.Event(nil), p.events...)
			b.emit(c.action, event.Payload{ID: b.g.New(), Parent: c.parent, Title: "x", Name: "x", Rank: rank.Mid()})

			_, problems := b.build()
			if len(problems) != 1 {
				t.Fatalf("problems = %v, want exactly one rejection", problems)
			}
			if !strings.Contains(problems[0].Reason, c.want) {
				t.Fatalf("reason = %q, want it to mention %q", problems[0].Reason, c.want)
			}
		})
	}
}

// This is what makes notes and tasks distinct kinds rather than one kind with
// optional fields.
func TestTaskOnlyActionsAreRejectedOnNotes(t *testing.T) {
	p := newProject(t)

	cases := map[event.Action]event.Payload{
		event.SetStatus:      {ID: p.note, Status: "done"},
		event.SetPriority:    {ID: p.note, Priority: "high"},
		event.SetDue:         {ID: p.note, Due: "2026-01-01"},
		event.AddAssignee:    {ID: p.note, Assignee: "someone"},
		event.RemoveAssignee: {ID: p.note, Assignee: "someone"},
	}

	for action, payload := range cases {
		t.Run(string(action), func(t *testing.T) {
			b := *p.builder
			b.events = append([]event.Event(nil), p.events...)
			b.emit(action, payload)

			_, problems := b.build()
			if len(problems) != 1 {
				t.Fatalf("problems = %v, want one rejection", problems)
			}
			if !strings.Contains(problems[0].Reason, "not a task") {
				t.Fatalf("reason = %q", problems[0].Reason)
			}
		})
	}
}

func TestTaskFieldsApply(t *testing.T) {
	p := newProject(t)
	assignee := ulid.NewGenerator().New()

	p.emit(event.CreateContributor, event.Payload{ID: assignee, Name: "Ada"})
	p.emit(event.SetStatus, event.Payload{ID: p.task, Status: "doing"})
	p.emit(event.SetPriority, event.Payload{ID: p.task, Priority: "high"})
	p.emit(event.SetDue, event.Payload{ID: p.task, Due: "2026-09-01"})
	p.emit(event.AddAssignee, event.Payload{ID: p.task, Assignee: assignee})

	s := p.mustBuild()
	task := s.Get(p.task)

	if task.Status != StatusDoing {
		t.Errorf("Status = %v", task.Status)
	}
	if task.Priority != PriorityHigh {
		t.Errorf("Priority = %v", task.Priority)
	}
	if got := FormatDue(task.Due); got != "2026-09-01" {
		t.Errorf("Due = %q", got)
	}
	if len(task.Assignees) != 1 || task.Assignees[0] != assignee {
		t.Errorf("Assignees = %v", task.Assignees)
	}
	if got := s.Contributor(assignee); got != "Ada" {
		t.Errorf("Contributor = %q, want Ada", got)
	}
}

// A due date must mean the same thing whenever replay happens, so relative
// words are resolved at write time and rejected here.
func TestSetDueRejectsRelativeWords(t *testing.T) {
	p := newProject(t)
	p.emit(event.SetDue, event.Payload{ID: p.task, Due: "tomorrow"})

	_, problems := p.build()
	if len(problems) != 1 || !strings.Contains(problems[0].Reason, "unparseable") {
		t.Fatalf("problems = %v, want a rejection of the relative date", problems)
	}
}

func TestSetDueEmptyClearsTheDate(t *testing.T) {
	p := newProject(t)
	p.emit(event.SetDue, event.Payload{ID: p.task, Due: "2026-09-01"})
	p.emit(event.SetDue, event.Payload{ID: p.task, Due: ""})

	s := p.mustBuild()
	if !s.Get(p.task).Due.IsZero() {
		t.Fatalf("Due = %v, want cleared", s.Get(p.task).Due)
	}
}

func TestTagsAreNormalizedAndDeduplicated(t *testing.T) {
	p := newProject(t)
	for _, tag := range []string{"Bug", "#bug", "bug", "needs review", "needs_review"} {
		p.emit(event.AddTag, event.Payload{ID: p.task, Tag: tag})
	}

	s := p.mustBuild()
	got := s.Get(p.task).Tags

	if len(got) != 2 {
		t.Fatalf("Tags = %v, want two distinct tags", got)
	}
	if got[0] != "bug" || got[1] != "needs-review" {
		t.Fatalf("Tags = %v, want [bug needs-review]", got)
	}
}

func TestRemoveTag(t *testing.T) {
	p := newProject(t)
	p.emit(event.AddTag, event.Payload{ID: p.task, Tag: "bug"})
	p.emit(event.AddTag, event.Payload{ID: p.task, Tag: "perf"})
	p.emit(event.RemoveTag, event.Payload{ID: p.task, Tag: "#Bug"})

	s := p.mustBuild()
	if got := s.Get(p.task).Tags; len(got) != 1 || got[0] != "perf" {
		t.Fatalf("Tags = %v, want [perf]", got)
	}
}

// The tag list must reflect what is actually in use, so deleting the last node
// carrying a tag retires it.
func TestTagCountsTrackLiveNodesOnly(t *testing.T) {
	p := newProject(t)
	p.emit(event.AddTag, event.Payload{ID: p.task, Tag: "bug"})
	p.emit(event.AddTag, event.Payload{ID: p.note, Tag: "bug"})
	p.emit(event.AddTag, event.Payload{ID: p.note, Tag: "design"})

	s := p.mustBuild()
	tags := s.Tags()
	if len(tags) != 2 || tags[0].Tag != "bug" || tags[0].Count != 2 {
		t.Fatalf("Tags = %v, want bug first with count 2", tags)
	}

	p.emit(event.DeleteNode, event.Payload{ID: p.note})
	s = p.mustBuild()

	tags = s.Tags()
	if len(tags) != 1 || tags[0].Tag != "bug" || tags[0].Count != 1 {
		t.Fatalf("after deletion Tags = %v, want only bug with count 1", tags)
	}
}

// Deleting a notebook must take its contents with it, or they survive as live
// nodes with no reachable parent.
func TestDeletingANotebookCascades(t *testing.T) {
	p := newProject(t)
	p.emit(event.DeleteNode, event.Payload{ID: p.nb})

	s := p.mustBuild()
	for _, id := range []string{p.nb, p.note, p.task} {
		if !s.Get(id).Deleted {
			t.Errorf("node %s survived the notebook deletion", s.Get(id).Title)
		}
	}
	if len(s.Notebooks()) != 0 {
		t.Error("the deleted notebook is still listed")
	}
	if len(s.List(Filter{}, OrderRank)) != 0 {
		t.Error("deleted contents are still listed")
	}
}

func TestRestoreUndoesADeleteIncludingChildren(t *testing.T) {
	p := newProject(t)
	p.emit(event.DeleteNode, event.Payload{ID: p.nb})
	p.emit(event.RestoreNode, event.Payload{ID: p.nb})

	s := p.mustBuild()
	for _, id := range []string{p.nb, p.note, p.task} {
		if s.Get(id).Deleted {
			t.Errorf("node %s was not restored", s.Get(id).Title)
		}
	}
}

// Restoring a note into a deleted notebook would leave it live but
// unreachable.
func TestRestoreIntoADeletedNotebookIsRejected(t *testing.T) {
	p := newProject(t)
	p.emit(event.DeleteNode, event.Payload{ID: p.note})
	p.emit(event.DeleteNode, event.Payload{ID: p.nb})
	p.emit(event.RestoreNode, event.Payload{ID: p.note})

	_, problems := p.build()
	if len(problems) != 1 || !strings.Contains(problems[0].Reason, "notebook was deleted") {
		t.Fatalf("problems = %v, want the restore rejected", problems)
	}
}

func TestWorkspaceCannotBeDeletedOrMoved(t *testing.T) {
	p := newProject(t)
	p.emit(event.DeleteNode, event.Payload{ID: p.ws})
	p.emit(event.MoveNode, event.Payload{ID: p.ws, Rank: rank.Mid()})

	s, problems := p.build()
	if len(problems) != 2 {
		t.Fatalf("problems = %v, want both rejected", problems)
	}
	if s.Get(p.ws).Deleted {
		t.Fatal("the workspace was deleted")
	}
}

// Deleting twice is two people agreeing, not a conflict to report.
func TestDuplicateDeleteIsNotAProblem(t *testing.T) {
	p := newProject(t)
	p.emit(event.DeleteNode, event.Payload{ID: p.note})
	p.emit(event.DeleteNode, event.Payload{ID: p.note})

	s, problems := p.build()
	if len(problems) != 0 {
		t.Fatalf("problems = %v, want none", problems)
	}
	if !s.Get(p.note).Deleted {
		t.Fatal("the note is not deleted")
	}
}

// Two machines can both initialise before ever syncing. Neither person's work
// may disappear when the logs finally meet.
func TestTwoWorkspacesMergeWithoutLosingWork(t *testing.T) {
	g := ulid.NewGenerator()
	alice, bob := g.New(), g.New()

	wsA, wsB := g.New(), g.New()
	nbA, nbB := g.New(), g.New()

	// Two independent logs, neither referring to the other.
	logA := []event.Event{
		{ID: g.New(), Action: event.InitWorkspace, Payload: event.Payload{ID: wsA, Name: "alice's", Rank: rank.Mid()}, UserID: alice},
	}
	logA = append(logA, event.Event{ID: g.New(), Ref: logA[0].ID, Action: event.AddNotebook, Payload: event.Payload{ID: nbA, Parent: wsA, Name: "from alice", Rank: rank.Mid()}, UserID: alice})

	logB := []event.Event{
		{ID: g.New(), Action: event.InitWorkspace, Payload: event.Payload{ID: wsB, Name: "bob's", Rank: rank.Mid()}, UserID: bob},
	}
	logB = append(logB, event.Event{ID: g.New(), Ref: logB[0].ID, Action: event.AddNotebook, Payload: event.Payload{ID: nbB, Parent: wsB, Name: "from bob", Rank: rank.Mid()}, UserID: bob})

	s, problems := Materialize(event.Sorted(append(append([]event.Event{}, logA...), logB...)))
	if len(problems) != 0 {
		t.Fatalf("problems = %v", problems)
	}

	// Exactly one root survives, and both people's notebooks hang off it.
	if s.Workspace != wsA && s.Workspace != wsB {
		t.Fatalf("Workspace = %q, want one of the two initialised roots", s.Workspace)
	}
	names := map[string]bool{}
	for _, nb := range s.Notebooks() {
		names[nb.Title] = true
	}
	if !names["from alice"] || !names["from bob"] {
		t.Fatalf("notebooks = %v, want both people's work reachable", names)
	}
	if len(names) != 2 {
		t.Fatalf("notebooks = %v, want exactly the two real notebooks", names)
	}

	// The superseded root is an alias, so addressing it still works.
	superseded := wsA
	if s.Workspace == wsA {
		superseded = wsB
	}
	if s.Get(superseded) != s.Get(s.Workspace) {
		t.Fatal("the superseded workspace id does not resolve to the surviving root")
	}
}

func TestDuplicateNodeIDIsRejected(t *testing.T) {
	p := newProject(t)
	p.emit(event.AddNote, event.Payload{ID: p.note, Parent: p.nb, Title: "impostor", Rank: rank.Mid()})

	s, problems := p.build()
	if len(problems) != 1 || !strings.Contains(problems[0].Reason, "already exists") {
		t.Fatalf("problems = %v", problems)
	}
	if s.Get(p.note).Title != "design sketch" {
		t.Fatal("the original node was overwritten")
	}
}

func TestEditTitleAndBody(t *testing.T) {
	p := newProject(t)
	p.emit(event.EditTitle, event.Payload{ID: p.note, Title: "revised sketch"})
	p.emit(event.EditBody, event.Payload{ID: p.note, Body: "# Heading\n\nsome prose"})

	s := p.mustBuild()
	n := s.Get(p.note)
	if n.Title != "revised sketch" {
		t.Errorf("Title = %q", n.Title)
	}
	if !strings.Contains(n.Body, "some prose") {
		t.Errorf("Body = %q", n.Body)
	}
}

func TestNotebooksHaveNoBody(t *testing.T) {
	p := newProject(t)
	p.emit(event.EditBody, event.Payload{ID: p.nb, Body: "nope"})

	_, problems := p.build()
	if len(problems) != 1 || !strings.Contains(problems[0].Reason, "no body") {
		t.Fatalf("problems = %v", problems)
	}
}

func TestEmptyTitleIsRejected(t *testing.T) {
	p := newProject(t)
	p.emit(event.EditTitle, event.Payload{ID: p.note, Title: "   "})

	_, problems := p.build()
	if len(problems) != 1 || !strings.Contains(problems[0].Reason, "cannot be empty") {
		t.Fatalf("problems = %v", problems)
	}
}

func TestLinksAndBacklinks(t *testing.T) {
	p := newProject(t)
	p.emit(event.LinkNode, event.Payload{ID: p.task, Target: p.note})

	s := p.mustBuild()
	if got := s.Get(p.task).Links; len(got) != 1 || got[0] != p.note {
		t.Fatalf("Links = %v", got)
	}
	back := s.Backlinks(p.note)
	if len(back) != 1 || back[0].ID != p.task {
		t.Fatalf("Backlinks = %v", back)
	}

	p.emit(event.UnlinkNode, event.Payload{ID: p.task, Target: p.note})
	s = p.mustBuild()
	if len(s.Get(p.task).Links) != 0 {
		t.Fatal("the link was not removed")
	}
}

// A link written before its target has synced is pending, not wrong.
func TestLinkToAnUnsyncedTargetIsAllowed(t *testing.T) {
	p := newProject(t)
	future := ulid.NewGenerator().New()
	p.emit(event.LinkNode, event.Payload{ID: p.task, Target: future})

	s, problems := p.build()
	if len(problems) != 0 {
		t.Fatalf("problems = %v, want the pending link accepted", problems)
	}
	if got := s.Get(p.task).Links; len(got) != 1 || got[0] != future {
		t.Fatalf("Links = %v", got)
	}
}

func TestSelfLinkIsRejected(t *testing.T) {
	p := newProject(t)
	p.emit(event.LinkNode, event.Payload{ID: p.task, Target: p.task})

	_, problems := p.build()
	if len(problems) != 1 || !strings.Contains(problems[0].Reason, "link to itself") {
		t.Fatalf("problems = %v", problems)
	}
}

func TestRebalanceRespacesChildren(t *testing.T) {
	b := newBuilder(t)
	ws := b.workspace("demo")
	nb := b.node(event.AddNotebook, ws, "nb")

	var ids []string
	for _, title := range []string{"a", "b", "c"} {
		ids = append(ids, b.node(event.AddNote, nb, title))
	}

	spaced, err := rank.Spaced(len(ids))
	if err != nil {
		t.Fatal(err)
	}
	ranks := map[string]string{}
	for i, id := range ids {
		ranks[id] = spaced[i]
	}
	b.emit(event.Rebalance, event.Payload{Parent: nb, Ranks: ranks})

	s := b.mustBuild()
	got := s.Children(nb)
	for i := range ids {
		if got[i].ID != ids[i] {
			t.Fatalf("order changed at %d", i)
		}
		if got[i].Rank != spaced[i] {
			t.Fatalf("child %d rank = %q, want %q", i, got[i].Rank, spaced[i])
		}
	}
	// The point of a rebalance is room to insert again.
	for i := 1; i < len(got); i++ {
		if _, err := rank.Between(got[i-1].Rank, got[i].Rank); err != nil {
			t.Fatalf("no room between children %d and %d after rebalance", i-1, i)
		}
	}
}

// A respacing computed against a stale view must apply what still makes sense
// rather than being thrown away.
func TestRebalanceIgnoresEntriesForDepartedChildren(t *testing.T) {
	b := newBuilder(t)
	ws := b.workspace("demo")
	nb := b.node(event.AddNotebook, ws, "nb")
	stays := b.node(event.AddNote, nb, "stays")

	spaced, _ := rank.Spaced(2)
	b.emit(event.Rebalance, event.Payload{
		Parent: nb,
		Ranks:  map[string]string{stays: spaced[0], ulid.NewGenerator().New(): spaced[1]},
	})

	s, problems := b.build()
	if len(problems) != 0 {
		t.Fatalf("problems = %v", problems)
	}
	if s.Get(stays).Rank != spaced[0] {
		t.Fatal("the surviving child was not respaced")
	}
}

func TestProvenanceIsRecorded(t *testing.T) {
	p := newProject(t)
	p.emit(event.EditTitle, event.Payload{ID: p.note, Title: "later"})

	s := p.mustBuild()
	n := s.Get(p.note)

	if n.CreatedBy != p.user || n.UpdatedBy != p.user {
		t.Fatalf("authorship = %q/%q, want %q", n.CreatedBy, n.UpdatedBy, p.user)
	}
	if !n.Updated.After(n.Created) && !n.Updated.Equal(n.Created) {
		t.Fatalf("Updated %v is before Created %v", n.Updated, n.Created)
	}
	if n.Created.IsZero() {
		t.Fatal("Created was not stamped")
	}
}

// Replay is a pure function of the events, which is what makes time travel a
// matter of replaying a prefix.
func TestMaterializeIsDeterministic(t *testing.T) {
	p := newProject(t)
	p.emit(event.AddTag, event.Payload{ID: p.task, Tag: "bug"})
	p.emit(event.SetStatus, event.Payload{ID: p.task, Status: "doing"})

	first := p.mustBuild()
	for i := 0; i < 10; i++ {
		again := p.mustBuild()
		if len(again.Nodes) != len(first.Nodes) {
			t.Fatalf("run %d produced a different node count", i)
		}
		for id, want := range first.Nodes {
			got := again.Nodes[id]
			if got == nil || got.Title != want.Title || got.Rank != want.Rank || got.Status != want.Status {
				t.Fatalf("run %d diverged on node %s", i, id)
			}
		}
	}
}

// Replaying a prefix must give the state as it stood then, with no trace of
// what came later.
func TestReplayingAPrefixGivesAPastState(t *testing.T) {
	p := newProject(t)
	sorted := event.Sorted(p.events)

	// Everything up to and including the note, but not the task.
	var cut int
	for i, e := range sorted {
		if e.Payload.ID == p.note {
			cut = i + 1
			break
		}
	}

	past, problems := Materialize(sorted[:cut])
	if len(problems) != 0 {
		t.Fatalf("problems = %v", problems)
	}
	if past.Get(p.task) != nil {
		t.Fatal("the past state contains a node created later")
	}
	if past.Get(p.note) == nil {
		t.Fatal("the past state is missing a node created before the cutoff")
	}
}

func TestMaterializeEmptyLog(t *testing.T) {
	s, problems := Materialize(nil)
	if len(problems) != 0 {
		t.Fatalf("problems = %v", problems)
	}
	if s.Workspace != "" || len(s.Nodes) != 0 {
		t.Fatal("an empty log produced a non-empty state")
	}
	if len(s.Notebooks()) != 0 || len(s.List(Filter{}, OrderRank)) != 0 {
		t.Fatal("queries on an empty state returned results")
	}
}

// An event whose target has not synced yet must be reported, not fatal.
func TestEventsForUnknownNodesAreReportedNotFatal(t *testing.T) {
	p := newProject(t)
	missing := ulid.NewGenerator().New()
	p.emit(event.EditTitle, event.Payload{ID: missing, Title: "ghost"})
	p.emit(event.AddTag, event.Payload{ID: missing, Tag: "bug"})

	s, problems := p.build()
	if len(problems) != 2 {
		t.Fatalf("problems = %v, want two", problems)
	}
	// The rest of the project still materialized.
	if s.Get(p.note) == nil || len(s.Children(p.nb)) != 2 {
		t.Fatal("valid events were lost alongside the rejected ones")
	}
	if !strings.Contains(problems[0].String(), "no node") {
		t.Fatalf("Problem.String = %q", problems[0].String())
	}
}

func TestSummary(t *testing.T) {
	p := newProject(t)
	other := p.node(event.AddTask, p.nb, "another")
	p.emit(event.SetStatus, event.Payload{ID: other, Status: "done"})
	p.emit(event.SetDue, event.Payload{ID: p.task, Due: "2020-01-01"})

	s := p.mustBuild()
	c := s.Summary(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	if c.Notebooks != 1 || c.Notes != 1 || c.Tasks != 2 {
		t.Fatalf("counts = %+v", c)
	}
	if c.Open != 1 || c.Done != 1 {
		t.Fatalf("status counts = %+v", c)
	}
	if c.Overdue != 1 {
		t.Fatalf("Overdue = %d, want 1", c.Overdue)
	}
}
