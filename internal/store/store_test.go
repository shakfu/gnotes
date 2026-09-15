package store

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shakfu/gnotes/internal/event"
	"github.com/shakfu/gnotes/internal/rank"
	"github.com/shakfu/gnotes/internal/state"
	"github.com/shakfu/gnotes/internal/ulid"
)

var clock = time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)

func testActor(t *testing.T, name string) Actor {
	t.Helper()
	return Actor{ID: ulid.NewGenerator().New(), Name: name}
}

func initAt(t *testing.T, root, name string) *Project {
	t.Helper()
	p, err := Init(root, name, clock)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

func initProject(t *testing.T) *Project {
	t.Helper()
	return initAt(t, t.TempDir(), "demo")
}

func discover(t *testing.T, dir string) *Project {
	t.Helper()
	p, err := Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

func mkdir(t *testing.T, parts ...string) string {
	t.Helper()
	dir := filepath.Join(parts...)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	mkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func loadRows(t *testing.T, p *Project) Rows {
	t.Helper()
	rows, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return rows
}

// external runs a statement as another SQLite client would.
func external(t *testing.T, p *Project, query string, args ...any) {
	t.Helper()
	db, err := sql.Open("sqlite", p.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

// tree builds a workspace, a notebook and a task with every field set, the way
// a session would, and returns the changed nodes and contributors.
func tree(t *testing.T, a Actor) (*state.State, []*state.Node, []*state.Contributor) {
	t.Helper()
	g := ulid.NewGeneratorAt(func() time.Time { return clock })
	ws, nb, task := g.New(), g.New(), g.New()
	st, _ := state.Materialize(nil)
	for _, e := range []event.Event{
		{Action: event.InitWorkspace, Payload: event.Payload{ID: ws, Name: "demo", Rank: rank.Mid()}},
		{Action: event.CreateContributor, Payload: event.Payload{ID: a.ID, Name: a.Name}},
		{Action: event.AddNotebook, Payload: event.Payload{ID: nb, Parent: ws, Name: "work", Rank: rank.Mid()}},
		{Action: event.AddTask, Payload: event.Payload{ID: task, Parent: nb, Title: "fix the lexer", Rank: rank.Mid()}},
		{Action: event.EditBody, Payload: event.Payload{ID: task, Body: "it breaks"}},
		{Action: event.SetStatus, Payload: event.Payload{ID: task, Status: "doing"}},
		{Action: event.SetPriority, Payload: event.Payload{ID: task, Priority: "high"}},
		{Action: event.SetDue, Payload: event.Payload{ID: task, Due: "2026-08-21"}},
		{Action: event.AddTag, Payload: event.Payload{ID: task, Tag: "parser"}},
		{Action: event.AddTag, Payload: event.Payload{ID: task, Tag: "bug"}},
		{Action: event.LinkNode, Payload: event.Payload{ID: task, Target: nb}},
		{Action: event.AddAssignee, Payload: event.Payload{ID: task, Assignee: a.ID}},
	} {
		e.ID, e.UserID = g.New(), a.ID
		if err := st.Apply(&e); err != nil {
			t.Fatalf("%s: %v", e.Action, err)
		}
	}
	nodes, contributors := st.TakeChanged()
	return st, nodes, contributors
}

func write(t *testing.T, p *Project, a Actor, at time.Time, nodes []*state.Node, contributors []*state.Contributor) (Snapshot, Snapshot) {
	t.Helper()
	prev, head, err := Write(p, a, at, nodes, contributors)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	return prev, head
}

func TestInitCreatesTheDatabaseAndItsGitFiles(t *testing.T) {
	root := t.TempDir()
	p := initAt(t, root, "demo")

	if want := filepath.Join(root, DirName, DBFile); p.Path != want || p.Root != root {
		t.Fatalf("Path, Root = %q, %q; want %q, %q", p.Path, p.Root, want, root)
	}
	for name, want := range map[string]string{".gitignore": gitignore, ".gitattributes": gitattributes} {
		if raw, err := os.ReadFile(filepath.Join(root, DirName, name)); err != nil || string(raw) != want {
			t.Fatalf("%s = %q, %v; want %q", name, raw, err, want)
		}
	}
	if again := discover(t, root); again.Config.Name != "demo" || again.Config.Created == "" {
		t.Fatalf("Config = %+v", again.Config)
	}
}

// The committed file must hold every write, so there is no WAL beside it.
func TestTheDatabaseFileIsCompleteAfterAWrite(t *testing.T) {
	p := initProject(t)
	a := testActor(t, "sa")
	_, nodes, contributors := tree(t, a)
	write(t, p, a, clock, nodes, contributors)

	var mode string
	if err := p.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "delete" {
		t.Fatalf("journal_mode = %q, %v; want delete", mode, err)
	}
	for _, suffix := range []string{"-wal", "-journal"} {
		if _, err := os.Stat(p.Path + suffix); !os.IsNotExist(err) {
			t.Errorf("%s%s exists after the write", DBFile, suffix)
		}
	}
}

func TestInitDefaultsNameToDirectory(t *testing.T) {
	root := mkdir(t, t.TempDir(), "my-project")
	if p := initAt(t, root, ""); p.Config.Name != "my-project" {
		t.Fatalf("Name = %q, want the directory name", p.Config.Name)
	}
}

func TestInitRefusesToOverwrite(t *testing.T) {
	root := t.TempDir()
	initAt(t, root, "first")
	if _, err := Init(root, "second", clock); !errors.Is(err, ErrExists) {
		t.Fatalf("second Init = %v, want ErrExists", err)
	}
	if p := discover(t, root); p.Config.Name != "first" {
		t.Fatalf("Name = %q, the existing project was overwritten", p.Config.Name)
	}
}

func TestDiscoverFindsTheNearestProject(t *testing.T) {
	root := t.TempDir()
	initAt(t, root, "outer")
	if p := discover(t, mkdir(t, root, "a", "b")); p.Config.Name != "outer" {
		t.Fatalf("from below: Name = %q", p.Config.Name)
	}
	inner := mkdir(t, root, "sub")
	initAt(t, inner, "inner")
	if p := discover(t, inner); p.Config.Name != "inner" {
		t.Fatalf("Name = %q, want the nearest project", p.Config.Name)
	}
}

// An empty .gnotes left by a partial checkout must not shadow a real project
// further up.
func TestDiscoverSkipsADirectoryWithoutADatabase(t *testing.T) {
	root := t.TempDir()
	initAt(t, root, "outer")
	mkdir(t, root, "a", DirName)
	if p := discover(t, filepath.Join(root, "a")); p.Config.Name != "outer" {
		t.Fatalf("Name = %q, want the outer project", p.Config.Name)
	}
	if _, err := Discover(t.TempDir()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Discover = %v, want ErrNotFound", err)
	}
}

func TestOpenRefusesANewerSchema(t *testing.T) {
	root := t.TempDir()
	p := initAt(t, root, "demo")
	if _, err := p.db.Exec(`PRAGMA user_version = 99`); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenAt(root); err == nil || !strings.Contains(err.Error(), "newer gnotes") {
		t.Fatalf("OpenAt = %v, want a newer-schema refusal", err)
	}
}

func TestWriteAndLoadRoundTripEveryField(t *testing.T) {
	p := initProject(t)
	a := testActor(t, "sa")
	st, nodes, contributors := tree(t, a)
	write(t, p, a, clock, nodes, contributors)

	rows := loadRows(t, p)
	got, problems := state.Build(rows.Nodes, rows.Contributors)
	if len(rows.Problems)+len(problems) != 0 {
		t.Fatalf("problems: %v %v", rows.Problems, problems)
	}
	for id, want := range st.Nodes {
		n := got.Get(id)
		if n == nil {
			t.Fatalf("node %s missing", id)
		}
		if n.Kind != want.Kind || n.Parent != want.Parent || n.Rank != want.Rank || n.Title != want.Title ||
			n.Body != want.Body || n.Status != want.Status || n.Priority != want.Priority ||
			!n.Due.Equal(want.Due) || !n.Created.Equal(want.Created) || n.CreatedBy != want.CreatedBy ||
			strings.Join(n.Tags, ",") != strings.Join(want.Tags, ",") ||
			strings.Join(n.Links, ",") != strings.Join(want.Links, ",") ||
			strings.Join(n.Assignees, ",") != strings.Join(want.Assignees, ",") {
			t.Fatalf("round trip of %q:\n got %+v\nwant %+v", want.Title, *n, *want)
		}
	}
	if got.Contributor(a.ID) != "sa" {
		t.Fatalf("contributor = %q", got.Contributor(a.ID))
	}
}

// Writing a node again writes only what differs, and a removed set member
// leaves its table.
func TestWriteRecordsOnlyWhatChanged(t *testing.T) {
	p := initProject(t)
	a := testActor(t, "sa")
	st, nodes, contributors := tree(t, a)
	_, head := write(t, p, a, clock, nodes, contributors)

	task, _ := st.Resolve("fix the lexer")
	later := clock.Add(time.Hour)
	for _, e := range []event.Event{
		{Action: event.RemoveTag, Payload: event.Payload{ID: task.ID, Tag: "bug"}},
		{Action: event.EditTitle, Payload: event.Payload{ID: task.ID, Title: "fix the lexer"}},
	} {
		e.ID, e.UserID = ulid.NewGeneratorAt(func() time.Time { return later }).New(), a.ID
		if err := st.Apply(&e); err != nil {
			t.Fatal(err)
		}
	}
	nodes, contributors = st.TakeChanged()
	prev, next := write(t, p, a, later, nodes, contributors)

	if prev != head {
		t.Fatalf("prev = %d, want the head of the first write, %d", prev, head)
	}
	changes, err := History(p, task.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var recent []Change
	for _, c := range changes {
		if c.Seq > int64(head) {
			recent = append(recent, c)
		}
	}
	if len(recent) != 1 || recent[0].Field != "tag" || recent[0].Old != "bug" || recent[0].New != "" ||
		!recent[0].At.Equal(later) || recent[0].Author != a.ID || int64(next) != recent[0].Seq {
		t.Fatalf("changes after the second write = %+v, want one tag removal at %v by %s", recent, later, a.ID)
	}
	if n := loadRows(t, p).Nodes; len(n) != 3 {
		t.Fatalf("loaded %d nodes, want 3", len(n))
	}
}

func TestWriteRefusesAnUnconfiguredActor(t *testing.T) {
	p := initProject(t)
	for name, a := range map[string]Actor{
		"no id":      {Name: "sa"},
		"bad id":     {ID: "nope", Name: "sa"},
		"blank name": {ID: ulid.NewGenerator().New(), Name: "   "},
	} {
		if _, _, err := Write(p, a, clock, nil, nil); err == nil {
			t.Errorf("%s: Write accepted an invalid actor", name)
		}
	}
}

// A write is one transaction: a node the schema refuses takes the whole write
// with it, and the writer row is left clear.
func TestWriteIsAllOrNothing(t *testing.T) {
	p := initProject(t)
	a := testActor(t, "sa")
	_, nodes, contributors := tree(t, a)
	orphan := &state.Node{ID: "orphan", Kind: state.KindNote, Parent: "missing", Title: "no parent", Created: clock, Updated: clock}

	if _, _, err := Write(p, a, clock, append(nodes, orphan), contributors); err == nil {
		t.Fatal("a node with a missing parent was written")
	}
	if rows := loadRows(t, p); len(rows.Nodes) != 0 || rows.Head != 0 {
		t.Fatalf("after the failed write: %d nodes, head %d", len(rows.Nodes), rows.Head)
	}
	var at sql.NullString
	if err := p.db.QueryRow(`SELECT at FROM writer`).Scan(&at); err != nil || at.Valid {
		t.Fatalf("writer.at = %v, %v; want NULL", at, err)
	}
}

func TestTheSchemaRefusesTaskFieldsOnANote(t *testing.T) {
	p := initProject(t)
	a := testActor(t, "sa")
	_, nodes, contributors := tree(t, a)
	write(t, p, a, clock, nodes, contributors)
	if _, err := p.db.Exec(`UPDATE nodes SET status = 'done' WHERE kind = 'notebook'`); err == nil {
		t.Fatal("a notebook took a status")
	}
}

// A change made with another client is recorded at the wall clock with no
// author, and moves the Snapshot; a statement that changes nothing does not.
func TestAnExternalEditIsRecorded(t *testing.T) {
	p := initProject(t)
	a := testActor(t, "sa")
	_, nodes, contributors := tree(t, a)
	write(t, p, a, clock, nodes, contributors)

	before := Snap(p)
	external(t, p, `UPDATE nodes SET title = title`)
	if Snap(p) != before {
		t.Fatal("a statement that changed nothing moved the snapshot")
	}
	start := time.Now().Add(-time.Second)
	external(t, p, `UPDATE nodes SET title = 'renamed' WHERE kind = 'task'`)
	if Snap(p) == before {
		t.Fatal("an external edit did not move the snapshot")
	}

	changes, err := History(p, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	c := changes[0]
	if c.Field != "title" || c.Old != "fix the lexer" || c.New != "renamed" || c.Author != "" || c.At.Before(start) {
		t.Fatalf("change = %+v, want an unattributed title edit at the wall clock", c)
	}
}

func TestRowsAtReplaysTheChanges(t *testing.T) {
	p := initProject(t)
	a := testActor(t, "sa")
	_, nodes, contributors := tree(t, a)
	write(t, p, a, clock, nodes, contributors)

	external(t, p, `UPDATE nodes SET title = 'renamed', status = 'done' WHERE kind = 'task'`)
	external(t, p, `DELETE FROM tags WHERE tag = 'bug'`)
	external(t, p, `INSERT INTO tags (node, tag) SELECT id, 'later' FROM nodes WHERE kind = 'task'`)

	past, err := RowsAt(p, clock)
	if err != nil {
		t.Fatal(err)
	}
	st, problems := state.Build(past.Nodes, past.Contributors)
	if len(problems) != 0 {
		t.Fatalf("problems: %v", problems)
	}
	task, err := st.Resolve("fix the lexer")
	if err != nil {
		t.Fatalf("the task as it stood: %v", err)
	}
	if task.Status != state.StatusDoing || strings.Join(task.Tags, ",") != "bug,parser" || !task.Due.Equal(time.Date(2026, 8, 21, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("task then = %+v", *task)
	}
	if st.Contributor(a.ID) != "sa" {
		t.Fatal("the contributor is missing from the past")
	}
	if early, _ := RowsAt(p, clock.Add(-time.Hour)); len(early.Nodes) != 0 {
		t.Fatalf("%d nodes before anything was written", len(early.Nodes))
	}
}

func TestSearchIndexesNotesAndTasksWithTheirTags(t *testing.T) {
	p := initProject(t)
	a := testActor(t, "sa")
	st, nodes, contributors := tree(t, a)
	write(t, p, a, clock, nodes, contributors)
	task, _ := st.Resolve("fix the lexer")

	for match, want := range map[string]int{`"parser"`: 1, `"breaks"`: 1, `"lex"*`: 1, `"work"`: 0} {
		hits, err := Search(p, match)
		if err != nil {
			t.Fatal(err)
		}
		if len(hits) != want || (want == 1 && hits[0].ID != task.ID) {
			t.Errorf("%s = %+v, want %d hit", match, hits, want)
		}
	}
	external(t, p, `DELETE FROM nodes WHERE kind = 'task'`)
	if hits, _ := Search(p, `"parser"`); len(hits) != 0 {
		t.Fatalf("a removed row is still indexed: %+v", hits)
	}
}

// Separate processes writing at once must each land whole.
func TestConcurrentWritesAllLand(t *testing.T) {
	root := t.TempDir()
	p := initAt(t, root, "demo")
	a := testActor(t, "sa")
	st, nodes, contributors := tree(t, a)
	write(t, p, a, clock, nodes, contributors)
	nb, _ := st.Resolve("work", state.KindNotebook)

	const writers = 8
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(title string) {
			defer wg.Done()
			other, err := OpenAt(root)
			if err != nil {
				errs <- err
				return
			}
			defer other.Close()
			n := &state.Node{ID: ulid.NewGenerator().New(), Kind: state.KindNote, Parent: nb.ID, Title: title, Tags: []string{"x", "y"}, Created: clock, Updated: clock}
			_, _, err = Write(other, a, clock, []*state.Node{n}, nil)
			errs <- err
		}(string(rune('a' + w)))
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Write: %v", err)
		}
	}
	if n := len(loadRows(t, p).Nodes); n != 3+writers {
		t.Fatalf("loaded %d nodes, want %d", n, 3+writers)
	}
}

func BenchmarkLoad(b *testing.B) {
	p, err := Init(b.TempDir(), "bench", clock)
	if err != nil {
		b.Fatal(err)
	}
	defer p.Close()
	a := Actor{ID: ulid.NewGenerator().New(), Name: "sa"}
	g := ulid.NewGenerator()
	ws := &state.Node{ID: g.New(), Kind: state.KindWorkspace, Title: "bench", Created: clock, Updated: clock}
	nb := &state.Node{ID: g.New(), Kind: state.KindNotebook, Parent: ws.ID, Title: "work", Created: clock, Updated: clock}
	nodes := []*state.Node{ws, nb}
	for i := 0; i < 5000; i++ {
		nodes = append(nodes, &state.Node{ID: g.New(), Kind: state.KindTask, Parent: nb.ID, Rank: "7fffffffffffffffffffffff",
			Title: "a task about something", Body: strings.Repeat("words ", 40), Tags: []string{"one", "two"}, Created: clock, Updated: clock})
	}
	if _, _, err := Write(p, a, clock, nodes, nil); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Load(p); err != nil {
			b.Fatal(err)
		}
	}
}

// legacyProject writes a pre-database project under root and returns its
// events directory.
func legacyProject(t *testing.T, root, config string) string {
	t.Helper()
	writeFile(t, filepath.Join(root, DirName, legacyConfigFile), config)
	return mkdir(t, root, DirName, legacyEventsDir)
}

func encode(t *testing.T, events ...event.Event) string {
	t.Helper()
	var b strings.Builder
	for _, e := range events {
		line, err := event.Encode(e)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// legacyEvents is a small project written by two authors over two hours.
func legacyEvents(alice, bob Actor) (ws, task string, byAlice, byBob []event.Event) {
	g := ulid.NewGeneratorAt(func() time.Time { return clock })
	later := ulid.NewGeneratorAt(func() time.Time { return clock.Add(2 * time.Hour) })
	ws, nb, task := g.New(), g.New(), g.New()
	byAlice = []event.Event{
		{ID: g.New(), Action: event.InitWorkspace, Payload: event.Payload{ID: ws, Name: "old", Rank: rank.Mid()}},
		{Action: event.CreateContributor, Payload: event.Payload{ID: alice.ID, Name: "alice"}},
		{Action: event.AddNotebook, Payload: event.Payload{ID: nb, Parent: ws, Name: "work", Rank: rank.Mid()}},
		{Action: event.AddTask, Payload: event.Payload{ID: task, Parent: nb, Title: "fix the lexer", Rank: rank.Mid()}},
	}
	for i := 1; i < len(byAlice); i++ {
		byAlice[i].ID, byAlice[i].Ref = g.New(), byAlice[i-1].ID
	}
	byBob = []event.Event{
		{ID: later.New(), Ref: byAlice[len(byAlice)-1].ID, Action: event.CreateContributor, Payload: event.Payload{ID: bob.ID, Name: "bob"}},
	}
	byBob = append(byBob, event.Event{ID: later.New(), Ref: byBob[0].ID, Action: event.EditTitle, Payload: event.Payload{ID: task, Title: "fix the lexer properly"}})
	return ws, task, byAlice, byBob
}

func TestLegacyProjectIsImportedOnceWithItsHistory(t *testing.T) {
	root := t.TempDir()
	dir := legacyProject(t, root, `{"name":"old","created":"2026-08-01T00:00:00Z"}`)
	alice, bob := testActor(t, "alice"), testActor(t, "bob")
	_, task, byAlice, byBob := legacyEvents(alice, bob)

	future := `{"v":1,"id":"` + ulid.NewGenerator().New() + `","a":"teleport.node"}` + "\n"
	writeFile(t, filepath.Join(dir, alice.ID+".alice"+LogExt), encode(t, byAlice...)+"\n  \n"+future)
	writeFile(t, filepath.Join(dir, strings.ToLower(bob.ID)+".b.o.b"+LogExt), encode(t, byBob...)+`{"v":1,"id":"01M0TORN`)
	writeFile(t, filepath.Join(dir, "README.md"), "not a log")
	writeFile(t, filepath.Join(dir, "scratch"+LogExt), `{"v":1,"id":"x"}`)

	p := discover(t, root)
	imp := p.Imported
	if imp == nil || imp.Events != 6 || imp.Unknown != 1 || imp.Unapplied != 0 || imp.From != dir {
		t.Fatalf("Imported = %+v, want 6 events and 1 unknown from %s", imp, dir)
	}
	if len(imp.Torn) != 1 || !strings.HasPrefix(imp.Torn[0], strings.ToLower(bob.ID)) {
		t.Fatalf("Torn = %v, want bob's log", imp.Torn)
	}
	if p.Config.Name != "old" || p.Config.Created != "2026-08-01T00:00:00Z" {
		t.Fatalf("Config = %+v, want the legacy descriptor", p.Config)
	}

	st, _ := state.Build(loadRows(t, p).Nodes, loadRows(t, p).Contributors)
	if n := st.Get(task); n == nil || n.Title != "fix the lexer properly" || n.UpdatedBy != bob.ID {
		t.Fatalf("imported task = %+v", n)
	}
	changes, err := History(p, task, 0)
	if err != nil {
		t.Fatal(err)
	}
	last := changes[len(changes)-1]
	if last.Field != "title" || last.Author != bob.ID || !last.At.Equal(clock.Add(2*time.Hour)) {
		t.Fatalf("last change = %+v, want bob's rename at its own time", last)
	}

	if again := discover(t, root); again.Imported != nil {
		t.Fatal("the legacy logs were imported a second time")
	}
	external(t, p, `DELETE FROM tags; DELETE FROM links; DELETE FROM assignees; DELETE FROM nodes`)
	if again := discover(t, root); again.Imported != nil {
		t.Fatal("emptying the database brought the legacy logs back")
	}
	if _, err := os.Stat(filepath.Join(dir, alice.ID+".alice"+LogExt)); err != nil {
		t.Fatalf("the legacy log was removed: %v", err)
	}
}

func TestLegacyEventsRootIsHonoured(t *testing.T) {
	root := t.TempDir()
	legacyProject(t, root, `{"name":"old","eventsRoot":"../elsewhere"}`)
	a := testActor(t, "sa")
	_, _, byAlice, _ := legacyEvents(a, a)
	writeFile(t, filepath.Join(root, "elsewhere", legacyEventsDir, a.ID+".sa"+LogExt), encode(t, byAlice...))

	if p := discover(t, root); p.Imported == nil || p.Imported.Events != len(byAlice) {
		t.Fatalf("Imported = %+v, want the relocated logs", p.Imported)
	}
}

// A record damaged anywhere but the end refuses the import, and the failed
// import leaves nothing behind that would stop a retry.
func TestLegacyCorruptRecordRefusesTheImportUntilFixed(t *testing.T) {
	root := t.TempDir()
	dir := legacyProject(t, root, `{"name":"old"}`)
	a := testActor(t, "sa")
	_, _, byAlice, _ := legacyEvents(a, a)
	log := filepath.Join(dir, a.ID+".sa"+LogExt)
	lines := encode(t, byAlice...)
	writeFile(t, log, strings.Replace(lines, "\n", "\n{ not json\n", 1))

	if _, err := Discover(root); err == nil || !strings.Contains(err.Error(), ":2:") {
		t.Fatalf("Discover = %v, want an error naming line 2", err)
	}
	writeFile(t, log, lines)
	if p := discover(t, root); p.Imported == nil || p.Imported.Events != len(byAlice) {
		t.Fatalf("Imported = %+v after the fix", p.Imported)
	}
}

func TestParseLogName(t *testing.T) {
	a := testActor(t, "sa")
	for _, name := range []string{
		a.ID + ".sa" + LogExt,
		a.ID + ".j.-r.-hacker" + LogExt,
		strings.ToLower(a.ID) + ".sa" + LogExt,
	} {
		if got, ok := ParseLogName(name); !ok || got != a.ID {
			t.Errorf("ParseLogName(%q) = %q, %v; want %q", name, got, ok, a.ID)
		}
	}
	for _, in := range []string{"notes.md", "README", "events.jsonl", "backup.jsonl~", ".jsonl"} {
		if _, ok := ParseLogName(in); ok {
			t.Errorf("ParseLogName(%q) accepted a non-log", in)
		}
	}
}

func TestGlobalLocationRoundTrip(t *testing.T) {
	t.Setenv("GNOTES_HOME", t.TempDir())

	if dir, err := LoadGlobal(); err != nil || dir != "" {
		t.Fatalf("LoadGlobal before setup = %q, %v; want empty", dir, err)
	}
	if err := SaveGlobal("notes"); err == nil {
		t.Fatal("a relative global location was accepted")
	}
	want := filepath.Join(t.TempDir(), "notes")
	if err := SaveGlobal(want); err != nil {
		t.Fatal(err)
	}
	if dir, err := LoadGlobal(); err != nil || dir != want {
		t.Fatalf("LoadGlobal = %q, %v; want %q", dir, err, want)
	}
}
