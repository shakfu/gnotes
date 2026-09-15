package cli

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shakfu/gnotes/internal/event"
	"github.com/shakfu/gnotes/internal/rank"
	"github.com/shakfu/gnotes/internal/store"
	"github.com/shakfu/gnotes/internal/ulid"
)

// fixture drives the whole command line in-process: no subprocess, no
// terminal, and a temporary home so the real user identity is untouched.
type fixture struct {
	t   *testing.T
	dir string
	now time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	// The identity lives outside any project, so it has to be redirected or
	// the tests would read and overwrite the developer's own.
	t.Setenv("GNOTES_HOME", t.TempDir())

	f := &fixture{
		t:   t,
		dir: t.TempDir(),
		now: time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC),
	}
	f.mustRun("init", "demo", "--user", "sa")
	return f
}

// run executes one command and returns its streams and exit status.
func (f *fixture) run(args ...string) (stdout, stderr string, code int) {
	f.t.Helper()

	var out, errBuf bytes.Buffer
	app := &App{
		Stdout: &out,
		Stderr: &errBuf,
		Stdin:  strings.NewReader(""),
		Dir:    f.dir,
		Now:    func() time.Time { return f.now },
		Color:  false,
	}
	code = app.Run(args)
	return out.String(), errBuf.String(), code
}

// runIn executes a command with something on standard input.
func (f *fixture) runIn(stdin string, args ...string) (string, string, int) {
	f.t.Helper()

	var out, errBuf bytes.Buffer
	app := &App{
		Stdout: &out,
		Stderr: &errBuf,
		Stdin:  strings.NewReader(stdin),
		Dir:    f.dir,
		Now:    func() time.Time { return f.now },
		Color:  false,
	}
	code := app.Run(args)
	return out.String(), errBuf.String(), code
}

// mustRun fails the test if the command does not succeed.
func (f *fixture) mustRun(args ...string) string {
	f.t.Helper()
	stdout, stderr, code := f.run(args...)
	if code != 0 {
		f.t.Fatalf("gnotes %s: exit %d\nstdout: %s\nstderr: %s",
			strings.Join(args, " "), code, stdout, stderr)
	}
	return stdout
}

// refOf extracts the handle from a creation command's output, which is the
// first field of the first line.
func (f *fixture) refOf(output string) string {
	f.t.Helper()
	fields := strings.Fields(output)
	if len(fields) == 0 {
		f.t.Fatalf("no handle in output: %q", output)
	}
	return fields[0]
}

func TestInitCreatesEverything(t *testing.T) {
	f := newFixture(t)

	if _, err := os.Stat(filepath.Join(f.dir, store.DirName, store.DBFile)); err != nil {
		t.Fatalf("no database: %v", err)
	}
	out := f.mustRun("info")
	if !strings.Contains(out, "demo") {
		t.Fatalf("info does not name the project:\n%s", out)
	}
}

func TestInitRefusesTwice(t *testing.T) {
	f := newFixture(t)
	if _, _, code := f.run("init"); code == 0 {
		t.Fatal("a second init succeeded")
	}
}

// The identity is global, so a second project must not ask for it again.
func TestIdentityIsSharedBetweenProjects(t *testing.T) {
	f := newFixture(t)

	other := t.TempDir()
	var out, errBuf bytes.Buffer
	app := &App{Stdout: &out, Stderr: &errBuf, Stdin: strings.NewReader(""), Dir: other, Now: func() time.Time { return f.now }}
	if code := app.Run([]string{"init", "second"}); code != 0 {
		t.Fatalf("init in a second project failed: %s%s", out.String(), errBuf.String())
	}
	if strings.Contains(out.String(), "Your name:") {
		t.Fatal("the second project asked for a name again")
	}
}

func TestNoteAndTaskLifecycle(t *testing.T) {
	f := newFixture(t)

	f.mustRun("nb", "work")
	noteRef := f.refOf(f.mustRun("note", "design sketch", "-b", "work", "-m", "some prose", "-t", "design"))
	taskRef := f.refOf(f.mustRun("task", "fix the lexer", "-b", "work", "-t", "bug", "-d", "2026-09-01", "-p", "high"))

	out := f.mustRun("ls")
	for _, want := range []string{"design sketch", "fix the lexer", "#bug", "#design", "!high", "due:2026-09-01"} {
		if !strings.Contains(out, want) {
			t.Errorf("ls output is missing %q:\n%s", want, out)
		}
	}
	// A note has no checkbox; a task does.
	if !strings.Contains(out, "[ ]") {
		t.Errorf("no open-task marker:\n%s", out)
	}

	f.mustRun("done", taskRef)
	if out := f.mustRun("ls"); !strings.Contains(out, "[x]") {
		t.Errorf("the task is not marked done:\n%s", out)
	}

	out = f.mustRun("show", noteRef)
	if !strings.Contains(out, "some prose") {
		t.Errorf("show does not print the body:\n%s", out)
	}
}

// A first note should work without setting a notebook up first.
func TestNoteWithoutANotebookCreatesInbox(t *testing.T) {
	f := newFixture(t)
	f.mustRun("note", "straight in")

	out := f.mustRun("nb")
	if !strings.Contains(out, "inbox") {
		t.Fatalf("no inbox notebook was created:\n%s", out)
	}
}

func TestBodyFromStdin(t *testing.T) {
	f := newFixture(t)

	stdout, stderr, code := f.runIn("piped body text\nsecond line\n", "note", "captured", "--stdin")
	if code != 0 {
		t.Fatalf("exit %d: %s%s", code, stdout, stderr)
	}

	out := f.mustRun("show", f.refOf(stdout))
	if !strings.Contains(out, "piped body text") || !strings.Contains(out, "second line") {
		t.Fatalf("the piped body was not stored:\n%s", out)
	}
}

func TestListFilters(t *testing.T) {
	f := newFixture(t)
	f.mustRun("nb", "work")
	f.mustRun("nb", "personal")
	f.mustRun("note", "design sketch", "-b", "work", "-t", "design")
	f.mustRun("task", "fix the lexer", "-b", "work", "-t", "bug", "-p", "high")
	f.mustRun("task", "buy milk", "-b", "personal")

	cases := []struct {
		name    string
		args    []string
		want    []string
		notWant []string
	}{
		{"by notebook", []string{"ls", "-b", "personal"}, []string{"buy milk"}, []string{"design sketch"}},
		{"by kind", []string{"ls", "-k", "note"}, []string{"design sketch"}, []string{"buy milk"}},
		{"by tag", []string{"ls", "-t", "bug"}, []string{"fix the lexer"}, []string{"buy milk"}},
		{"by priority", []string{"ls", "-p", "high"}, []string{"fix the lexer"}, []string{"buy milk"}},
		{"by status", []string{"ls", "-s", "open"}, []string{"fix the lexer", "buy milk"}, []string{"design sketch"}},
		{"by text", []string{"ls", "milk"}, []string{"buy milk"}, []string{"fix the lexer"}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := f.mustRun(c.args...)
			for _, want := range c.want {
				if !strings.Contains(out, want) {
					t.Errorf("missing %q:\n%s", want, out)
				}
			}
			for _, notWant := range c.notWant {
				if strings.Contains(out, notWant) {
					t.Errorf("unexpectedly present %q:\n%s", notWant, out)
				}
			}
		})
	}
}

func TestJSONOutputIsStructured(t *testing.T) {
	f := newFixture(t)
	f.mustRun("nb", "work")
	f.mustRun("task", "fix the lexer", "-b", "work", "-t", "bug", "-p", "high", "-d", "2026-09-01")

	out := f.mustRun("ls", "--json")

	var got []jsonNode
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}

	n := got[0]
	if n.Kind != "task" || n.Title != "fix the lexer" || n.Status != "open" {
		t.Errorf("entry = %+v", n)
	}
	if n.Priority != "high" || n.Due != "2026-09-01" {
		t.Errorf("task fields = %+v", n)
	}
	if n.Notebook != "work" {
		t.Errorf("Notebook = %q", n.Notebook)
	}
	// The handle in JSON must be the same one the tables print, so a script
	// can feed it straight back in.
	if !strings.HasSuffix(n.ID, n.Ref) {
		t.Errorf("Ref %q is not a suffix of ID %q", n.Ref, n.ID)
	}
}

func TestSearch(t *testing.T) {
	f := newFixture(t)
	f.mustRun("note", "parser design", "-m", "The lexer tokenizes input.")
	f.mustRun("note", "shopping list", "-m", "milk and bread")

	out := f.mustRun("search", "tokenizes")
	if !strings.Contains(out, "parser design") {
		t.Fatalf("search missed a body match:\n%s", out)
	}
	if strings.Contains(out, "shopping") {
		t.Fatalf("search returned an unrelated note:\n%s", out)
	}

	if out := f.mustRun("search", "nothinglikethis"); !strings.Contains(out, "nothing matches") {
		t.Fatalf("a fruitless search said: %s", out)
	}
}

// Entries can be named by handle or by title, since that is what people type.
func TestReferencesResolveByHandleAndTitle(t *testing.T) {
	f := newFixture(t)
	ref := f.refOf(f.mustRun("task", "fix the lexer"))

	for _, r := range []string{ref, strings.ToLower(ref), "fix the lexer", "fix"} {
		if _, _, code := f.run("show", r); code != 0 {
			t.Errorf("could not resolve %q", r)
		}
	}
}

func TestAmbiguousReferenceIsReportedWithCandidates(t *testing.T) {
	f := newFixture(t)
	f.mustRun("note", "parser one")
	f.mustRun("note", "parser two")

	_, stderr, code := f.run("show", "parser")
	if code == 0 {
		t.Fatal("an ambiguous reference was accepted")
	}
	for _, want := range []string{"parser one", "parser two"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("the error does not list %q: %s", want, stderr)
		}
	}
}

func TestTaskVerbsRefuseNotes(t *testing.T) {
	f := newFixture(t)
	ref := f.refOf(f.mustRun("note", "just a note"))

	for _, args := range [][]string{
		{"done", ref},
		{"due", ref, "friday"},
		{"prio", ref, "high"},
		{"assign", ref, "me"},
	} {
		_, stderr, code := f.run(args...)
		if code == 0 {
			t.Errorf("%v succeeded on a note", args)
		}
		if !strings.Contains(stderr, "not a task") {
			t.Errorf("%v: unhelpful error %q", args, stderr)
		}
	}
}

func TestTagsAndUntag(t *testing.T) {
	f := newFixture(t)
	ref := f.refOf(f.mustRun("note", "tagged thing"))

	f.mustRun("tag", ref, "bug", "#Perf", "needs review")
	out := f.mustRun("tags")
	for _, want := range []string{"#bug", "#perf", "#needs-review"} {
		if !strings.Contains(out, want) {
			t.Errorf("tags is missing %q:\n%s", want, out)
		}
	}

	f.mustRun("untag", ref, "bug")
	if out := f.mustRun("tags"); strings.Contains(out, "#bug") {
		t.Errorf("the tag survived removal:\n%s", out)
	}
}

func TestDeleteAndRestore(t *testing.T) {
	f := newFixture(t)
	ref := f.refOf(f.mustRun("note", "temporary"))

	f.mustRun("rm", ref)
	if out := f.mustRun("ls"); strings.Contains(out, "temporary") {
		t.Fatalf("the deleted note is still listed:\n%s", out)
	}

	f.mustRun("restore", ref)
	if out := f.mustRun("ls"); !strings.Contains(out, "temporary") {
		t.Fatalf("the note was not restored:\n%s", out)
	}
}

func TestMoveBetweenNotebooks(t *testing.T) {
	f := newFixture(t)
	f.mustRun("nb", "work")
	f.mustRun("nb", "personal")
	ref := f.refOf(f.mustRun("note", "wanderer", "-b", "work"))

	f.mustRun("mv", ref, "personal")

	if out := f.mustRun("ls", "-b", "personal"); !strings.Contains(out, "wanderer") {
		t.Fatalf("the note did not arrive:\n%s", out)
	}
	if out := f.mustRun("ls", "-b", "work"); strings.Contains(out, "wanderer") {
		t.Fatalf("the note is still in its old notebook:\n%s", out)
	}
}

func TestReorderWithBeforeAndAfter(t *testing.T) {
	f := newFixture(t)
	f.mustRun("nb", "work")
	first := f.refOf(f.mustRun("note", "first", "-b", "work"))
	second := f.refOf(f.mustRun("note", "second", "-b", "work"))

	f.mustRun("mv", first, "--after", second)

	out := f.mustRun("ls", "-b", "work")
	if strings.Index(out, "second") > strings.Index(out, "first") {
		t.Fatalf("the order did not change:\n%s", out)
	}
}

func TestLinkShowsBothDirections(t *testing.T) {
	f := newFixture(t)
	note := f.refOf(f.mustRun("note", "design sketch"))
	task := f.refOf(f.mustRun("task", "implement it"))

	f.mustRun("link", task, note)

	if out := f.mustRun("show", task); !strings.Contains(out, "design sketch") {
		t.Errorf("the outgoing link is not shown:\n%s", out)
	}
	out := f.mustRun("show", note)
	if !strings.Contains(out, "referenced by") || !strings.Contains(out, "implement it") {
		t.Errorf("the backlink is not shown:\n%s", out)
	}
}

// Relative dates must be resolved against the command's clock, not read back
// later.
func TestRelativeDueDates(t *testing.T) {
	f := newFixture(t) // a Monday
	ref := f.refOf(f.mustRun("task", "ship it"))

	f.mustRun("due", ref, "friday")
	if out := f.mustRun("show", ref); !strings.Contains(out, "2026-08-21") {
		t.Fatalf("friday did not resolve to a date:\n%s", out)
	}

	f.mustRun("due", ref, "none")
	if out := f.mustRun("show", ref); strings.Contains(out, "2026-08-21") {
		t.Fatalf("the due date was not cleared:\n%s", out)
	}
}

func TestOverdueFilter(t *testing.T) {
	f := newFixture(t)
	past := f.refOf(f.mustRun("task", "long overdue", "-d", "2020-01-01"))
	f.mustRun("task", "not yet", "-d", "2030-01-01")

	out := f.mustRun("ls", "--overdue")
	if !strings.Contains(out, "long overdue") || strings.Contains(out, "not yet") {
		t.Fatalf("the overdue filter is wrong:\n%s", out)
	}

	// A completed task stops being overdue.
	f.mustRun("done", past)
	if out := f.mustRun("ls", "--overdue"); strings.Contains(out, "long overdue") {
		t.Fatalf("a done task is still reported overdue:\n%s", out)
	}
}

// Time travel replays a prefix of the log; nothing is stored for it.
func TestTimeTravel(t *testing.T) {
	f := newFixture(t)
	f.mustRun("note", "early")

	cutoff := f.now.Add(time.Second)
	f.now = f.now.Add(48 * time.Hour)
	f.mustRun("note", "later")

	out := f.mustRun("ls", "--at", cutoff.Format("2006-01-02T15:04:05Z07:00"))
	if !strings.Contains(out, "early") {
		t.Errorf("the past state is missing what existed then:\n%s", out)
	}
	if strings.Contains(out, "later") {
		t.Errorf("the past state contains what came after:\n%s", out)
	}

	// And a duration is accepted where a date is.
	if out := f.mustRun("ls", "--at", "1d"); strings.Contains(out, "later") {
		t.Errorf("a relative cutoff was misread:\n%s", out)
	}
	// The present is unchanged.
	if out := f.mustRun("ls"); !strings.Contains(out, "later") {
		t.Errorf("time travel changed the present:\n%s", out)
	}
}

func TestLogShowsEvents(t *testing.T) {
	f := newFixture(t)
	ref := f.refOf(f.mustRun("task", "fix the lexer"))
	f.mustRun("done", ref)

	out := f.mustRun("log")
	for _, want := range []string{"add.task", "set.status", "sa"} {
		if !strings.Contains(out, want) {
			t.Errorf("log is missing %q:\n%s", want, out)
		}
	}

	// Scoped to one entry, only its events appear.
	scoped := f.mustRun("log", ref)
	if !strings.Contains(scoped, "set.status") {
		t.Errorf("scoped log is missing the status change:\n%s", scoped)
	}
	if strings.Contains(scoped, "init.workspace") {
		t.Errorf("scoped log includes unrelated events:\n%s", scoped)
	}
}

func TestWhoami(t *testing.T) {
	f := newFixture(t)

	if out := f.mustRun("whoami"); !strings.Contains(out, "sa") {
		t.Fatalf("whoami = %s", out)
	}

	f.mustRun("whoami", "--set", "Shakeeb")
	out := f.mustRun("whoami")
	if !strings.Contains(out, "Shakeeb") {
		t.Fatalf("the rename did not take: %s", out)
	}

	// The id must survive a rename, or every past event is orphaned.
	f.mustRun("note", "after the rename")
	if out := f.mustRun("log"); strings.Count(out, "Shakeeb") < 1 {
		t.Fatalf("events are not attributed to the new name:\n%s", out)
	}
}

func TestUnknownCommandSuggestsAndExitsTwo(t *testing.T) {
	f := newFixture(t)

	_, stderr, code := f.run("serach", "x")
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, "search") {
		t.Errorf("no suggestion offered: %s", stderr)
	}
}

func TestUsageErrorsExitTwo(t *testing.T) {
	f := newFixture(t)

	for _, args := range [][]string{
		{"note"},
		{"show"},
		{"tag", "x"},
		{"due", "x"},
	} {
		_, stderr, code := f.run(args...)
		if code != 2 {
			t.Errorf("%v: exit = %d, want 2", args, code)
		}
		if !strings.Contains(stderr, "usage:") {
			t.Errorf("%v: no usage line: %s", args, stderr)
		}
	}
}

func TestCommandsOutsideAProjectExplainThemselves(t *testing.T) {
	t.Setenv("GNOTES_HOME", t.TempDir())

	var out, errBuf bytes.Buffer
	app := &App{Stdout: &out, Stderr: &errBuf, Stdin: strings.NewReader(""), Dir: t.TempDir(), Now: time.Now}

	if code := app.Run([]string{"ls"}); code == 0 {
		t.Fatal("ls succeeded outside a project")
	}
	if !strings.Contains(errBuf.String(), "gnotes init") {
		t.Fatalf("the error does not say what to do: %s", errBuf.String())
	}
}

func TestHelp(t *testing.T) {
	f := newFixture(t)

	out := f.mustRun("help")
	for _, want := range []string{"note", "task", "ls", "log", "search"} {
		if !strings.Contains(out, want) {
			t.Errorf("help is missing %q", want)
		}
	}

	// Every command must have its own page, or help is a lie.
	for _, c := range commands {
		out, stderr, code := f.run("help", c.name)
		if code != 0 {
			t.Errorf("help %s: exit %d %s", c.name, code, stderr)
		}
		if !strings.Contains(out, c.summary) {
			t.Errorf("help %s does not print its summary", c.name)
		}
	}
}

// Aliases exist to be typed, so each must actually dispatch.
func TestAliasesDispatch(t *testing.T) {
	f := newFixture(t)
	ref := f.refOf(f.mustRun("task", "aliased"))

	for _, args := range [][]string{
		{"n", "via alias"},
		{"t", "task via alias"},
		{"list"},
		{"s", "aliased"},
		{"cat", ref},
		{"prio", ref, "low"},
		{"doing", ref},
		{"reopen", ref},
		{"stat"},
	} {
		if _, stderr, code := f.run(args...); code != 0 {
			t.Errorf("%v failed: %s", args, stderr)
		}
	}
}

// The status aliases carry their meaning in the command name.
func TestStatusAliasesSetTheRightStatus(t *testing.T) {
	f := newFixture(t)
	ref := f.refOf(f.mustRun("task", "cycle me"))

	f.mustRun("doing", ref)
	if out := f.mustRun("ls"); !strings.Contains(out, "[~]") {
		t.Errorf("doing did not take:\n%s", out)
	}
	f.mustRun("done", ref)
	if out := f.mustRun("ls"); !strings.Contains(out, "[x]") {
		t.Errorf("done did not take:\n%s", out)
	}
	f.mustRun("reopen", ref)
	if out := f.mustRun("ls"); !strings.Contains(out, "[ ]") {
		t.Errorf("reopen did not take:\n%s", out)
	}
}

func TestVersion(t *testing.T) {
	f := newFixture(t)
	if out := f.mustRun("--version"); strings.TrimSpace(out) == "" {
		t.Fatal("--version printed nothing")
	}
}

// Output must have no trailing whitespace, so it diffs and copies cleanly.
func TestTableRowsHaveNoTrailingWhitespace(t *testing.T) {
	f := newFixture(t)
	f.mustRun("nb", "work")
	f.mustRun("note", "a short one", "-b", "work")
	f.mustRun("task", "a much longer title than the other", "-b", "work", "-t", "tagged")

	for _, args := range [][]string{{"ls"}, {"nb"}, {"tags"}, {"info"}} {
		for i, line := range strings.Split(strings.TrimRight(f.mustRun(args...), "\n"), "\n") {
			if line != strings.TrimRight(line, " \t") {
				t.Errorf("%v line %d has trailing whitespace: %q", args, i, line)
			}
		}
	}
}

func TestParseWhen(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)

	cases := map[string]time.Time{
		"3d":         now.Add(-72 * time.Hour),
		"2h":         now.Add(-2 * time.Hour),
		"1w":         now.Add(-7 * 24 * time.Hour),
		"2026-08-01": time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
	}
	for in, want := range cases {
		got, err := parseWhen(in, now)
		if err != nil {
			t.Errorf("parseWhen(%q): %v", in, err)
			continue
		}
		if !got.Equal(want) {
			t.Errorf("parseWhen(%q) = %v, want %v", in, got, want)
		}
	}

	for _, bad := range []string{"whenever", "1.5d", "99999999999999999d", "-3d", "friday", "2026-09-01"} {
		if got, err := parseWhen(bad, now); err == nil {
			t.Errorf("parseWhen(%q) = %v, want an error", bad, got)
		}
	}

	// A date without a zone is the start of that day where the user is.
	pdt := time.FixedZone("PDT", -7*3600)
	got, err := parseWhen("2026-08-01", now.In(pdt))
	if err != nil || !got.Equal(time.Date(2026, 8, 1, 0, 0, 0, 0, pdt)) {
		t.Errorf("parseWhen(2026-08-01) in PDT = %v, %v; want local midnight", got, err)
	}
}

// showJSON returns one entry as the --json output decodes it.
func (f *fixture) showJSON(ref string) map[string]any {
	f.t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(f.mustRun("show", ref, "--json")), &out); err != nil {
		f.t.Fatalf("decode show --json: %v", err)
	}
	return out
}

// An unquoted multi-word reference names the whole title, not its first word.
// Taking only the first word marked or deleted a different entry.
func TestMultiWordReferencesAreNotCutToTheFirstWord(t *testing.T) {
	f := newFixture(t)
	f.mustRun("task", "fix the lexer")
	f.mustRun("task", "the plan")

	f.mustRun("done", "the", "lexer")
	if f.showJSON("the plan")["status"] == "done" {
		t.Fatal("done the lexer marked the plan done")
	}
	if f.showJSON("fix the lexer")["status"] != "done" {
		t.Fatal("done the lexer did not mark fix the lexer")
	}

	f.mustRun("status", "the", "plan", "doing")
	if f.showJSON("the plan")["status"] != "doing" {
		t.Fatal("status with a multi-word reference did not apply")
	}

	f.mustRun("prio", "the", "plan", "high")
	f.mustRun("due", "the", "plan", "friday")
	f.mustRun("rm", "the", "plan")
	if _, _, code := f.run("show", "fix the lexer"); code != 0 {
		t.Fatal("rm the plan deleted fix the lexer")
	}
	f.mustRun("restore", "the", "plan")
}

// When the words split into a reference and values in more than one valid
// way, the command refuses rather than picking one.
func TestAmbiguousSplitsAreRefused(t *testing.T) {
	f := newFixture(t)
	f.mustRun("task", "fix the lexer")

	_, stderr, code := f.run("tag", "fix", "the", "lexer", "bug")
	if code == 0 || !strings.Contains(stderr, "more than one way") {
		t.Fatalf("exit %d, stderr %q; want the split refused", code, stderr)
	}
	if tags, _ := f.showJSON("fix the lexer")["tags"].([]any); len(tags) != 0 {
		t.Fatalf("tags = %v, want none written", tags)
	}

	// Quoted, it is unambiguous.
	f.mustRun("tag", "fix the lexer", "bug")

	// Two references, each several words, split the one way both resolve.
	f.mustRun("note", "design sketch")
	f.mustRun("link", "fix", "the", "lexer", "design", "sketch")
	if links, _ := f.showJSON("fix the lexer")["links"].([]any); len(links) != 1 {
		t.Fatalf("links = %v, want the design sketch", links)
	}
}

// A title written by another SQLite client is untrusted. Printing it must not
// pass terminal escape sequences or newlines through.
func TestOutsideControlCharactersAreNotPrinted(t *testing.T) {
	f := newFixture(t)
	f.mustRun("note", "harmless", "-t", "x", "-m", "body")
	id := f.showJSON("harmless")["id"].(string)

	p, err := store.Discover(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	execSQL(t, p, `UPDATE nodes SET title = ?, body = ? WHERE id = ?`, "evil\x1b]0;PWNED\x07\x1b[2J\nforged row", "line\x1b[2J\nnext", id)

	for _, args := range [][]string{{"ls"}, {"show", id[len(id)-6:]}, {"log"}, {"search", "evil"}} {
		out := f.mustRun(args...)
		if strings.ContainsAny(out, "\x1b\x07") {
			t.Errorf("gnotes %s printed a control character: %q", strings.Join(args, " "), out)
		}
		if strings.Contains(out, "\nforged row") {
			t.Errorf("gnotes %s let a title break its line: %q", strings.Join(args, " "), out)
		}
	}
}

// "--" ends flag parsing, so a title beginning with a dash is a title.
func TestDoubleDashPassesADashedTitle(t *testing.T) {
	f := newFixture(t)
	f.mustRun("note", "--", "-x marks the spot")
	if f.showJSON("marks the spot")["title"] != "-x marks the spot" {
		t.Fatal("the dashed title was not kept")
	}
}

func TestEditCanClearABody(t *testing.T) {
	f := newFixture(t)
	f.mustRun("note", "cleared", "-m", "something")
	t.Setenv("EDITOR", "false")
	f.mustRun("edit", "cleared", "-m", "")
	if body, _ := f.showJSON("cleared")["body"].(string); body != "" {
		t.Fatalf("body = %q, want it cleared", body)
	}
	if _, _, code := f.run("edit", "cleared", "--title", ""); code == 0 {
		t.Fatal("an empty title was accepted")
	}
}

// A refused init must not have changed the global identity first.
func TestRefusedInitLeavesTheIdentityAlone(t *testing.T) {
	f := newFixture(t)
	if _, _, code := f.run("init", "--user", "someone else"); code == 0 {
		t.Fatal("init in an initialised project succeeded")
	}
	actor, err := store.LoadUser()
	if err != nil || actor.Name != "sa" {
		t.Fatalf("identity = %q, %v; want it unchanged", actor.Name, err)
	}
}

// A teammate on a fresh clone has the project but no identity. Init sets the
// identity and succeeds.
func TestInitOnAFreshCloneOnlySetsTheIdentity(t *testing.T) {
	f := newFixture(t)
	t.Setenv("GNOTES_HOME", t.TempDir())

	stdout, stderr, code := f.run("init", "--user", "bob")
	if code != 0 {
		t.Fatalf("exit %d: %s%s", code, stdout, stderr)
	}
	if actor, _ := store.LoadUser(); actor.Name != "bob" {
		t.Fatalf("identity = %q, want bob", actor.Name)
	}
}

// A project inside another project's directory would shadow it for every
// command run below.
func TestInitRefusesToNestAProject(t *testing.T) {
	f := newFixture(t)
	sub := filepath.Join(f.dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	f.dir = sub
	if _, stderr, code := f.run("init", "inner"); code == 0 || !strings.Contains(stderr, "already covers") {
		t.Fatalf("exit %d, stderr %q; want the nested init refused", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(sub, ".gnotes")); err == nil {
		t.Fatal("a nested project was created")
	}
}

func TestHelpFlagsAndUsageErrors(t *testing.T) {
	f := newFixture(t)

	stdout, _, code := f.run("tag", "x", "--help")
	if code != 0 || !strings.Contains(stdout, "usage: gnotes tag") {
		t.Fatalf("tag --help: exit %d, stdout %q", code, stdout)
	}
	if _, stderr, code := f.run("ls", "--bogus"); code != 2 || !strings.Contains(stderr, "bogus") {
		t.Fatalf("ls --bogus: exit %d, stderr %q; want 2 and the flag named", code, stderr)
	}
	if _, _, code := f.run("help", "nosuch"); code != 2 {
		t.Fatalf("help nosuch: exit %d, want 2 like an unknown command", code)
	}
}

func TestTyposGetASuggestion(t *testing.T) {
	for typed, want := range map[string]string{"shwo": "show", "sreach": "search", "tga": "tag"} {
		if got := closest(typed); got != want {
			t.Errorf("closest(%q) = %q, want %q", typed, got, want)
		}
	}
}

func TestStdinMustBeUTF8(t *testing.T) {
	f := newFixture(t)
	if _, stderr, code := f.runIn("bad \xff byte", "note", "binary", "--stdin"); code == 0 || !strings.Contains(stderr, "UTF-8") {
		t.Fatalf("exit %d, stderr %q; want non-UTF-8 input refused", code, stderr)
	}
}

func TestMovePlacementIsValidated(t *testing.T) {
	f := newFixture(t)
	f.mustRun("nb", "work")
	f.mustRun("nb", "home")
	f.mustRun("note", "here", "-b", "work")
	f.mustRun("note", "there", "-b", "home")

	for _, args := range [][]string{
		{"mv", "here"},                             // nowhere to go
		{"mv", "here", "--top", "--bottom"},        // two placements
		{"mv", "here", "--before", "there"},        // a sibling elsewhere
		{"mv", "here", "work", "--after", "there"}, // still elsewhere
	} {
		if _, _, code := f.run(args...); code == 0 {
			t.Errorf("gnotes %s succeeded", strings.Join(args, " "))
		}
	}
	f.mustRun("mv", "here", "home", "--before", "there")
}

func TestListingsShowAssigneesAndJSONHasStableArrays(t *testing.T) {
	f := newFixture(t)
	f.mustRun("task", "owned", "-a", "me")
	if out := f.mustRun("ls"); !strings.Contains(out, "@sa") {
		t.Fatalf("ls does not show the assignee:\n%s", out)
	}

	f.mustRun("note", "bare")
	got := f.showJSON("bare")
	for _, key := range []string{"tags", "links", "assignees"} {
		if _, ok := got[key].([]any); !ok {
			t.Errorf("%s = %v, want an array", key, got[key])
		}
	}
	if got["notebookId"] == "" || got["notebookId"] == nil {
		t.Error("notebookId is missing")
	}
}

func TestAssigningTwiceAndDuplicateNotebooks(t *testing.T) {
	f := newFixture(t)
	f.mustRun("task", "owned")
	f.mustRun("assign", "owned", "me")
	before := strings.Count(f.mustRun("log"), "add.assignee")
	f.mustRun("assign", "owned", "me")
	if after := strings.Count(f.mustRun("log"), "add.assignee"); after != before {
		t.Fatalf("assigning again wrote another event (%d -> %d)", before, after)
	}

	f.mustRun("nb", "work")
	if _, _, code := f.run("nb", "Work"); code == 0 {
		t.Fatal("a second notebook named Work was created")
	}
	if _, _, code := f.run("assign", "owned", ulid.NewGenerator().New()); code == 0 {
		t.Fatal("an id belonging to nobody was accepted as an assignee")
	}
}

// Overdue in a time-travelled listing is judged at the cutoff.
func TestOverdueAtACutoff(t *testing.T) {
	f := newFixture(t)
	f.mustRun("task", "deadline", "-d", "2026-08-20")
	f.now = f.now.AddDate(0, 0, 10)
	if out := f.mustRun("ls", "--at", "2026-08-19"); strings.Contains(out, "due:2026-08-20!") {
		t.Fatalf("a task due the next day was shown overdue at the cutoff:\n%s", out)
	}
}

// Global notes are reached only with a leading -g, wherever the command runs.
func TestGlobalNotesNeedTheFlag(t *testing.T) {
	f := newFixture(t)
	global := filepath.Join(t.TempDir(), "notes")

	out := f.mustRun("-g", "init", global)
	if !strings.Contains(out, "global notes: "+global) || !strings.Contains(out, "gnotes -g note") {
		t.Fatalf("-g init did not report the location:\n%s", out)
	}
	f.mustRun("-g", "note", "global entry")

	if out := f.mustRun("ls"); strings.Contains(out, "global entry") {
		t.Fatalf("the project listing shows a global note:\n%s", out)
	}
	if out := f.mustRun("-g", "ls"); !strings.Contains(out, "global entry") {
		t.Fatalf("-g ls misses the global note:\n%s", out)
	}

	// Outside any project, a command without -g still refuses.
	f.dir = t.TempDir()
	if _, stderr, code := f.run("ls"); code == 0 || !strings.Contains(stderr, "gnotes init") {
		t.Fatalf("ls outside a project: exit %d, stderr %q; want the usual refusal", code, stderr)
	}
	if out := f.mustRun("-g", "ls"); !strings.Contains(out, "global entry") {
		t.Fatalf("-g ls outside a project misses the global note:\n%s", out)
	}
}

func TestGlobalInitTwiceReportsTheLocation(t *testing.T) {
	f := newFixture(t)
	global := filepath.Join(t.TempDir(), "notes")
	f.mustRun("-g", "init", global)

	out := f.mustRun("-g", "init", filepath.Join(t.TempDir(), "elsewhere"))
	if !strings.Contains(out, "global notes location already exists: "+global) {
		t.Fatalf("second -g init:\n%s", out)
	}
}

func TestGlobalInitDefaultsToHomeNotes(t *testing.T) {
	f := newFixture(t)
	t.Setenv("HOME", t.TempDir())
	home, _ := os.UserHomeDir()

	f.mustRun("-g", "init")
	if _, err := os.Stat(filepath.Join(home, "notes", store.DirName, store.DBFile)); err != nil {
		t.Fatalf("no global project in ~/notes: %v", err)
	}
	if out := f.mustRun("-g", "info"); !strings.Contains(out, "global") {
		t.Fatalf("the global project is not named global:\n%s", out)
	}
}

// A clone of the global notes from another machine is used as it is.
func TestGlobalInitAdoptsAnExistingProject(t *testing.T) {
	f := newFixture(t)
	f.mustRun("note", "already here")
	project := f.dir

	f.dir = t.TempDir()
	f.mustRun("-g", "init", project)
	if out := f.mustRun("-g", "ls"); !strings.Contains(out, "already here") {
		t.Fatalf("-g ls does not show the adopted project:\n%s", out)
	}
}

func TestGlobalWithoutSetupSaysHow(t *testing.T) {
	f := newFixture(t)
	if _, stderr, code := f.run("-g", "ls"); code == 0 || !strings.Contains(stderr, "gnotes -g init") {
		t.Fatalf("exit %d, stderr %q; want a pointer to -g init", code, stderr)
	}
}

// A project from before the database is imported on first use, and says so once.
func TestLegacyProjectIsImportedWithANotice(t *testing.T) {
	f := newFixture(t)
	legacy := t.TempDir()
	events := filepath.Join(legacy, store.DirName, "events")
	if err := os.MkdirAll(events, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, store.DirName, "project.json"), []byte(`{"name":"old"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	author := ulid.NewGenerator().New()
	g := ulid.NewGenerator()
	ws, nb := g.New(), g.New()
	var log []byte
	for _, e := range []event.Event{
		{Action: event.InitWorkspace, Payload: event.Payload{ID: ws, Name: "old", Rank: rank.Mid()}},
		{Action: event.CreateContributor, Payload: event.Payload{ID: author, Name: "sa"}},
		{Action: event.AddNotebook, Payload: event.Payload{ID: nb, Parent: ws, Name: "inbox", Rank: rank.Mid()}},
		{Action: event.AddNote, Payload: event.Payload{ID: g.New(), Parent: nb, Title: "written before the database", Rank: rank.Mid()}},
	} {
		e.ID = g.New()
		line, err := event.Encode(e)
		if err != nil {
			t.Fatal(err)
		}
		log = append(append(log, line...), '\n')
	}
	if err := os.WriteFile(filepath.Join(events, author+".sa"+store.LogExt), log, 0o644); err != nil {
		t.Fatal(err)
	}

	f.dir = legacy
	out, stderr, code := f.run("ls")
	if code != 0 || !strings.Contains(out, "written before the database") {
		t.Fatalf("exit %d, stdout %q, stderr %q; want the imported note listed", code, out, stderr)
	}
	if !strings.Contains(stderr, "imported 4 events") {
		t.Fatalf("stderr %q does not report the import", stderr)
	}
	if _, stderr, _ := f.run("ls"); strings.Contains(stderr, "imported") {
		t.Fatalf("the second command reported the import again: %q", stderr)
	}
}

// execSQL runs a statement as another SQLite client would.
func execSQL(t *testing.T, p *store.Project, query string, args ...any) {
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
