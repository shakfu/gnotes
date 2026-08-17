package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shakfu/gnotes/internal/event"
	"github.com/shakfu/gnotes/internal/ulid"
)

func testActor(t *testing.T, name string) Actor {
	t.Helper()
	return Actor{ID: ulid.NewGenerator().New(), Name: name}
}

func initProject(t *testing.T) *Project {
	t.Helper()
	p, err := Init(t.TempDir(), "demo", time.Now())
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	return p
}

// writeEvents appends a chain of events, each referring to the one before it,
// as a real session would.
func writeEvents(t *testing.T, p *Project, a Actor, n int, root string) []event.Event {
	t.Helper()

	g := ulid.NewGenerator()
	out := make([]event.Event, 0, n)
	ref := root
	for i := 0; i < n; i++ {
		e := event.Event{ID: g.New(), Ref: ref, Action: event.AddNote, Payload: event.Payload{ID: "n", Title: "t"}}
		out = append(out, e)
		ref = e.ID
	}
	if err := Append(p, a, out); err != nil {
		t.Fatalf("Append: %v", err)
	}
	return out
}

func TestInitCreatesLayout(t *testing.T) {
	root := t.TempDir()
	p, err := Init(root, "demo", time.Now())
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	if p.Dir != filepath.Join(root, DirName) {
		t.Fatalf("Dir = %q", p.Dir)
	}
	if p.Root() != root {
		t.Fatalf("Root = %q, want %q", p.Root(), root)
	}
	for _, path := range []string{p.ConfigPath(), p.EventsDir()} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected %s to exist: %v", path, err)
		}
	}
	if p.Config.Name != "demo" {
		t.Errorf("Name = %q", p.Config.Name)
	}
	if p.Config.Created == "" {
		t.Error("Created was not stamped")
	}
}

func TestInitDefaultsNameToDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "my-project")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}

	p, err := Init(root, "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if p.Config.Name != "my-project" {
		t.Fatalf("Name = %q, want the directory name", p.Config.Name)
	}
}

func TestInitRefusesToOverwrite(t *testing.T) {
	root := t.TempDir()
	if _, err := Init(root, "first", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := Init(root, "second", time.Now()); !errors.Is(err, ErrExists) {
		t.Fatalf("second Init = %v, want ErrExists", err)
	}

	// The original descriptor must be untouched.
	p, err := Open(filepath.Join(root, DirName))
	if err != nil {
		t.Fatal(err)
	}
	if p.Config.Name != "first" {
		t.Fatalf("Name = %q, the existing project was overwritten", p.Config.Name)
	}
}

func TestDiscoverFindsNearestProjectFromNestedDirectory(t *testing.T) {
	root := t.TempDir()
	if _, err := Init(root, "outer", time.Now()); err != nil {
		t.Fatal(err)
	}

	deep := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}

	p, err := Discover(deep)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if p.Config.Name != "outer" {
		t.Fatalf("Name = %q", p.Config.Name)
	}
}

func TestDiscoverPrefersTheNearestProject(t *testing.T) {
	root := t.TempDir()
	if _, err := Init(root, "outer", time.Now()); err != nil {
		t.Fatal(err)
	}
	inner := filepath.Join(root, "sub")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Init(inner, "inner", time.Now()); err != nil {
		t.Fatal(err)
	}

	p, err := Discover(inner)
	if err != nil {
		t.Fatal(err)
	}
	if p.Config.Name != "inner" {
		t.Fatalf("Name = %q, want the nearest project", p.Config.Name)
	}
}

// A bare directory left by a partial checkout must not shadow a real project
// further up the tree.
func TestDiscoverIgnoresDirectoryWithoutConfig(t *testing.T) {
	root := t.TempDir()
	if _, err := Init(root, "outer", time.Now()); err != nil {
		t.Fatal(err)
	}

	empty := filepath.Join(root, "sub", DirName)
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}

	p, err := Discover(filepath.Join(root, "sub"))
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if p.Config.Name != "outer" {
		t.Fatalf("Name = %q, an empty directory shadowed the real project", p.Config.Name)
	}
}

func TestDiscoverReportsNotFound(t *testing.T) {
	if _, err := Discover(t.TempDir()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Discover = %v, want ErrNotFound", err)
	}
}

func TestOpenRejectsMalformedConfig(t *testing.T) {
	dir := filepath.Join(t.TempDir(), DirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ConfigFile), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Open(dir); err == nil {
		t.Fatal("Open accepted a malformed config")
	}
}

// EventsRoot is the seam that lets the logs move into a worktree on another
// branch later without touching load or replay.
func TestEventsRootRelocatesTheLogs(t *testing.T) {
	p := initProject(t)

	if got, want := p.EventsDir(), filepath.Join(p.Dir, EventsDirName); got != want {
		t.Fatalf("default EventsDir = %q, want %q", got, want)
	}

	p.Config.EventsRoot = "../elsewhere"
	rel := p.EventsDir()
	if !strings.HasSuffix(rel, filepath.Join("elsewhere", EventsDirName)) {
		t.Fatalf("relative EventsRoot gave %q", rel)
	}

	abs := filepath.Join(t.TempDir(), "worktree")
	p.Config.EventsRoot = abs
	if got, want := p.EventsDir(), filepath.Join(abs, EventsDirName); got != want {
		t.Fatalf("absolute EventsRoot gave %q, want %q", got, want)
	}
}

func TestLogNameRoundTripsTheAuthorID(t *testing.T) {
	a := testActor(t, "Shakeeb Alireza")
	name := LogName(a)

	if !strings.HasSuffix(name, LogExt) {
		t.Fatalf("LogName = %q, missing extension", name)
	}
	got, ok := ParseLogName(name)
	if !ok {
		t.Fatalf("ParseLogName(%q) failed", name)
	}
	if got != a.ID {
		t.Fatalf("author id = %q, want %q", got, a.ID)
	}
}

// A display name may contain dots, so only the first one separates the id.
func TestLogNameHandlesDottedDisplayName(t *testing.T) {
	a := testActor(t, "J. R. Hacker")

	got, ok := ParseLogName(LogName(a))
	if !ok || got != a.ID {
		t.Fatalf("ParseLogName = %q, %v; want %q", got, ok, a.ID)
	}
}

// A case-folding filesystem must not split one person into two authors.
func TestParseLogNameRestoresCanonicalCase(t *testing.T) {
	a := testActor(t, "sa")
	lowered := strings.ToLower(LogName(a))

	got, ok := ParseLogName(lowered)
	if !ok {
		t.Fatalf("ParseLogName(%q) failed", lowered)
	}
	if got != a.ID {
		t.Fatalf("author id = %q, want the canonical %q", got, a.ID)
	}
}

func TestParseLogNameRejectsNonLogs(t *testing.T) {
	for _, in := range []string{"notes.md", "README", "events.jsonl", "backup.jsonl~", ".jsonl"} {
		if _, ok := ParseLogName(in); ok {
			t.Errorf("ParseLogName(%q) accepted a non-log", in)
		}
	}
}

func TestSanitizeRemovesPathSeparatorsAndCase(t *testing.T) {
	a := Actor{ID: ulid.NewGenerator().New(), Name: "../../etc/pa ss wd"}
	name := LogName(a)

	if strings.ContainsAny(name, `/\`) {
		t.Fatalf("LogName = %q leaks a path separator", name)
	}
	if name != strings.ToLower(name) {
		t.Fatalf("LogName = %q is not lowercased", name)
	}
	// Writing it must land inside the events directory and nowhere else.
	p := initProject(t)
	if err := Append(p, a, []event.Event{{ID: ulid.NewGenerator().New(), Action: event.AddNote}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(p.EventsDir(), name)); err != nil {
		t.Fatalf("log did not land in the events directory: %v", err)
	}
}

func TestAppendAndLoadRoundTrip(t *testing.T) {
	p := initProject(t)
	a := testActor(t, "sa")
	want := writeEvents(t, p, a, 5, "")

	got, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.Events) != len(want) {
		t.Fatalf("loaded %d events, want %d", len(got.Events), len(want))
	}
	for i := range want {
		if got.Events[i].ID != want[i].ID {
			t.Fatalf("event %d = %s, want %s", i, got.Events[i].ID, want[i].ID)
		}
		if got.Events[i].UserID != a.ID {
			t.Fatalf("event %d author = %q, want %q", i, got.Events[i].UserID, a.ID)
		}
	}
}

func TestLoadOnEmptyProject(t *testing.T) {
	p := initProject(t)

	got, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.Events) != 0 {
		t.Fatalf("empty project loaded %d events", len(got.Events))
	}
}

// A project whose events directory has not been created yet is valid, not an
// error: a fresh clone can arrive that way.
func TestLoadWithMissingEventsDirectory(t *testing.T) {
	p := initProject(t)
	if err := os.RemoveAll(p.EventsDir()); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(p); err != nil {
		t.Fatalf("Load: %v", err)
	}
}

// The point of per-author files: two people's work merges into one ordering
// without either file being touched.
func TestLoadMergesMultipleAuthors(t *testing.T) {
	p := initProject(t)
	alice := testActor(t, "alice")
	bob := testActor(t, "bob")

	shared := writeEvents(t, p, alice, 2, "")
	writeEvents(t, p, bob, 3, shared[len(shared)-1].ID)

	got, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Events) != 5 {
		t.Fatalf("loaded %d events, want 5", len(got.Events))
	}

	authors := map[string]int{}
	for _, e := range got.Events {
		authors[e.UserID]++
	}
	if authors[alice.ID] != 2 || authors[bob.ID] != 3 {
		t.Fatalf("authorship lost: %v", authors)
	}
	// Bob branched from Alice's last event, so her run must come first.
	if got.Events[0].UserID != alice.ID || got.Events[2].UserID != bob.ID {
		t.Fatalf("merged order is wrong: %v", authors)
	}
}

// Load must not depend on the order the filesystem happens to list files in.
func TestLoadIsDeterministicAcrossAuthorCount(t *testing.T) {
	p := initProject(t)

	root := ""
	for i := 0; i < 6; i++ {
		a := testActor(t, string(rune('a'+i)))
		ev := writeEvents(t, p, a, 4, root)
		root = ev[len(ev)-1].ID
	}

	first, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	for trial := 0; trial < 20; trial++ {
		again, err := Load(p)
		if err != nil {
			t.Fatal(err)
		}
		for i := range first.Events {
			if first.Events[i].ID != again.Events[i].ID {
				t.Fatalf("trial %d diverged at %d", trial, i)
			}
		}
	}
}

func TestLoadIgnoresForeignFiles(t *testing.T) {
	p := initProject(t)
	a := testActor(t, "sa")
	writeEvents(t, p, a, 2, "")

	for name, body := range map[string]string{
		"README.md":       "hello",
		"scratch.jsonl":   `{"v":1,"id":"x"}`,
		"notes.txt":       "hello",
		".DS_Store":       "",
		"backup.jsonl.sw": "",
	} {
		if err := os.WriteFile(filepath.Join(p.EventsDir(), name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.Events) != 2 {
		t.Fatalf("loaded %d events, want only the 2 real ones", len(got.Events))
	}
}

// A corrupt line is a real problem and must be reported with its location,
// not silently dropped.
func TestLoadReportsCorruptLineWithLocation(t *testing.T) {
	p := initProject(t)
	a := testActor(t, "sa")
	writeEvents(t, p, a, 2, "")

	path := filepath.Join(p.EventsDir(), LogName(a))
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("{ this is not json\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	_, err = Load(p)
	if err == nil {
		t.Fatal("Load accepted a corrupt line")
	}
	if !strings.Contains(err.Error(), ":3:") {
		t.Fatalf("error does not name the line: %v", err)
	}
}

// Events from a newer gnotes must be stepped over, not treated as corruption,
// and must be counted so the user can be told to upgrade.
func TestLoadSkipsUnknownActionsAndCountsThem(t *testing.T) {
	p := initProject(t)
	a := testActor(t, "sa")
	writeEvents(t, p, a, 2, "")

	path := filepath.Join(p.EventsDir(), LogName(a))
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	g := ulid.NewGenerator()
	for i := 0; i < 3; i++ {
		if _, err := f.WriteString(`{"v":1,"id":"` + g.New() + `","a":"teleport.node"}` + "\n"); err != nil {
			t.Fatal(err)
		}
	}
	f.Close()

	got, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.Events) != 2 {
		t.Fatalf("loaded %d events, want the 2 understood ones", len(got.Events))
	}
	if got.Skipped["teleport.node"] != 3 {
		t.Fatalf("Skipped = %v, want 3 teleport.node", got.Skipped)
	}
}

func TestLoadToleratesBlankLines(t *testing.T) {
	p := initProject(t)
	a := testActor(t, "sa")
	writeEvents(t, p, a, 2, "")

	path := filepath.Join(p.EventsDir(), LogName(a))
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString("\n\n   \n")
	f.Close()

	got, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.Events) != 2 {
		t.Fatalf("loaded %d events, want 2", len(got.Events))
	}
}

// A log whose final line has no newline, as a crashed writer might leave,
// must still parse.
func TestLoadReadsFinalLineWithoutNewline(t *testing.T) {
	p := initProject(t)
	a := testActor(t, "sa")

	line, err := event.Encode(event.Event{ID: ulid.NewGenerator().New(), Action: event.AddNote})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.EventsDir(), LogName(a)), line, 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.Events) != 1 {
		t.Fatalf("loaded %d events, want 1", len(got.Events))
	}
}

func TestAppendRejectsUnconfiguredActor(t *testing.T) {
	p := initProject(t)
	e := []event.Event{{ID: ulid.NewGenerator().New(), Action: event.AddNote}}

	for name, a := range map[string]Actor{
		"no id":      {Name: "sa"},
		"bad id":     {ID: "nope", Name: "sa"},
		"no name":    {ID: ulid.NewGenerator().New()},
		"blank name": {ID: ulid.NewGenerator().New(), Name: "   "},
	} {
		if err := Append(p, a, e); err == nil {
			t.Errorf("%s: Append accepted an invalid actor", name)
		}
	}
}

func TestAppendOfNothingIsANoOp(t *testing.T) {
	p := initProject(t)
	a := testActor(t, "sa")

	if err := Append(p, a, nil); err != nil {
		t.Fatalf("Append(nil): %v", err)
	}
	if _, err := os.Stat(filepath.Join(p.EventsDir(), LogName(a))); !os.IsNotExist(err) {
		t.Fatal("an empty append created a log file")
	}
}

func TestAppendIsAdditive(t *testing.T) {
	p := initProject(t)
	a := testActor(t, "sa")

	first := writeEvents(t, p, a, 2, "")
	writeEvents(t, p, a, 3, first[len(first)-1].ID)

	got, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Events) != 5 {
		t.Fatalf("loaded %d events, want 5; the second append overwrote the first", len(got.Events))
	}
}

// Two processes appending as the same person must not interleave a batch,
// because a half-written command would replay as a different operation.
func TestConcurrentAppendsKeepBatchesIntact(t *testing.T) {
	p := initProject(t)
	a := testActor(t, "sa")

	const writers, perBatch = 8, 4
	done := make(chan error, writers)

	for w := 0; w < writers; w++ {
		go func() {
			g := ulid.NewGenerator()
			batch := make([]event.Event, perBatch)
			ref := ""
			for i := range batch {
				batch[i] = event.Event{ID: g.New(), Ref: ref, Action: event.AddNote}
				ref = batch[i].ID
			}
			done <- Append(p, a, batch)
		}()
	}
	for w := 0; w < writers; w++ {
		if err := <-done; err != nil {
			t.Fatalf("concurrent Append: %v", err)
		}
	}

	got, err := Load(p)
	if err != nil {
		t.Fatalf("Load after concurrent appends: %v", err)
	}
	if len(got.Events) != writers*perBatch {
		t.Fatalf("loaded %d events, want %d; a batch was torn", len(got.Events), writers*perBatch)
	}
}

func BenchmarkLoad(b *testing.B) {
	dir := b.TempDir()
	p, err := Init(dir, "bench", time.Now())
	if err != nil {
		b.Fatal(err)
	}

	// Four collaborators, 5000 events each: a large but plausible project.
	g := ulid.NewGenerator()
	for w := 0; w < 4; w++ {
		a := Actor{ID: g.New(), Name: string(rune('a' + w))}
		batch := make([]event.Event, 5000)
		ref := ""
		for i := range batch {
			batch[i] = event.Event{
				ID: g.New(), Ref: ref, Action: event.AddNote,
				Payload: event.Payload{ID: g.New(), Parent: g.New(), Rank: "7fffffffffffffffffffffff", Title: "a note about something"},
			}
			ref = batch[i].ID
		}
		if err := Append(p, a, batch); err != nil {
			b.Fatal(err)
		}
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Load(p); err != nil {
			b.Fatal(err)
		}
	}
}
