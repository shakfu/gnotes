package gitsync

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shakfu/gnotes/internal/event"
	"github.com/shakfu/gnotes/internal/store"
	"github.com/shakfu/gnotes/internal/ulid"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// newRepo creates a git working tree with a gnotes project and one committed
// file, so HEAD exists.
func newRepo(t *testing.T) (root string, p *store.Project) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}

	root = t.TempDir()
	git(t, root, "init", "--initial-branch=main")
	git(t, root, "config", "user.email", "test@example.com")
	git(t, root, "config", "user.name", "Test")
	// Signing would prompt or fail in a sandbox.
	git(t, root, "config", "commit.gpgsign", "false")

	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", "README.md")
	git(t, root, "commit", "-m", "initial")

	p, err := store.Init(root, "demo", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return root, p
}

// writeEvent appends one event so the logs have something to commit.
func writeEvent(t *testing.T, p *store.Project) {
	t.Helper()
	a := store.Actor{ID: ulid.NewGenerator().New(), Name: "sa"}
	e := event.Event{ID: ulid.NewGenerator().New(), Action: event.AddNote, Payload: event.Payload{ID: "n", Title: "t"}}
	if err := store.Append(p, a, []event.Event{e}); err != nil {
		t.Fatal(err)
	}
}

func TestOpenFindsTheRepositoryRoot(t *testing.T) {
	root, p := newRepo(t)

	r, err := Open(p)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// macOS temp dirs are symlinked, so compare resolved paths.
	wantRoot, _ := filepath.EvalSymlinks(root)
	gotRoot, _ := filepath.EvalSymlinks(r.Root)
	if gotRoot != wantRoot {
		t.Fatalf("Root = %q, want %q", gotRoot, wantRoot)
	}
	if len(r.Paths) != 1 || r.Paths[0] != store.DirName {
		t.Fatalf("Paths = %v, want just %q", r.Paths, store.DirName)
	}
}

func TestOpenOutsideARepository(t *testing.T) {
	p, err := store.Init(t.TempDir(), "demo", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(p); !errors.Is(err, ErrNotARepo) {
		t.Fatalf("Open = %v, want ErrNotARepo", err)
	}
}

func TestCommitRecordsTheLogs(t *testing.T) {
	root, p := newRepo(t)
	writeEvent(t, p)

	r, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}

	dirty, err := r.Dirty()
	if err != nil {
		t.Fatal(err)
	}
	if !dirty {
		t.Fatal("new logs did not show as dirty")
	}

	committed, err := r.Commit("gnotes: 1 event")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if !committed {
		t.Fatal("Commit reported nothing to do")
	}

	if dirty, _ := r.Dirty(); dirty {
		t.Fatal("still dirty after committing")
	}
	if !strings.Contains(git(t, root, "log", "-1", "--pretty=%s"), "gnotes: 1 event") {
		t.Fatal("the commit message was not used")
	}
	if !strings.Contains(git(t, root, "show", "--stat", "--pretty=", "HEAD"), store.DirName) {
		t.Fatal("the commit does not contain the event logs")
	}
}

func TestCommitIsANoOpWhenNothingChanged(t *testing.T) {
	_, p := newRepo(t)
	r, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}

	writeEvent(t, p)
	if _, err := r.Commit("first"); err != nil {
		t.Fatal(err)
	}

	committed, err := r.Commit("second")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if committed {
		t.Fatal("Commit created an empty commit")
	}
}

// The critical safety property: syncing notes must never sweep the user's own
// staged work into a commit labelled as a notes sync.
func TestCommitLeavesTheUsersStagedWorkAlone(t *testing.T) {
	root, p := newRepo(t)

	// The user stages a change of their own.
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", "main.go")

	writeEvent(t, p)
	r, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit("gnotes: sync"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// The notes commit must not contain their file.
	if strings.Contains(git(t, root, "show", "--stat", "--pretty=", "HEAD"), "main.go") {
		t.Fatal("the notes commit swept up the user's staged file")
	}
	// And their work must still be staged, exactly as they left it.
	if !strings.Contains(git(t, root, "diff", "--cached", "--name-only"), "main.go") {
		t.Fatal("the user's staged change was lost")
	}
}

// Likewise, an unstaged change of the user's must be left in the working tree.
func TestCommitLeavesUnstagedWorkAlone(t *testing.T) {
	root, p := newRepo(t)

	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	writeEvent(t, p)
	r, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit("gnotes: sync"); err != nil {
		t.Fatal(err)
	}

	if strings.Contains(git(t, root, "show", "--stat", "--pretty=", "HEAD"), "README.md") {
		t.Fatal("the notes commit included the user's edit")
	}
	if !strings.Contains(git(t, root, "status", "--porcelain"), "README.md") {
		t.Fatal("the user's unstaged edit disappeared")
	}
}

// Dirty must only report on the gnotes paths, or a user with unrelated changes
// would see a notes sync claim there is work to do.
func TestDirtyIgnoresChangesOutsideTheProject(t *testing.T) {
	root, p := newRepo(t)
	r, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}

	// Get the project itself committed first, so anything still dirty
	// afterwards can only have come from outside it.
	writeEvent(t, p)
	if _, err := r.Commit("gnotes: baseline"); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(root, "unrelated.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	dirty, err := r.Dirty()
	if err != nil {
		t.Fatal(err)
	}
	if dirty {
		t.Fatal("an unrelated file made the project look dirty")
	}
}

func TestSyncWithoutRemoteCommitsLocally(t *testing.T) {
	_, p := newRepo(t)
	writeEvent(t, p)

	res, err := Sync(p, "gnotes: sync", false)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if !res.Committed {
		t.Fatal("Sync did not commit")
	}
	if res.Pulled || res.Pushed {
		t.Fatal("Sync contacted a remote when it was not asked to")
	}
	if res.Branch != "main" {
		t.Fatalf("Branch = %q", res.Branch)
	}
}

// A repository that has just been created has no commits, so HEAD names a
// branch that does not exist yet. The first sync must still work and still
// report which branch it landed on.
func TestSyncIntoARepositoryWithNoCommits(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}

	root := t.TempDir()
	git(t, root, "init", "--initial-branch=main")
	git(t, root, "config", "user.email", "test@example.com")
	git(t, root, "config", "user.name", "Test")
	git(t, root, "config", "commit.gpgsign", "false")

	p, err := store.Init(root, "demo", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	writeEvent(t, p)

	res, err := Sync(p, "gnotes: first", false)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if !res.Committed {
		t.Fatal("the first sync did not commit")
	}
	if res.Branch != "main" {
		t.Fatalf("Branch = %q, want main", res.Branch)
	}
}

func TestSyncReportsAMissingRemote(t *testing.T) {
	_, p := newRepo(t)
	writeEvent(t, p)

	if _, err := Sync(p, "gnotes: sync", true); !errors.Is(err, ErrNoRemote) {
		t.Fatalf("Sync = %v, want ErrNoRemote", err)
	}
}

// The end-to-end property: two clones, each with their own author file, merge
// without a conflict.
func TestTwoClonesMergeWithoutConflict(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}

	// A bare origin both clones share.
	origin := t.TempDir()
	git(t, origin, "init", "--bare", "--initial-branch=main")

	seed, seedProject := newRepo(t)
	git(t, seed, "remote", "add", "origin", origin)
	writeEvent(t, seedProject)

	// The first sync meets an empty remote: there is nothing to pull, only to
	// push.
	res, err := Sync(seedProject, "gnotes: seed", true)
	if err != nil {
		t.Fatalf("seed sync: %v", err)
	}
	if res.Pulled {
		t.Fatal("Sync pulled from a remote with no branch yet")
	}
	if !res.Pushed {
		t.Fatal("the first sync did not push")
	}

	// A second clone, writing as a different author.
	clone := t.TempDir()
	git(t, clone, "clone", origin, ".")
	git(t, clone, "config", "user.email", "other@example.com")
	git(t, clone, "config", "user.name", "Other")
	git(t, clone, "config", "commit.gpgsign", "false")

	cloneProject, err := store.Discover(clone)
	if err != nil {
		t.Fatalf("discover in clone: %v", err)
	}
	writeEvent(t, cloneProject)
	if _, err := Sync(cloneProject, "gnotes: from clone", true); err != nil {
		t.Fatalf("clone sync: %v", err)
	}

	// The seed pulls the clone's work back. Different author files means the
	// merge is a union of paths, not a textual conflict.
	writeEvent(t, seedProject)
	if _, err := Sync(seedProject, "gnotes: more from seed", true); err != nil {
		t.Fatalf("second seed sync: %v", err)
	}

	loaded, err := store.Load(seedProject)
	if err != nil {
		t.Fatalf("Load after merge: %v", err)
	}
	if len(loaded.Events) != 3 {
		t.Fatalf("merged log has %d events, want all 3", len(loaded.Events))
	}

	authors := map[string]bool{}
	for _, e := range loaded.Events {
		authors[e.UserID] = true
	}
	if len(authors) != 3 {
		t.Fatalf("got %d authors, want 3 distinct log files merged", len(authors))
	}
}
