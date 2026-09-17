package mcp

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/shakfu/gwiki/internal/wiki"
)

// page writes a page directly, as an editor outside gwiki would.
func (f *fixture) page(id, src string) {
	f.t.Helper()
	file := filepath.Join(f.wiki.PagesPath(), filepath.FromSlash(id)+".md")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(src), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) source(id string) string {
	f.t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.wiki.PagesPath(), filepath.FromSlash(id)+".md"))
	if err != nil {
		f.t.Fatal(err)
	}
	return string(raw)
}

var hashLine = regexp.MustCompile(`(?m)^hash: ([0-9a-f]{64})$`)

func hashIn(t *testing.T, text string) string {
	t.Helper()
	m := hashLine.FindStringSubmatch(text)
	if m == nil {
		t.Fatalf("no hash in:\n%s", text)
	}
	return m[1]
}

func TestWikiAnnotationsMatchBehaviour(t *testing.T) {
	readOnly := map[string]bool{"gwiki_list": true, "gwiki_search": true, "gwiki_read": true, "gwiki_check": true, "gwiki_tasks": true}
	destructive := map[string]bool{"gwiki_edit": true, "gwiki_write": true}
	for _, entry := range wikiRegistry {
		a := entry.Annotations
		if a == nil {
			t.Errorf("%s has no annotations", entry.Name)
			continue
		}
		if a.ReadOnlyHint != readOnly[entry.Name] {
			t.Errorf("%s readOnlyHint = %v", entry.Name, a.ReadOnlyHint)
		}
		if !a.ReadOnlyHint && (a.DestructiveHint == nil || *a.DestructiveHint != destructive[entry.Name]) {
			t.Errorf("%s destructiveHint = %v, want %v", entry.Name, a.DestructiveHint, destructive[entry.Name])
		}
	}
}

func TestWikiInstructions(t *testing.T) {
	f := newFixture(t)
	replies := f.exchange(f.frame(1, "initialize", map[string]any{"protocolVersion": "2025-06-18"}))
	result := replies[0]["result"].(map[string]any)
	if got := result["instructions"].(string); !strings.Contains(got, ".gwiki/wiki") {
		t.Fatalf("instructions = %q", got)
	}
	if name := result["serverInfo"].(map[string]any)["name"]; name != "gwiki" {
		t.Fatalf("server name = %v", name)
	}
}

func TestWikiCreateReadEditAndWrite(t *testing.T) {
	f := newFixture(t)

	out := f.mustCall("gwiki_create", map[string]any{"title": "Parser notes", "dir": "lexer", "tags": []string{"parser"}, "body": "One.\n\nTwo."})
	if !strings.HasPrefix(out, "created lexer/parser-notes\n") {
		t.Fatalf("create: %s", out)
	}
	base := hashIn(t, out)

	read := f.mustCall("gwiki_read", map[string]any{"page": "parser notes"})
	if hashIn(t, read) != base || !strings.Contains(read, "source:\n---\ntags: [parser]\n---\n\n# Parser notes\n\nOne.\n\nTwo.\n\n(end of source)") {
		t.Fatalf("read:\n%s", read)
	}

	// Writes take the exact path, not a fragment.
	if text, isError := f.call("gwiki_edit", map[string]any{"page": "parser notes", "base": base, "old": "One.", "new": "1."}); !isError || !strings.Contains(text, "no page at") {
		t.Fatalf("edit by title = %v %s", isError, text)
	}

	out = f.mustCall("gwiki_edit", map[string]any{"page": "lexer/parser-notes", "base": base, "old": "One.", "new": "1."})
	next := hashIn(t, out)
	if got := f.source("lexer/parser-notes"); !strings.Contains(got, "\n1.\n\nTwo.\n") || wiki.Hash([]byte(got)) != next {
		t.Fatalf("after edit:\n%s", got)
	}

	// The developer saves in an editor; the agent's hash is stale.
	edited := strings.Replace(f.source("lexer/parser-notes"), "Two.", "Two, by hand.", 1)
	f.page("lexer/parser-notes", edited)
	text, isError := f.call("gwiki_edit", map[string]any{"page": "lexer/parser-notes", "base": next, "old": "1.", "new": "One again."})
	if !isError || !strings.Contains(text, "changed after it was read") || hashIn(t, text) != wiki.Hash([]byte(edited)) || !strings.Contains(text, "Two, by hand.") {
		t.Fatalf("stale edit = %v\n%s", isError, text)
	}
	if f.source("lexer/parser-notes") != edited {
		t.Fatal("a stale edit was written")
	}

	if text, isError := f.call("gwiki_write", map[string]any{"page": "lexer/parser-notes", "base": next, "content": "# Mine\n"}); !isError || !strings.Contains(text, "Two, by hand.") {
		t.Fatalf("stale write = %v\n%s", isError, text)
	}
	if text, isError := f.call("gwiki_write", map[string]any{"page": "lexer/parser-notes", "base": "", "content": "# Mine\n"}); !isError || !strings.Contains(text, "gwiki_create") {
		t.Fatalf("write without a base = %v\n%s", isError, text)
	}
	out = f.mustCall("gwiki_write", map[string]any{"page": "lexer/parser-notes", "base": wiki.Hash([]byte(edited)), "content": "# Mine\n"})
	if f.source("lexer/parser-notes") != "# Mine\n" || hashIn(t, out) != wiki.Hash([]byte("# Mine\n")) {
		t.Fatalf("write: %s", out)
	}

	// The cache saw the outside edit and the write.
	if out := f.mustCall("gwiki_list", nil); out != `lexer/parser-notes  "Mine"` {
		t.Fatalf("list: %q", out)
	}
}

func TestWikiSearchListAndRead(t *testing.T) {
	f := newFixture(t)
	f.page("index", "# Index\n\nSee [[Lexer]] and [code](../../main.go#L1) and [[Nowhere]].\n")
	f.page("lexer", "---\ntags: [code]\n---\n\n# Lexer\n\n## Tokens\n\nThe lexer tokenizes input.\n")
	if err := os.WriteFile(filepath.Join(filepath.Dir(filepath.Dir(f.wiki.PagesPath())), "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if out := f.mustCall("gwiki_search", map[string]any{"query": "tokeni"}); !strings.HasPrefix(out, `lexer  "Lexer"  #code`) || !strings.Contains(out, "\n  ") || strings.ContainsAny(out, "\x02\x03") {
		t.Fatalf("search: %q", out)
	}
	if out := f.mustCall("gwiki_search", map[string]any{"query": "zebra"}); out != "No pages match." {
		t.Fatalf("search with no match: %q", out)
	}
	if out := f.mustCall("gwiki_list", map[string]any{"tag": "code"}); out != `lexer  "Lexer"  #code` {
		t.Fatalf("list by tag: %q", out)
	}
	if out := f.mustCall("gwiki_list", map[string]any{"limit": 1}); !strings.Contains(out, "1 more pages not shown") {
		t.Fatalf("list limit: %q", out)
	}

	read := f.mustCall("gwiki_read", map[string]any{"page": "index"})
	for _, want := range []string{"index:3  [[Lexer]]  -> lexer", "index:3  ../../main.go#L1  -> main.go", "index:3  [[Nowhere]]  missing-page"} {
		if !strings.Contains(read, want) {
			t.Errorf("read index lacks %q:\n%s", want, read)
		}
	}
	read = f.mustCall("gwiki_read", map[string]any{"page": "lexer"})
	if !strings.Contains(read, "headings:\n  5  # Lexer  #lexer\n  7  ## Tokens  #tokens\n") || !strings.Contains(read, "backlinks:\n  index:3  [[Lexer]]  -> lexer\n") {
		t.Fatalf("read lexer:\n%s", read)
	}
}

func TestWikiRenameCheckAndFix(t *testing.T) {
	f := newFixture(t)
	f.page("index", "# Index\n\n- [[lexer/design-sketch]]\n- [sketch](lexer/design-sketch.md)\n- [[Missing pages]]\n")
	f.page("lexer/design-sketch", "# Design sketch\n")
	f.page("missing-page", "# Missing page\n")
	f.page("missing-paper", "# Missing paper\n")

	before := f.source("index")
	out := f.mustCall("gwiki_rename", map[string]any{"page": "lexer/design-sketch", "to": "archive/", "dry_run": true})
	if !strings.HasPrefix(out, "would move lexer/design-sketch to archive/design-sketch\n") || !strings.Contains(out, "index:4  lexer/design-sketch.md -> archive/design-sketch.md") {
		t.Fatalf("dry run:\n%s", out)
	}
	if f.source("index") != before {
		t.Fatal("a dry run wrote")
	}
	out = f.mustCall("gwiki_rename", map[string]any{"page": "lexer/design-sketch", "to": "archive/"})
	if !strings.HasPrefix(out, "moved ") || !strings.Contains(f.source("index"), "[sketch](archive/design-sketch.md)") {
		t.Fatalf("rename:\n%s\n%s", out, f.source("index"))
	}

	out = f.mustCall("gwiki_check", nil)
	if !strings.HasPrefix(out, "1 broken links.\n\nindex:5  [[Missing pages]]  missing-page\n") {
		t.Fatalf("check:\n%s", out)
	}
	if !strings.Contains(out, "  new: Missing page  (") || !strings.Contains(out, "  new: Missing paper  (") {
		t.Fatalf("check offers:\n%s", out)
	}

	args := map[string]any{"page": "index", "line": 5, "link": "[[Missing pages]]", "new": "Somewhere else"}
	if text, isError := f.call("gwiki_fix_link", args); !isError || !strings.Contains(text, "the offers are: Missing page, Missing paper") {
		t.Fatalf("fix with an unoffered destination = %v %s", isError, text)
	}
	args["line"] = 4
	if text, isError := f.call("gwiki_fix_link", args); !isError || !strings.Contains(text, "no broken link") {
		t.Fatalf("fix on the wrong line = %v %s", isError, text)
	}
	args["line"], args["new"] = 5, "Missing page"
	if out := f.mustCall("gwiki_fix_link", args); out != "fixed index:5  [[Missing pages]] -> Missing page" {
		t.Fatalf("fix: %q", out)
	}
	if !strings.Contains(f.source("index"), "- [[Missing page|Missing pages]]\n") {
		t.Fatalf("index after fix:\n%s", f.source("index"))
	}
	if out := f.mustCall("gwiki_check", map[string]any{"page": "index"}); out != "No broken links." {
		t.Fatalf("check after fix: %q", out)
	}
}

func TestWikiTasksAndSetTask(t *testing.T) {
	f := newFixture(t)
	f.page("plan", "# Plan\n\n- [ ] first due:2026-10-01\n- [x] second\n")
	f.page("tasks/ship", "---\ntitle: Ship\ntype: task\npriority: high\n---\n")

	if out := f.mustCall("gwiki_tasks", nil); out != "plan:3  [ ]  first  due:2026-10-01\nplan:4  [x]  second\ntasks/ship  [open]  Ship  priority:high" {
		t.Fatalf("tasks:\n%s", out)
	}
	if out := f.mustCall("gwiki_tasks", map[string]any{"status": "done"}); out != "plan:4  [x]  second" {
		t.Fatalf("tasks done: %q", out)
	}

	if text, isError := f.call("gwiki_set_task", map[string]any{"task": "plan:3", "status": "done"}); !isError || !strings.Contains(text, "item's text") {
		t.Fatalf("set_task without text = %v %s", isError, text)
	}
	// A line was inserted since the agent listed tasks.
	f.page("plan", "# Plan\n\n- [ ] zeroth\n- [ ] first\n- [x] second\n")
	if text, isError := f.call("gwiki_set_task", map[string]any{"task": "plan:3", "status": "done", "text": "first"}); !isError || !strings.Contains(text, `now holds "zeroth"`) {
		t.Fatalf("set_task on a moved line = %v %s", isError, text)
	}
	if out := f.mustCall("gwiki_set_task", map[string]any{"task": "plan:4", "status": "done", "text": "first"}); out != "plan:4  [x]  first" {
		t.Fatalf("set_task: %q", out)
	}
	if got := f.source("plan"); got != "# Plan\n\n- [ ] zeroth\n- [x] first\n- [x] second\n" {
		t.Fatalf("plan:\n%s", got)
	}

	if out := f.mustCall("gwiki_set_task", map[string]any{"task": "tasks/ship", "status": "doing"}); out != "tasks/ship  [doing]  Ship  priority:high" {
		t.Fatalf("set_task on a task page: %q", out)
	}
	if text, isError := f.call("gwiki_set_task", map[string]any{"task": "plan", "status": "done"}); !isError || !strings.Contains(text, "not a task page") {
		t.Fatalf("set_task on a plain page = %v %s", isError, text)
	}
}
