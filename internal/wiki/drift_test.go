package wiki

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitWiki is an empty wiki in a real git repository, and a function running git
// there.
func gitWiki(t *testing.T) (*Wiki, string, func(args ...string)) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	// The developer's own git configuration, such as commit signing, must not
	// reach the test.
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	w, root := emptyWiki(t)
	os.RemoveAll(filepath.Join(root, ".git"))
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root, "-c", "user.name=Ada", "-c", "user.email=ada@example.com"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	return w, root, git
}

// numbered is a source file of n distinct lines.
func numbered(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	return b.String()
}

func drifts(t *testing.T, w *Wiki) []string {
	t.Helper()
	if _, err := w.Refresh(); err != nil {
		t.Fatal(err)
	}
	ds, err := w.Drift()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, d := range ds {
		s := fmt.Sprintf("%s:%d %s %s", d.Page, d.Line, d.Status, d.Written())
		if d.Offer != nil {
			s += " -> " + d.Offer.New
		}
		out = append(out, s)
	}
	return out
}

func TestDriftMovedAndChangedLines(t *testing.T) {
	w, root, git := gitWiki(t)
	write(t, filepath.Join(root, "a.go"), numbered(20))
	put(t, w, root, map[string]string{"p": "# P\n\n" +
		"[same](/a.go#L3)\n" +
		"[one](/a.go#L5)\n" +
		"[range](/a.go#L7-L9)\n" +
		"[edited](/a.go#L12)\n" +
		"[far](/a.go#L99)\n" +
		"[gone](/nowhere.go#L1)\n"})
	git("add", ".")
	git("commit", "-q", "-m", "one")

	if got := drifts(t, w); len(got) != 0 {
		t.Fatalf("drift before any change: %v", got)
	}

	// Two lines inserted after line 3, and line 12 rewritten.
	lines := strings.SplitAfter(numbered(20), "\n")
	lines[11] = "line 12, rewritten\n"
	src := strings.Join(lines[:3], "") + "new\nnew\n" + strings.Join(lines[3:], "")
	write(t, filepath.Join(root, "a.go"), src)

	want := []string{
		"p:4 line-moved /a.go#L5 -> /a.go#L7",
		"p:5 line-moved /a.go#L7-L9 -> /a.go#L9-L11",
		"p:6 line-changed /a.go#L12",
	}
	if got := drifts(t, w); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("drift after an uncommitted edit:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	git("commit", "-q", "-am", "code")
	if got := drifts(t, w); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("drift after the edit is committed:\n%s", strings.Join(got, "\n"))
	}
}

// A commit that edits the page elsewhere, or an uncommitted fix to one of its
// links, must not hide the drift of the page's other links.
func TestDriftBaselineIsPerLink(t *testing.T) {
	w, root, git := gitWiki(t)
	write(t, filepath.Join(root, "a.go"), numbered(10))
	put(t, w, root, map[string]string{"p": "# P\n\n[x](/a.go#L2) and [y](/a.go#L4)\n"})
	git("add", ".")
	git("commit", "-q", "-m", "one")

	write(t, filepath.Join(root, "a.go"), "top\n"+numbered(10))
	git("commit", "-q", "-am", "code moves")
	put(t, w, root, map[string]string{"p": "# P, retitled\n\n[x](/a.go#L2) and [y](/a.go#L4)\n"})
	git("commit", "-q", "-am", "page typo")

	want := "p:3 line-moved /a.go#L2 -> /a.go#L3\np:3 line-moved /a.go#L4 -> /a.go#L5"
	if got := strings.Join(drifts(t, w), "\n"); got != want {
		t.Fatalf("after a page-only commit:\n%s\nwant:\n%s", got, want)
	}

	ds, err := w.Drift()
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Fix(ds[0].Link, *ds[0].Offer); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(drifts(t, w), "\n"); got != "p:3 line-moved /a.go#L4 -> /a.go#L5" {
		t.Fatalf("after fixing one link, uncommitted:\n%s", got)
	}
}

func TestDriftFollowsARenamedPage(t *testing.T) {
	w, root, git := gitWiki(t)
	write(t, filepath.Join(root, "a.go"), numbered(10))
	put(t, w, root, map[string]string{"old": "# Old\n\n[x](/a.go#L2)\n"})
	git("add", ".")
	git("commit", "-q", "-m", "one")
	write(t, filepath.Join(root, "a.go"), "top\n"+numbered(10))
	git("commit", "-q", "-am", "code moves")
	git("mv", ".gwiki/wiki/old.md", ".gwiki/wiki/new.md")
	git("commit", "-q", "-m", "rename")

	if got := strings.Join(drifts(t, w), "\n"); got != "new:3 line-moved /a.go#L2 -> /a.go#L3" {
		t.Fatalf("drift = %q", got)
	}
}

func TestDriftOfARepeatedBlockIsAChange(t *testing.T) {
	w, root, git := gitWiki(t)
	write(t, filepath.Join(root, "a.go"), "a\nb\nc\n")
	put(t, w, root, map[string]string{"p": "# P\n\n[b](/a.go#L2)\n"})
	git("add", ".")
	git("commit", "-q", "-m", "one")
	write(t, filepath.Join(root, "a.go"), "b\na\nb\n")

	if got := strings.Join(drifts(t, w), "\n"); got != "p:3 line-changed /a.go#L2" {
		t.Fatalf("drift = %q", got)
	}
}

// A link not yet committed was written against the working tree, so it is not
// compared with anything until it is committed.
func TestDriftSkipsAnUncommittedLink(t *testing.T) {
	w, root, git := gitWiki(t)
	write(t, filepath.Join(root, "a.go"), numbered(5))
	put(t, w, root, map[string]string{"p": "# P\n\n[x](/a.go#L2)\n"})
	git("add", ".")
	git("commit", "-q", "-m", "one")

	// The code moves, uncommitted, and a second link is written against it.
	write(t, filepath.Join(root, "a.go"), "top\n"+numbered(5))
	put(t, w, root, map[string]string{"p": "# P\n\n[x](/a.go#L2)\n\n[y](/a.go#L4)\n"})
	if got := strings.Join(drifts(t, w), "\n"); got != "p:3 line-moved /a.go#L2 -> /a.go#L3" {
		t.Fatalf("drift = %q", got)
	}
}

func TestDriftNeedsHistory(t *testing.T) {
	w, root := emptyWiki(t) // .git is an empty directory, not a repository
	if ds, err := w.Drift(); err != nil || len(ds) != 0 {
		t.Fatalf("with no line links: %v, %v", ds, err)
	}
	put(t, w, root, map[string]string{"p": "# P\n\n[x](/a.go#L2)\n"})
	if _, err := w.Drift(); !errors.Is(err, ErrNoHistory) {
		t.Fatalf("err = %v, want ErrNoHistory", err)
	}

	src, root, git := gitWiki(t)
	write(t, filepath.Join(root, "a.go"), numbered(5))
	put(t, src, root, map[string]string{"p": "# P\n\n[x](/a.go#L2)\n"})
	git("add", ".")
	git("commit", "-q", "-m", "one")
	git("commit", "-q", "--allow-empty", "-m", "two")
	clone := filepath.Join(t.TempDir(), "clone")
	git("clone", "-q", "--depth", "1", "file://"+root, clone)
	p, err := Discover(clone)
	if err != nil {
		t.Fatal(err)
	}
	shallow, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer shallow.Close()
	if _, err := shallow.Drift(); !errors.Is(err, ErrNoHistory) || !strings.Contains(err.Error(), "shallow") {
		t.Fatalf("err = %v, want ErrNoHistory for a shallow clone", err)
	}
}

func TestCountDest(t *testing.T) {
	for line, want := range map[string]int{
		"[a](/a.go#L3)":                   1,
		"[a](/a.go#L34) [b](/a.go#L3-L5)": 0,
		"[a](/x/a.go#L3)":                 0,
		"[a](/a.go#L3) and [b](/a.go#L3)": 2,
		"</a.go#L3>":                      1,
	} {
		if got := countDest(line, "/a.go#L3"); got != want {
			t.Errorf("countDest(%q) = %d, want %d", line, got, want)
		}
	}
}

// A fix can make a link's text equal to a committed link's. The page then holds
// more copies than HEAD, and which is uncommitted cannot be told apart.
func TestDriftSkipsTextWithUncommittedCopies(t *testing.T) {
	w, root, git := gitWiki(t)
	write(t, filepath.Join(root, "a.go"), numbered(5))
	put(t, w, root, map[string]string{"p": "# P\n\n[x](/a.go#L2) and [y](/a.go#L3)\n"})
	git("add", ".")
	git("commit", "-q", "-m", "one")
	write(t, filepath.Join(root, "a.go"), "top\n"+numbered(5))

	ds, err := w.Drift()
	if err != nil || len(ds) != 2 {
		t.Fatalf("drift = %v, %v", ds, err)
	}
	if err := w.Fix(ds[0].Link, *ds[0].Offer); err != nil {
		t.Fatal(err)
	}
	if got := drifts(t, w); len(got) != 0 {
		t.Fatalf("drift after the fix = %v", got)
	}
}

// A range past the end of a shorter file whose lines moved up is offered the
// new range instead of dropping the anchor.
func TestOutOfRangeLinesThatMovedAreOfferedTheirNewRange(t *testing.T) {
	w, root, git := gitWiki(t)
	write(t, filepath.Join(root, "a.go"), numbered(10))
	put(t, w, root, map[string]string{"p": "# P\n\n[moved](/a.go#L8-L9) and [gone](/a.go#L10)\n"})
	git("add", ".")
	git("commit", "-q", "-m", "one")
	write(t, filepath.Join(root, "a.go"), strings.Join(strings.SplitAfter(numbered(9), "\n")[3:], ""))

	broken, err := w.Check()
	if err != nil || len(broken) != 2 {
		t.Fatalf("broken = %+v, %v", broken, err)
	}
	all, err := w.OffersFor(broken)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for i, l := range broken {
		for _, o := range all[i] {
			got = append(got, l.Written()+" -> "+o.New+" ("+o.Label+")")
		}
	}
	want := "/a.go#L8-L9 -> /a.go#L5-L6 (the lines moved to L5-L6)\n/a.go#L10 -> /a.go (drop the line anchor)"
	if strings.Join(got, "\n") != want {
		t.Fatalf("offers:\n%s\nwant:\n%s", strings.Join(got, "\n"), want)
	}
	if ds, err := w.Drift(); err != nil || len(ds) != 0 {
		t.Fatalf("Drift lists a link Check reports: %+v, %v", ds, err)
	}
}
