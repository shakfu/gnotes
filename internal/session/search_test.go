package session

import (
	"strings"
	"testing"
	"time"

	"github.com/shakfu/gnotes/internal/state"
)

// corpus commits four notes to search.
func corpus(t *testing.T) *Session {
	t.Helper()
	s := newSession(t)
	s.NewNotebook("work")
	for _, n := range []struct{ title, body, tag string }{
		{"parser design", "The lexer tokenizes input before the parser runs.", "design"},
		{"shopping list", "milk, bread, coffee beans", "personal"},
		{"lexer bugs", "An ambiguity in the grammar breaks the lexer on nested quotes.", "bug"},
		{"meeting notes", "Discussed the parser rewrite and the timeline.", "work"},
	} {
		node, err := s.NewNote("work", n.title, n.body)
		if err != nil {
			t.Fatal(err)
		}
		s.AddTag(node, n.tag)
	}
	if err := s.Commit(); err != nil {
		t.Fatal(err)
	}
	return s
}

func find(t *testing.T, s *Session, query string) []string {
	t.Helper()
	nodes, err := s.Search(query, 0, false)
	if err != nil {
		t.Fatalf("Search(%q): %v", query, err)
	}
	out := make([]string, len(nodes))
	for i, n := range nodes {
		out[i] = n.Title
	}
	return out
}

func TestSearchFindsTitleBodyAndTags(t *testing.T) {
	s := corpus(t)
	for query, want := range map[string]string{
		"shopping": "shopping list",
		"coffee":   "shopping list",
		"personal": "shopping list",
	} {
		if got := find(t, s, query); len(got) != 1 || got[0] != want {
			t.Errorf("%q = %v, want [%s]", query, got, want)
		}
	}
}

func TestTitleHitsOutrankBodyHits(t *testing.T) {
	// "lexer" is in the title of one note and the body of another.
	if got := find(t, corpus(t), "lexer"); len(got) != 2 || got[0] != "lexer bugs" {
		t.Fatalf("got %v, want the title hit first", got)
	}
}

func TestTitlePhraseWins(t *testing.T) {
	if got := find(t, corpus(t), "parser design"); len(got) == 0 || got[0] != "parser design" {
		t.Fatalf("got %v, want the exact title first", got)
	}
}

func TestMultipleTermsAreAnded(t *testing.T) {
	if got := find(t, corpus(t), "parser timeline"); len(got) != 1 || got[0] != "meeting notes" {
		t.Fatalf("got %v, want only the note with both words", got)
	}
}

// Only the last word matches by prefix: the user typed a space after the
// others.
func TestOnlyTheLastTermIsPrefixMatched(t *testing.T) {
	s := corpus(t)
	for _, partial := range []string{"lex", "lexe", "grammar amb"} {
		if got := find(t, s, partial); len(got) == 0 {
			t.Errorf("%q found nothing", partial)
		}
	}
	if got := find(t, s, "lex bugs"); len(got) != 0 {
		t.Fatalf("got %v, want no prefix match on a finished word", got)
	}
}

func TestExactMatchOutranksPrefixMatch(t *testing.T) {
	s := newSession(t)
	s.NewNotebook("work")
	s.NewNote("work", "note one", "parse")
	s.NewNote("work", "note two", "parsing parser parsed")
	if err := s.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := find(t, s, "parse"); len(got) != 2 || got[0] != "note one" {
		t.Fatalf("got %v, want the exact match first", got)
	}
}

// Case, punctuation and query syntax in typed text are not operators.
func TestSearchTreatsTypedTextAsWords(t *testing.T) {
	s := newSession(t)
	s.NewNotebook("work")
	s.NewNote("work", "The Parser", "handles a.b.c and foo-bar! NEAR c++")
	if err := s.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"PARSER", "a.b.c", "foo-bar", `"foo`, "near", "c++", "AND OR"} {
		got := find(t, s, query)
		if query == "AND OR" {
			if len(got) != 0 {
				t.Errorf("%q = %v, want nothing", query, got)
			}
			continue
		}
		if len(got) != 1 {
			t.Errorf("%q = %v, want the note", query, got)
		}
	}
}

func TestSearchExcludesDeletedUnlessAsked(t *testing.T) {
	s := corpus(t)
	n, _ := s.State.Resolve("shopping list")
	s.Delete(n)
	if err := s.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := find(t, s, "coffee"); len(got) != 0 {
		t.Fatalf("got %v, want the deleted note left out", got)
	}
	if got, _ := s.Search("coffee", 0, true); len(got) != 1 {
		t.Fatalf("got %d results, want the deleted note when asked", len(got))
	}
}

func TestSearchLimitAndEmptyQuery(t *testing.T) {
	s := corpus(t)
	if got, _ := s.Search("the", 1, false); len(got) != 1 {
		t.Fatalf("got %d results, want 1", len(got))
	}
	if got := find(t, s, "  !!! "); len(got) != 0 {
		t.Fatalf("got %v for a query with no words", got)
	}
}

// The index follows edits, tags included.
func TestSearchFollowsEdits(t *testing.T) {
	s := corpus(t)
	n, _ := s.State.Resolve("shopping list")
	s.SetBody(n, "tea")
	s.RemoveTag(n, "personal")
	s.AddTag(n, "errands")
	if err := s.Commit(); err != nil {
		t.Fatal(err)
	}
	for query, want := range map[string]int{"coffee": 0, "tea": 1, "personal": 0, "errands": 1} {
		if got := find(t, s, query); len(got) != want {
			t.Errorf("%q = %v, want %d results", query, got, want)
		}
	}
}

func TestHistoryDescribesEachWrite(t *testing.T) {
	s := newSession(t)
	s.NewNotebook("work")
	task, _ := s.NewTask("work", "ship it", "")
	if err := s.Commit(); err != nil {
		t.Fatal(err)
	}
	s.SetClock(func() time.Time { return testClock().Add(time.Minute) })
	s.SetStatus(task, state.StatusDone)
	s.AddTag(task, "release")
	s.SetBody(task, "notes")
	if err := s.Commit(); err != nil {
		t.Fatal(err)
	}

	entries, err := s.History(task.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, e := range entries {
		lines = append(lines, e.Action+" "+e.Detail+" by "+e.Author)
	}
	// One commit stores final values, so its changes come in column order,
	// then set members.
	want := []string{"add.task ship it by sa", "edit.body 5 bytes by sa", "set.status done by sa", "add.tag release by sa"}
	if strings.Join(lines, "|") != strings.Join(want, "|") {
		t.Fatalf("history =\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
}

// A row edited with another SQLite client is a real edit: gnotes loads it,
// records it in history with no author, and indexes it.
func TestAnEditMadeWithSQLIsLoadedRecordedAndIndexed(t *testing.T) {
	s := corpus(t)
	n, _ := s.State.Resolve("shopping list")
	execSQL(t, s.Project, `UPDATE nodes SET title = 'groceries' WHERE id = ?`, n.ID)
	execSQL(t, s.Project, `INSERT INTO tags (node, tag) VALUES (?, 'Weekly Shop')`, n.ID)

	if changed, err := s.Refresh(); err != nil || !changed {
		t.Fatalf("Refresh = %v, %v; want the outside edit loaded", changed, err)
	}
	got := s.State.Get(n.ID)
	if got.Title != "groceries" || !got.HasTag("weekly-shop") {
		t.Fatalf("node = %q %v, want the SQL edit with its tag normalised", got.Title, got.Tags)
	}
	if !got.Updated.After(testClock()) {
		t.Fatalf("Updated = %v, want the edit to date the node", got.Updated)
	}

	entries, err := s.History(n.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	last := entries[len(entries)-2]
	if last.Action != "edit.title" || last.Detail != "groceries" || last.Author != "" {
		t.Fatalf("history entry = %+v, want an unattributed title edit", last)
	}
	if found := find(t, s, "groceries"); len(found) != 1 {
		t.Fatalf("search for the new title = %v", found)
	}
}

// A row the tree cannot hold is reported, not fatal.
func TestAHandWrittenRowThatBreaksTheTreeIsAProblem(t *testing.T) {
	s := corpus(t)
	n, _ := s.State.Resolve("shopping list")
	execSQL(t, s.Project, `INSERT INTO nodes (id, kind, parent, title) VALUES ('stray', 'note', ?, 'inside a note')`, n.ID)
	if err := s.Reload(); err != nil {
		t.Fatal(err)
	}
	if len(s.Problems) != 1 || s.Problems[0].ID != "stray" {
		t.Fatalf("Problems = %v, want the stray row", s.Problems)
	}
	if s.State.Get("stray") != nil {
		t.Fatal("the stray row was placed in the tree")
	}
}
