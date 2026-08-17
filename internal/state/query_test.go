package state

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shakfu/gnotes/internal/event"
)

// filled is a richer fixture: two notebooks with a mix of notes and tasks in
// varying states, enough to exercise filtering and ordering.
type filled struct {
	*builder
	ws, work, personal         string
	sketch, spec, lexer, bench string
	shopping, ada              string
}

func newFilled(t *testing.T) *filled {
	t.Helper()
	b := newBuilder(t)
	f := &filled{builder: b}

	f.ws = b.workspace("demo")
	f.work = b.node(event.AddNotebook, f.ws, "work")
	f.personal = b.node(event.AddNotebook, f.ws, "personal")

	f.sketch = b.node(event.AddNote, f.work, "design sketch")
	f.lexer = b.node(event.AddTask, f.work, "fix the lexer")
	f.spec = b.node(event.AddNote, f.work, "spec notes")
	f.bench = b.node(event.AddTask, f.work, "benchmark the parser")
	f.shopping = b.node(event.AddNote, f.personal, "shopping list")

	f.ada = b.g.New()
	b.emit(event.CreateContributor, event.Payload{ID: f.ada, Name: "Ada"})

	b.emit(event.AddTag, event.Payload{ID: f.lexer, Tag: "bug"})
	b.emit(event.AddTag, event.Payload{ID: f.lexer, Tag: "parser"})
	b.emit(event.AddTag, event.Payload{ID: f.bench, Tag: "parser"})
	b.emit(event.AddTag, event.Payload{ID: f.sketch, Tag: "design"})

	b.emit(event.SetStatus, event.Payload{ID: f.bench, Status: "done"})
	b.emit(event.SetPriority, event.Payload{ID: f.lexer, Priority: "high"})
	b.emit(event.SetDue, event.Payload{ID: f.lexer, Due: "2026-01-15"})
	b.emit(event.AddAssignee, event.Payload{ID: f.lexer, Assignee: f.ada})

	return f
}

func titles(nodes []*Node) []string {
	out := make([]string, len(nodes))
	for i, n := range nodes {
		out[i] = n.Title
	}
	return out
}

func TestListDefaultsToNotesAndTasks(t *testing.T) {
	s := newFilled(t).mustBuild()

	got := s.List(Filter{}, OrderTitle)
	if len(got) != 5 {
		t.Fatalf("List = %v, want the 5 notes and tasks", titles(got))
	}
	for _, n := range got {
		if n.Kind == KindNotebook || n.Kind == KindWorkspace {
			t.Fatalf("List returned a %s", n.Kind)
		}
	}
}

func TestListFilters(t *testing.T) {
	f := newFilled(t)
	s := f.mustBuild()

	done := StatusDone
	high := PriorityHigh

	cases := []struct {
		name   string
		filter Filter
		want   []string
	}{
		{"by kind", Filter{Kinds: []Kind{KindTask}}, []string{"benchmark the parser", "fix the lexer"}},
		{"by notebook", Filter{Notebook: f.personal}, []string{"shopping list"}},
		{"by tag", Filter{Tags: []string{"parser"}}, []string{"benchmark the parser", "fix the lexer"}},
		{"by two tags", Filter{Tags: []string{"parser", "bug"}}, []string{"fix the lexer"}},
		{"by unnormalized tag", Filter{Tags: []string{"#Parser"}}, []string{"benchmark the parser", "fix the lexer"}},
		{"by status", Filter{Status: &done}, []string{"benchmark the parser"}},
		{"by priority", Filter{Priority: &high}, []string{"fix the lexer"}},
		{"by assignee", Filter{Assignee: f.ada}, []string{"fix the lexer"}},
		{"by title text", Filter{Text: "parser"}, []string{"benchmark the parser"}},
		{"by title text, case insensitive", Filter{Text: "SKETCH"}, []string{"design sketch"}},
		{"nothing matches", Filter{Tags: []string{"nonexistent"}}, nil},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := titles(s.List(c.filter, OrderTitle))
			if strings.Join(got, "|") != strings.Join(c.want, "|") {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}

// A note has no status, so asking for open items must not return every note.
func TestTaskFiltersExcludeNotes(t *testing.T) {
	s := newFilled(t).mustBuild()

	open := StatusOpen
	got := s.List(Filter{Status: &open}, OrderTitle)

	if len(got) != 1 || got[0].Title != "fix the lexer" {
		t.Fatalf("List = %v, want only the open task", titles(got))
	}

	none := PriorityNone
	got = s.List(Filter{Priority: &none}, OrderTitle)
	for _, n := range got {
		if n.Kind != KindTask {
			t.Fatalf("a priority filter returned a %s", n.Kind)
		}
	}
}

func TestListOverdueAndDueBefore(t *testing.T) {
	f := newFilled(t)
	s := f.mustBuild()

	after := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	got := s.List(Filter{Overdue: true, Now: after}, OrderTitle)
	if len(got) != 1 || got[0].Title != "fix the lexer" {
		t.Fatalf("overdue = %v, want the lexer task", titles(got))
	}

	before := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	if got := s.List(Filter{Overdue: true, Now: before}, OrderTitle); len(got) != 0 {
		t.Fatalf("nothing is overdue before its due date, got %v", titles(got))
	}

	got = s.List(Filter{DueBefore: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)}, OrderTitle)
	if len(got) != 1 || got[0].Title != "fix the lexer" {
		t.Fatalf("DueBefore = %v", titles(got))
	}
}

// A completed task is not overdue, however long ago it was due.
func TestDoneTasksAreNeverOverdue(t *testing.T) {
	f := newFilled(t)
	f.emit(event.SetDue, event.Payload{ID: f.bench, Due: "2020-01-01"})
	s := f.mustBuild()

	got := s.List(Filter{Overdue: true, Now: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)}, OrderTitle)
	for _, n := range got {
		if n.Title == "benchmark the parser" {
			t.Fatal("a done task was reported overdue")
		}
	}
}

func TestListOrdering(t *testing.T) {
	f := newFilled(t)
	s := f.mustBuild()

	t.Run("title", func(t *testing.T) {
		got := titles(s.List(Filter{Notebook: f.work}, OrderTitle))
		want := []string{"benchmark the parser", "design sketch", "fix the lexer", "spec notes"}
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("rank matches creation order here", func(t *testing.T) {
		got := titles(s.List(Filter{Notebook: f.work}, OrderRank))
		want := []string{"design sketch", "fix the lexer", "spec notes", "benchmark the parser"}
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("priority, highest first", func(t *testing.T) {
		got := s.List(Filter{Kinds: []Kind{KindTask}}, OrderPriority)
		if got[0].Title != "fix the lexer" {
			t.Fatalf("got %v, want the high-priority task first", titles(got))
		}
	})

	t.Run("due, undated last", func(t *testing.T) {
		got := s.List(Filter{Kinds: []Kind{KindTask}}, OrderDue)
		if got[0].Title != "fix the lexer" {
			t.Fatalf("got %v, want the dated task first", titles(got))
		}
		if !got[len(got)-1].Due.IsZero() {
			t.Fatal("an undated task should sort last")
		}
	})

	t.Run("created, oldest first", func(t *testing.T) {
		got := s.List(Filter{Notebook: f.work}, OrderCreated)
		for i := 1; i < len(got); i++ {
			if got[i].Created.Before(got[i-1].Created) {
				t.Fatalf("not ascending by creation at %d", i)
			}
		}
	})
}

// Node maps iterate randomly, so every ordering must be total or a listing
// would shuffle between runs.
func TestListOrderingIsStableAcrossRuns(t *testing.T) {
	s := newFilled(t).mustBuild()

	for _, order := range []Order{OrderRank, OrderCreated, OrderUpdated, OrderTitle, OrderDue, OrderPriority} {
		first := strings.Join(titles(s.List(Filter{}, order)), "|")
		for i := 0; i < 30; i++ {
			again := strings.Join(titles(s.List(Filter{}, order)), "|")
			if again != first {
				t.Fatalf("order %d is not stable:\n %s\n %s", order, first, again)
			}
		}
	}
}

// A cross-notebook listing must group by notebook, since a rank only orders
// siblings and means nothing between parents.
func TestRankOrderGroupsByNotebook(t *testing.T) {
	f := newFilled(t)
	s := f.mustBuild()

	got := s.List(Filter{}, OrderRank)

	var seen []string
	for _, n := range got {
		if len(seen) == 0 || seen[len(seen)-1] != n.Parent {
			seen = append(seen, n.Parent)
		}
	}
	if len(seen) != 2 {
		t.Fatalf("notebooks are interleaved: %v", titles(got))
	}
	if seen[0] != f.work {
		t.Fatalf("the first notebook by rank should come first")
	}
}

func TestListExcludesDeletedUnlessAsked(t *testing.T) {
	f := newFilled(t)
	f.emit(event.DeleteNode, event.Payload{ID: f.sketch})
	s := f.mustBuild()

	if got := s.List(Filter{}, OrderTitle); len(got) != 4 {
		t.Fatalf("List = %v, want the deleted note excluded", titles(got))
	}
	if got := s.List(Filter{IncludeDeleted: true}, OrderTitle); len(got) != 5 {
		t.Fatalf("List = %v, want the deleted note included", titles(got))
	}
}

func TestResolveByFullAndShortID(t *testing.T) {
	f := newFilled(t)
	s := f.mustBuild()

	for _, ref := range []string{f.lexer, f.lexer[20:], strings.ToLower(f.lexer[20:])} {
		got, err := s.Resolve(ref)
		if err != nil {
			t.Fatalf("Resolve(%q): %v", ref, err)
		}
		if got.ID != f.lexer {
			t.Fatalf("Resolve(%q) = %q", ref, got.Title)
		}
	}
}

func TestResolveByTitle(t *testing.T) {
	f := newFilled(t)
	s := f.mustBuild()

	got, err := s.Resolve("shopping")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.ID != f.shopping {
		t.Fatalf("Resolve = %q", got.Title)
	}
}

// A title prefix beats a title substring, so the obvious match wins.
func TestResolvePrefersTitlePrefixOverSubstring(t *testing.T) {
	b := newBuilder(t)
	ws := b.workspace("demo")
	nb := b.node(event.AddNotebook, ws, "nb")
	prefix := b.node(event.AddNote, nb, "parser design")
	b.node(event.AddNote, nb, "notes about the parser")

	s := b.mustBuild()
	got, err := s.Resolve("parser")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.ID != prefix {
		t.Fatalf("Resolve = %q, want the prefix match", got.Title)
	}
}

func TestResolveReportsAmbiguityWithCandidates(t *testing.T) {
	b := newBuilder(t)
	ws := b.workspace("demo")
	nb := b.node(event.AddNotebook, ws, "nb")
	b.node(event.AddNote, nb, "parser one")
	b.node(event.AddNote, nb, "parser two")

	s := b.mustBuild()
	_, err := s.Resolve("parser")

	var ambiguous *ErrAmbiguous
	if !errors.As(err, &ambiguous) {
		t.Fatalf("Resolve = %v, want ErrAmbiguous", err)
	}
	if len(ambiguous.Matches) != 2 {
		t.Fatalf("Matches = %d, want 2", len(ambiguous.Matches))
	}
	// The message must name the candidates so the user can pick one.
	for _, want := range []string{"parser one", "parser two"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
}

func TestResolveReportsNoMatch(t *testing.T) {
	s := newFilled(t).mustBuild()

	var noMatch *ErrNoMatch
	if _, err := s.Resolve("nothing like this exists"); !errors.As(err, &noMatch) {
		t.Fatalf("Resolve = %v, want ErrNoMatch", err)
	}
	if _, err := s.Resolve(""); !errors.As(err, &noMatch) {
		t.Fatalf("Resolve(\"\") = %v, want ErrNoMatch", err)
	}
}

func TestResolveHonoursKindConstraint(t *testing.T) {
	f := newFilled(t)
	s := f.mustBuild()

	if _, err := s.Resolve("design sketch", KindTask); err == nil {
		t.Fatal("Resolve found a note when asked for a task")
	}
	got, err := s.Resolve("work", KindNotebook)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.ID != f.work {
		t.Fatalf("Resolve = %q", got.Title)
	}
}

func TestResolveSkipsDeletedNodes(t *testing.T) {
	f := newFilled(t)
	f.emit(event.DeleteNode, event.Payload{ID: f.shopping})
	s := f.mustBuild()

	if _, err := s.Resolve("shopping"); err == nil {
		t.Fatal("Resolve returned a deleted node")
	}
}

func TestContributorLookup(t *testing.T) {
	f := newFilled(t)
	s := f.mustBuild()

	if got := s.Contributor(f.ada); got != "Ada" {
		t.Fatalf("Contributor = %q", got)
	}
	id, ok := s.FindContributor("ada")
	if !ok || id != f.ada {
		t.Fatalf("FindContributor = %q, %v", id, ok)
	}
	if _, ok := s.FindContributor("nobody"); ok {
		t.Fatal("FindContributor invented a person")
	}
}

// An assignee whose contributor record has not synced must still render as
// something stable rather than as a blank column.
func TestContributorFallsBackToAShortID(t *testing.T) {
	s := newFilled(t).mustBuild()
	unknown := "01ARZ3NDEKTSV4RRFFQ69G5FAV"

	if got := s.Contributor(unknown); got != "9G5FAV" {
		t.Fatalf("Contributor = %q, want a short id", got)
	}
	if got := s.Contributor(""); got != "unknown" {
		t.Fatalf("Contributor(\"\") = %q", got)
	}
}

func TestRenameContributorKeepsAssignments(t *testing.T) {
	f := newFilled(t)
	f.emit(event.RenameContributor, event.Payload{ID: f.ada, Name: "Ada Lovelace"})
	s := f.mustBuild()

	if got := s.Contributor(f.ada); got != "Ada Lovelace" {
		t.Fatalf("Contributor = %q", got)
	}
	// The assignment refers to the id, so renaming cannot orphan it.
	if got := s.List(Filter{Assignee: f.ada}, OrderTitle); len(got) != 1 {
		t.Fatalf("assignment lost after rename: %v", titles(got))
	}
}

func TestPath(t *testing.T) {
	f := newFilled(t)
	s := f.mustBuild()

	got := s.Path(s.Get(f.lexer))
	want := []string{"demo", "work", "fix the lexer"}

	if strings.Join(got, "/") != strings.Join(want, "/") {
		t.Fatalf("Path = %v, want %v", got, want)
	}
}

func TestSiblingsExcludesTheMovingNode(t *testing.T) {
	f := newFilled(t)
	s := f.mustBuild()

	all := s.Siblings(f.work, "")
	without := s.Siblings(f.work, f.lexer)

	if len(without) != len(all)-1 {
		t.Fatalf("Siblings excluding one returned %d, want %d", len(without), len(all)-1)
	}
	for _, sib := range without {
		if sib.ID == f.lexer {
			t.Fatal("the excluded node is still listed")
		}
	}
	// The result must be ascending, since rank.Resolve relies on it.
	for i := 1; i < len(all); i++ {
		if all[i-1].Rank >= all[i].Rank {
			t.Fatalf("Siblings not ascending at %d", i)
		}
	}
}

func TestParseHelpers(t *testing.T) {
	if k, ok := ParseKind("TASKS"); !ok || k != KindTask {
		t.Errorf("ParseKind(TASKS) = %v, %v", k, ok)
	}
	if _, ok := ParseKind("widget"); ok {
		t.Error("ParseKind accepted a nonsense kind")
	}
	if s, ok := ParseStatus("wip"); !ok || s != StatusDoing {
		t.Errorf("ParseStatus(wip) = %v, %v", s, ok)
	}
	if p, ok := ParsePriority("urgent"); !ok || p != PriorityHigh {
		t.Errorf("ParsePriority(urgent) = %v, %v", p, ok)
	}
	if o, ok := ParseOrder("recent"); !ok || o != OrderUpdated {
		t.Errorf("ParseOrder(recent) = %v, %v", o, ok)
	}
	if _, ok := ParseOrder("sideways"); ok {
		t.Error("ParseOrder accepted a nonsense order")
	}
}

func TestParseDueRelativeWords(t *testing.T) {
	now := time.Date(2026, 8, 17, 15, 30, 0, 0, time.UTC) // a Monday

	cases := map[string]string{
		"today":      "2026-08-17",
		"tomorrow":   "2026-08-18",
		"yesterday":  "2026-08-16",
		"friday":     "2026-08-21",
		"fri":        "2026-08-21",
		"monday":     "2026-08-24", // next Monday, not today
		"2026-12-25": "2026-12-25",
		"2026/12/25": "2026-12-25",
	}

	for in, want := range cases {
		got, ok := ParseDue(in, now)
		if !ok {
			t.Errorf("ParseDue(%q) failed", in)
			continue
		}
		if FormatDue(got) != want {
			t.Errorf("ParseDue(%q) = %q, want %q", in, FormatDue(got), want)
		}
	}

	if got, ok := ParseDue("", now); !ok || !got.IsZero() {
		t.Errorf("ParseDue(\"\") = %v, %v; want the zero time", got, ok)
	}
	if _, ok := ParseDue("next tuesday-ish", now); ok {
		t.Error("ParseDue accepted nonsense")
	}
}

// The strict parser is what replay uses, so it must reject anything whose
// meaning depends on when it is read.
func TestParseDueAbsoluteRejectsRelativeWords(t *testing.T) {
	for _, in := range []string{"today", "tomorrow", "friday", "yesterday"} {
		if _, ok := ParseDueAbsolute(in); ok {
			t.Errorf("ParseDueAbsolute(%q) was accepted", in)
		}
	}
	if got, ok := ParseDueAbsolute("2026-08-17"); !ok || FormatDue(got) != "2026-08-17" {
		t.Errorf("ParseDueAbsolute rejected a plain date")
	}
}

func TestNormalizeTag(t *testing.T) {
	cases := map[string]string{
		"bug":           "bug",
		"#bug":          "bug",
		"  Bug  ":       "bug",
		"needs review":  "needs-review",
		"needs_review":  "needs-review",
		"NEEDS  REVIEW": "needs-review",
		"trailing ":     "trailing",
		"":              "",
		"#":             "",
	}
	for in, want := range cases {
		if got := NormalizeTag(in); got != want {
			t.Errorf("NormalizeTag(%q) = %q, want %q", in, got, want)
		}
	}
}

func BenchmarkList(b *testing.B) {
	t := &testing.T{}
	f := newFilled(t)

	// Grow the fixture to a realistic size.
	for i := 0; i < 5000; i++ {
		f.node(event.AddTask, f.work, "task number "+string(rune('a'+i%26)))
	}
	s := f.mustBuild()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.List(Filter{Kinds: []Kind{KindTask}}, OrderRank)
	}
}
