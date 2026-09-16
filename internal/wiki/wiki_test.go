package wiki

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// touch moves a file's modification time forward, so a same-size rewrite is
// seen as a change on filesystems with coarse timestamps.
func touch(t *testing.T, path string) {
	t.Helper()
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
}

// fixture builds a repository with a wiki whose index page holds one link for
// every status and resolution rule.
func fixture(t *testing.T) (*Wiki, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "src", "lexer.go"), strings.Repeat("// line\n", 10))
	pages := filepath.Join(root, DirName, PagesDir)

	write(t, filepath.Join(pages, "index.md"), `# Top

- [[Design sketch]]
- [[lexer/grammar-ambiguity]]
- [[grammar ambiguity]]
- [[Nope]]
- [[Dup]]
- [[Design sketch#Tokens]]
- [[Design sketch#Nowhere]]
- [[#Top]]
- [src](../../src/lexer.go)
- [lines](../../src/lexer.go#L5-L8)
- [far](../../src/lexer.go#L50)
- [gone](../../src/missing.go)
- [out](../../../outside.txt)
- [sketch](lexer/design-sketch.md#tokens)
- [missing](lexer/nope.md)
- [web](https://example.com)
- [dir](../../src)
- [rooted](/src/lexer.go)
- [[Design Sketch Stem]]
`)
	write(t, filepath.Join(pages, "lexer", "design-sketch.md"), "---\ntitle: Design sketch\ntags: [Design, parser]\n---\n\n## Tokens\n\nBack to [[index]]. The lexer tokenizes input.\n")
	write(t, filepath.Join(pages, "lexer", "grammar-ambiguity.md"), "# Grammar ambiguity\n\n- [ ] resolve the ambiguity due:2026-08-21\n- [x] write it down\n")
	write(t, filepath.Join(pages, "a", "dup.md"), "# Dup\n")
	write(t, filepath.Join(pages, "b", "dup.md"), "# Dup\n")
	write(t, filepath.Join(pages, "design-sketch-stem.md"), "No title heading here.\n")
	write(t, filepath.Join(pages, "tasks", "ship.md"), "---\ntitle: Ship the parser\ntype: task\nstatus: doing\npriority: high\ndue: 2026-09-01\n---\n\nParser work.\n")
	write(t, filepath.Join(pages, "orphan.md"), "# Orphan\n\nNothing links here.\n")

	p, err := Discover(filepath.Join(pages, "lexer"))
	if err != nil {
		t.Fatal(err)
	}
	w, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	return w, root
}

func statuses(t *testing.T, w *Wiki, page string) []string {
	t.Helper()
	links, err := w.Links(page)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range links {
		out = append(out, fmt.Sprintf("%s %s %s %s", l.Written(), l.Kind, l.Status, l.Resolved))
	}
	return out
}

func TestEveryLinkStatus(t *testing.T) {
	w, _ := fixture(t)
	want := []string{
		"[[Design sketch]] page ok lexer/design-sketch",
		"[[lexer/grammar-ambiguity]] page ok lexer/grammar-ambiguity",
		"[[grammar ambiguity]] page ok lexer/grammar-ambiguity",
		"[[Nope]] page missing-page ",
		"[[Dup]] page ambiguous ",
		"[[Design sketch#Tokens]] heading ok lexer/design-sketch",
		"[[Design sketch#Nowhere]] heading missing-heading lexer/design-sketch",
		"[[#Top]] heading ok index",
		"../../src/lexer.go file ok src/lexer.go",
		"../../src/lexer.go#L5-L8 line ok src/lexer.go",
		"../../src/lexer.go#L50 line line-out-of-range src/lexer.go",
		"../../src/missing.go file missing-file src/missing.go",
		"../../../outside.txt file outside-repo ../../../outside.txt",
		"lexer/design-sketch.md#tokens heading ok lexer/design-sketch",
		"lexer/nope.md page missing-page lexer/nope",
		"https://example.com external ok https://example.com",
		"../../src file ok src",
		"/src/lexer.go file ok src/lexer.go",
		"[[Design Sketch Stem]] page ok design-sketch-stem",
	}
	got := statuses(t, w, "index")
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("links:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	broken, err := w.Check()
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, l := range broken {
		kinds = append(kinds, l.Status)
	}
	if strings.Join(kinds, ",") != "missing-page,ambiguous,missing-heading,line-out-of-range,missing-file,outside-repo,missing-page" {
		t.Fatalf("Check = %v", kinds)
	}
}

// Deleting, restoring and renaming pages re-resolves links on other pages.
func TestRefreshFollowsPagesComingAndGoing(t *testing.T) {
	w, root := fixture(t)
	pages := filepath.Join(root, DirName, PagesDir)
	sketch := filepath.Join(pages, "lexer", "design-sketch.md")
	raw, _ := os.ReadFile(sketch)

	if err := os.Remove(sketch); err != nil {
		t.Fatal(err)
	}
	ch, err := w.Refresh()
	if err != nil || strings.Join(ch.Removed, ",") != "lexer/design-sketch" {
		t.Fatalf("Refresh = %+v, %v", ch, err)
	}
	if got := statuses(t, w, "index")[0]; got != "[[Design sketch]] page missing-page " {
		t.Fatalf("after delete: %s", got)
	}

	write(t, filepath.Join(pages, "notes", "sketch.md"), string(raw))
	ch, err = w.Refresh()
	if err != nil || len(ch.Renamed) != 1 || ch.Renamed[0] != [2]string{"lexer/design-sketch", "notes/sketch"} {
		t.Fatalf("rename not detected: %+v, %v", ch, err)
	}
	// Found again by title; the markdown link by path stays broken.
	got := statuses(t, w, "index")
	if got[0] != "[[Design sketch]] page ok notes/sketch" || got[13] != "lexer/design-sketch.md#tokens heading missing-page lexer/design-sketch" {
		t.Fatalf("after rename:\n%s", strings.Join(got, "\n"))
	}
	if r, _ := w.Renames(); len(r) != 1 {
		t.Fatalf("Renames = %v", r)
	}
}

func TestRefreshSeesEditsAndIgnoresNothingChanged(t *testing.T) {
	w, root := fixture(t)
	if ch, err := w.Refresh(); err != nil || !ch.Empty() {
		t.Fatalf("unchanged refresh = %+v, %v", ch, err)
	}
	orphan := filepath.Join(root, DirName, PagesDir, "orphan.md")
	write(t, orphan, "# Orphan\n\nNow links [[Nope]].\n")
	touch(t, orphan)
	ch, err := w.Refresh()
	if err != nil || strings.Join(ch.Modified, ",") != "orphan" {
		t.Fatalf("Refresh = %+v, %v", ch, err)
	}
	if got := statuses(t, w, "orphan"); len(got) != 1 || got[0] != "[[Nope]] page missing-page " {
		t.Fatalf("orphan links = %v", got)
	}

	// A page created with the missing title repairs the link from elsewhere.
	write(t, filepath.Join(root, DirName, PagesDir, "nope.md"), "# Nope\n")
	if _, err := w.Refresh(); err != nil {
		t.Fatal(err)
	}
	if got := statuses(t, w, "orphan")[0]; got != "[[Nope]] page ok nope" {
		t.Fatalf("after creating the page: %s", got)
	}
}

func TestCheckNoticesFilesOutsideTheWiki(t *testing.T) {
	w, root := fixture(t)
	if err := os.WriteFile(filepath.Join(root, "src", "lexer.go"), []byte("short\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "missing.go"), []byte("now here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Check(); err != nil {
		t.Fatal(err)
	}
	got := statuses(t, w, "index")
	if !strings.HasPrefix(got[9], "../../src/lexer.go#L5-L8 line line-out-of-range") || !strings.HasPrefix(got[11], "../../src/missing.go file ok") {
		t.Fatalf("after changing files:\n%s", strings.Join(got, "\n"))
	}
}

func TestACacheFromAnotherSchemaOrCorruptIsRebuilt(t *testing.T) {
	w, _ := fixture(t)
	if _, err := w.db.Exec(`PRAGMA user_version = 99`); err != nil {
		t.Fatal(err)
	}
	w.Close()
	w2, err := Open(w.Project)
	if err != nil {
		t.Fatalf("Open with a stale schema: %v", err)
	}
	if pages, _ := w2.Pages("", ""); len(pages) != 8 {
		t.Fatalf("rebuilt cache has %d pages", len(pages))
	}
	w2.Close()

	for _, suffix := range []string{"-wal", "-shm"} {
		os.Remove(w.Cache() + suffix)
	}
	if err := os.WriteFile(w.Cache(), []byte("not a database at all, just text that is long enough"), 0o644); err != nil {
		t.Fatal(err)
	}
	w3, err := Open(w.Project)
	if err != nil {
		t.Fatalf("Open with a corrupt cache: %v", err)
	}
	defer w3.Close()
	if pages, _ := w3.Pages("", ""); len(pages) != 8 {
		t.Fatalf("rebuilt cache has %d pages", len(pages))
	}
}

func TestSearch(t *testing.T) {
	w, _ := fixture(t)
	hits, err := w.Search("design sketch", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].Path != "lexer/design-sketch" {
		t.Fatalf("hits = %+v, want the titled page first", hits)
	}
	hits, _ = w.Search("tokeniz", 0)
	if len(hits) != 1 || !strings.Contains(hits[0].Snippet, "\x02tokenizes\x03") {
		t.Fatalf("prefix search = %+v", hits)
	}
	if hits, _ := w.Search(`"parser" OR NEAR(`, 0); len(hits) != 0 {
		t.Fatalf("query syntax was read as operators: %+v", hits)
	}
	if hits, _ := w.Search("parser", 1); len(hits) != 1 {
		t.Fatalf("limit ignored: %d hits", len(hits))
	}
}

func TestTasksBacklinksOrphansAndFind(t *testing.T) {
	w, _ := fixture(t)

	tasks, err := w.Tasks(TaskFilter{})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, tk := range tasks {
		got = append(got, fmt.Sprintf("%s:%d %s %s %s", tk.Page, tk.Line, tk.Status, tk.Due, tk.Text))
	}
	want := "lexer/grammar-ambiguity:3 open 2026-08-21 resolve the ambiguity|" +
		"lexer/grammar-ambiguity:4 done  write it down|tasks/ship:0 doing 2026-09-01 Ship the parser"
	if strings.Join(got, "|") != want {
		t.Fatalf("tasks = %q", strings.Join(got, "|"))
	}
	if open, _ := w.Tasks(TaskFilter{Status: "open"}); len(open) != 1 {
		t.Fatalf("open tasks = %+v", open)
	}

	back, err := w.Backlinks("lexer/design-sketch")
	if err != nil || len(back) != 4 {
		t.Fatalf("backlinks = %+v, %v", back, err)
	}

	orphans, _ := w.Orphans()
	var names []string
	for _, o := range orphans {
		names = append(names, o.Path)
	}
	if strings.Join(names, ",") != "a/dup,b/dup,orphan,tasks/ship" {
		t.Fatalf("orphans = %v", names)
	}

	for ref, want := range map[string]string{
		"lexer/design-sketch.md": "lexer/design-sketch",
		"DESIGN SKETCH":          "lexer/design-sketch",
		"design-sketch":          "lexer/design-sketch", // a file name beats the fragment it also is of design-sketch-stem
		"ship":                   "tasks/ship",
		"design sketch stem":     "design-sketch-stem", // spaces read as hyphens in a file name
	} {
		if p, err := w.Find(ref); err != nil || p.Path != want {
			t.Errorf("Find(%q) = %+v, %v", ref, p, err)
		}
	}
	var amb *ErrAmbiguous
	if _, err := w.Find("dup"); !errors.As(err, &amb) || len(amb.Candidates) != 2 {
		t.Fatalf("Find(dup) = %v", err)
	}

	page, err := w.Page("lexer/design-sketch")
	if err != nil || page.Title != "Design sketch" || strings.Join(page.Tags, ",") != "design,parser" ||
		len(page.Headings) != 1 || !strings.Contains(page.Body, "tokenizes") || strings.Contains(page.Body, "title:") {
		t.Fatalf("Page = %+v, %v", page, err)
	}
}

func TestInitKeepsExistingFiles(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, DirName, ".gitignore"), "notes.db-journal")
	write(t, filepath.Join(root, DirName, configFile), `{"name": "kept"}`)
	p, err := Init(root)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(root, DirName, ".gitignore"))
	if string(raw) != "notes.db-journal\ncache.db*\ndrafts/\n" || p.Config.Name != "kept" {
		t.Fatalf(".gitignore = %q, name = %q", raw, p.Config.Name)
	}
	if _, err := Init(root); err != nil {
		t.Fatal(err)
	}
	if again, _ := os.ReadFile(filepath.Join(root, DirName, ".gitignore")); string(again) != string(raw) {
		t.Fatalf("a second Init changed .gitignore: %q", again)
	}
	if _, err := Discover(t.TempDir()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Discover outside a wiki = %v", err)
	}
}

// Changing a page's title or headings re-resolves links from other pages that
// name it.
func TestTitleAndHeadingChangesReachLinksElsewhere(t *testing.T) {
	w, root := fixture(t)
	pages := filepath.Join(root, DirName, PagesDir)
	named := filepath.Join(pages, "notes", "n1.md")
	write(t, named, "# Old name\n")
	write(t, filepath.Join(pages, "orphan.md"), "# Orphan\n\nSee [[Old name]] and [[New name]].\n")
	if _, err := w.Refresh(); err != nil {
		t.Fatal(err)
	}
	if got := statuses(t, w, "orphan"); got[0] != "[[Old name]] page ok notes/n1" || got[1] != "[[New name]] page missing-page " {
		t.Fatalf("before: %v", got)
	}

	write(t, named, "# New name\n")
	touch(t, named)
	sketch := filepath.Join(pages, "lexer", "design-sketch.md")
	write(t, sketch, "---\ntitle: Lexer design\n---\n\n## Scanning\n\nBack to [[index]].\n")
	touch(t, sketch)
	if _, err := w.Refresh(); err != nil {
		t.Fatal(err)
	}

	if got := statuses(t, w, "orphan"); got[0] != "[[Old name]] page missing-page " || got[1] != "[[New name]] page ok notes/n1" {
		t.Fatalf("after the title change: %v", got)
	}
	index := statuses(t, w, "index")
	for i, want := range map[int]string{
		0:  "[[Design sketch]] page ok lexer/design-sketch", // by file name, whatever the title
		5:  "[[Design sketch#Tokens]] heading missing-heading lexer/design-sketch",
		13: "lexer/design-sketch.md#tokens heading missing-heading lexer/design-sketch",
	} {
		if index[i] != want {
			t.Errorf("index link %d = %q, want %q", i, index[i], want)
		}
	}
}

// Processes that notice the same change at once, such as the terminal
// interface and a command, must not index it twice.
func TestConcurrentRefreshesOfTheSameChange(t *testing.T) {
	w, root := fixture(t)
	const n = 6
	var others []*Wiki
	for i := 0; i < n; i++ {
		o, err := Open(w.Project)
		if err != nil {
			t.Fatal(err)
		}
		defer o.Close()
		others = append(others, o)
	}
	for i := 0; i < 20; i++ {
		write(t, filepath.Join(root, DirName, PagesDir, "new", fmt.Sprintf("page-%d.md", i)), fmt.Sprintf("# New %d\n\n[[Design sketch]]\n", i))
	}
	errs := make(chan error, n)
	for _, o := range others {
		go func() {
			_, err := o.Refresh()
			errs <- err
		}()
	}
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Errorf("concurrent Refresh: %v", err)
		}
	}
	if pages, _ := w.Pages("new", ""); len(pages) != 20 {
		t.Fatalf("%d pages indexed, want 20", len(pages))
	}
	if back, _ := w.Backlinks("lexer/design-sketch"); len(back) != 24 {
		t.Fatalf("%d backlinks, want 24: a page was indexed twice or not at all", len(back))
	}
}

// A snapshot resolves a page's saved source exactly as the cache did.
func TestSnapshotMatchesTheCache(t *testing.T) {
	w, root := fixture(t)
	s, err := w.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range s.Pages() {
		src, _, err := w.Read(p.Path)
		if err != nil {
			t.Fatal(err)
		}
		cached, err := w.Links(p.Path)
		if err != nil {
			t.Fatal(err)
		}
		got := s.Links(p.Path, src)
		if fmt.Sprintf("%+v", got) != fmt.Sprintf("%+v", cached) && !(len(got) == 0 && len(cached) == 0) {
			t.Errorf("%s:\nsnapshot %+v\ncache    %+v", p.Path, got, cached)
		}
	}

	// An unsaved buffer: a heading added, a link to it, and a link broken.
	buf := []byte("# Top\n\n## Added\n\n[[#Added]] [[#Top]] [[Orphn]]\n")
	links := s.Links("index", buf)
	var got []string
	for _, l := range links {
		got = append(got, l.Written()+" "+l.Status)
	}
	if strings.Join(got, ", ") != "[[#Added]] ok, [[#Top]] ok, [[Orphn]] missing-page" {
		t.Fatalf("buffer links = %v", got)
	}
	// The snapshot's own view of index is unchanged afterwards.
	if again := s.Links("lexer/design-sketch", []byte("[[index#Added]]\n")); again[0].Status != StatusMissingHeading {
		t.Fatalf("a buffer's heading leaked into the index: %+v", again[0])
	}

	offers, err := s.Offers(links[2], buf)
	if err != nil || len(offers) == 0 || offers[0].New != "Orphan" {
		t.Fatalf("offers for [[Orphn]] = %+v, %v", offers, err)
	}
	if got := FixText(links[2], buf, offers[0]); got != "Orphan|Orphn" {
		t.Fatalf("FixText = %q", got)
	}

	if id, ok := w.PageOf(filepath.Join(root, DirName, PagesDir, "lexer", "grammar-ambiguity.md")); !ok || id != "lexer/grammar-ambiguity" {
		t.Fatalf("PageOf = %q, %v", id, ok)
	}
	for _, file := range []string{filepath.Join(root, "README.md"), filepath.Join(root, DirName, PagesDir, ".hidden", "x.md"), filepath.Join(root, DirName, PagesDir, "x.txt")} {
		if id, ok := w.PageOf(file); ok {
			t.Errorf("PageOf(%s) = %q", file, id)
		}
	}
}

func TestOverviewQueries(t *testing.T) {
	w, root := fixture(t)
	write(t, filepath.Join(root, DirName, PagesDir, "fan.md"), "# Fan\n\n[[Design sketch]] and [[Design sketch#Tokens]]\n")
	if _, err := w.Refresh(); err != nil {
		t.Fatal(err)
	}

	// Two pages link to the sketch; one link each to the rest, by name.
	hubs, err := w.Hubs(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(hubs) != 2 || hubs[0] != (Count{"lexer/design-sketch", 2}) || hubs[1] != (Count{"design-sketch-stem", 1}) {
		t.Fatalf("hubs = %+v", hubs)
	}
	if n, err := w.BrokenCount(); err != nil || n != 7 {
		broken, _ := w.Broken()
		t.Fatalf("BrokenCount = %d, %v; Broken has %d", n, err, len(broken))
	}
	tags, err := w.TagCounts()
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 2 || tags[0] != (Count{"design", 1}) || tags[1] != (Count{"parser", 1}) {
		t.Fatalf("tags = %+v", tags)
	}
	ends, err := w.DeadEnds()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range ends {
		names = append(names, p.Path)
	}
	if strings.Join(names, ",") != "a/dup,b/dup,design-sketch-stem,lexer/grammar-ambiguity,orphan,tasks/ship" {
		t.Fatalf("dead ends = %v", names)
	}

	// Outside git: modification order, no commit information.
	sketch := filepath.Join(root, DirName, PagesDir, "lexer", "design-sketch.md")
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(sketch, later, later); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Refresh(); err != nil {
		t.Fatal(err)
	}
	recent, err := w.Recent(3)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 3 || recent[0].Path != "lexer/design-sketch" || !recent[0].Modified.Equal(later) {
		t.Fatalf("recent = %+v", recent)
	}
}

func TestRecentReadsGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
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
	put(t, w, root, map[string]string{"committed": "# Committed\n", "edited": "# Edited\n"})
	git("add", ".gwiki/wiki")
	git("commit", "-q", "-m", "pages")
	put(t, w, root, map[string]string{"edited": "# Edited again\n", "new": "# New\n"})

	recent, err := w.Recent(10)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]Change{}
	for _, c := range recent {
		by[c.Path] = c
	}
	if c := by["committed"]; c.Author != "Ada" || c.Committed.IsZero() || c.Uncommitted {
		t.Errorf("committed = %+v", c)
	}
	if c := by["edited"]; c.Author != "Ada" || !c.Uncommitted {
		t.Errorf("edited = %+v", c)
	}
	if c := by["new"]; c.Author != "" || !c.Uncommitted {
		t.Errorf("new = %+v", c)
	}
}
