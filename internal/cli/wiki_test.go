package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// wikiFixture is a directory with a wiki of five pages, driven through App.
func wikiFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, dir: t.TempDir(), now: time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)}
	if err := os.Mkdir(filepath.Join(f.dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if out := f.mustRun("init"); !strings.Contains(out, "0 pages") {
		t.Fatalf("init: %s", out)
	}
	pages := filepath.Join(f.dir, ".gwiki", "wiki")
	for name, body := range map[string]string{
		"index.md":               "# Home\n\nStart at [[Design sketch]] or [the grammar](lexer/grammar.md#rules).\n",
		"lexer/design-sketch.md": "---\ntitle: Design sketch\ntags: [design]\n---\n\nThe lexer tokenizes input. See [[Missing page]].\n\n- [ ] benchmark due:2026-08-21\n",
		"lexer/grammar.md":       "# Grammar\n\n## Rules\n\nNothing yet. [code](../../../main.go)\n",
		"tasks/ship.md":          "---\ntitle: Ship it\ntype: task\nstatus: doing\n---\n",
		"lexer/evil-title.md":    "# evil\x1b]0;PWNED\x07 title\n",
	} {
		path := filepath.Join(pages, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func TestWikiListShowAndSearch(t *testing.T) {
	f := wikiFixture(t)

	out := f.mustRun("ls", "lexer")
	for _, want := range []string{"lexer/design-sketch", "Design sketch", "#design", "lexer/grammar"} {
		if !strings.Contains(out, want) {
			t.Errorf("ls lexer is missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "index") || strings.Contains(out, "tasks/ship") {
		t.Errorf("ls lexer listed pages outside lexer:\n%s", out)
	}
	if out := f.mustRun("ls", "-t", "design"); strings.Count(out, "\n") != 1 {
		t.Errorf("ls -t design:\n%s", out)
	}

	out = f.mustRun("show", "design", "sketch")
	for _, want := range []string{"Design sketch  .gwiki/wiki/lexer/design-sketch.md", "tokenizes input", "[[Missing page]]", "missing-page", "backlinks", "index:3"} {
		if !strings.Contains(out, want) {
			t.Errorf("show is missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "title:") {
		t.Errorf("show printed the front matter:\n%s", out)
	}

	out = f.mustRun("search", "tokeniz")
	if !strings.Contains(out, "lexer/design-sketch  Design sketch") || !strings.Contains(out, "tokenizes") {
		t.Errorf("search:\n%s", out)
	}
	if out := f.mustRun("search", "no such words"); !strings.Contains(out, "nothing matches") {
		t.Errorf("empty search:\n%s", out)
	}
}

func TestWikiLinksCheckAndExitStatus(t *testing.T) {
	f := wikiFixture(t)

	out := f.mustRun("links", "index")
	if !strings.Contains(out, "lexer/grammar.md#rules") || strings.Contains(out, "missing") {
		t.Errorf("links index:\n%s", out)
	}
	if out := f.mustRun("backlinks", "lexer/grammar.md"); !strings.Contains(out, "index:3") {
		t.Errorf("backlinks:\n%s", out)
	}

	out, stderr, code := f.run("check")
	if code != 1 || !strings.Contains(stderr, "2 broken links") {
		t.Fatalf("check: exit %d, stderr %q", code, stderr)
	}
	for _, want := range []string{"lexer/design-sketch:6:", "missing-page", "lexer/grammar:5:", "missing-file"} {
		if !strings.Contains(out, want) {
			t.Errorf("check is missing %q:\n%s", want, out)
		}
	}

	if err := os.WriteFile(filepath.Join(f.dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.dir, ".gwiki", "wiki", "missing-page.md"), []byte("# Missing page\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := f.mustRun("check"); !strings.Contains(out, "no broken links") {
		t.Errorf("check after repair:\n%s", out)
	}
}

func TestWikiTasksOrphansAndCache(t *testing.T) {
	f := wikiFixture(t)

	out := strings.Join(strings.Fields(f.mustRun("tasks")), " ")
	// The item's date shows once, in its column.
	if !strings.Contains(out, "lexer/design-sketch:8 [ ] 2026-08-21 benchmark") || strings.Contains(out, "due:") || !strings.Contains(out, "tasks/ship [~] Ship it") {
		t.Errorf("tasks:\n%s", out)
	}
	if out := f.mustRun("tasks", "-s", "doing"); strings.Contains(out, "benchmark") || !strings.Contains(out, "Ship it") {
		t.Errorf("tasks -s doing:\n%s", out)
	}
	if _, _, code := f.run("tasks", "-s", "later"); code != 2 {
		t.Errorf("tasks -s later: exit %d, want 2", code)
	}

	out = f.mustRun("orphans")
	if !strings.Contains(out, "tasks/ship") || strings.Contains(out, "lexer/grammar") {
		t.Errorf("orphans:\n%s", out)
	}

	if out := f.mustRun("cache", "--rebuild"); !strings.Contains(out, "5 pages") {
		t.Errorf("cache --rebuild: %s", out)
	}
}

func TestWikiJSON(t *testing.T) {
	f := wikiFixture(t)
	for _, args := range [][]string{
		{"ls", "--json"},
		{"show", "grammar", "--json"},
		{"search", "lexer", "--json"},
		{"links", "index", "--json"},
		{"backlinks", "grammar", "--json"},
		{"orphans", "--json"},
		{"tasks", "--json"},
	} {
		out := f.mustRun(args...)
		if !json.Valid([]byte(out)) {
			t.Errorf("gwiki %s is not JSON:\n%s", strings.Join(args, " "), out)
		}
		if strings.ContainsAny(out, "\x02\x03") {
			t.Errorf("gwiki %s leaked snippet markers", strings.Join(args, " "))
		}
	}
	out, _, code := f.run("check", "--json")
	var broken []map[string]any
	if code != 1 || json.Unmarshal([]byte(out), &broken) != nil || len(broken) != 2 {
		t.Errorf("check --json: exit %d, %s", code, out)
	}
}

func TestWikiErrors(t *testing.T) {
	f := wikiFixture(t)
	if _, _, code := f.run("nosuch"); code != 2 {
		t.Errorf("unknown subcommand: exit %d, want 2", code)
	}
	if out := f.mustRun("help"); !strings.Contains(out, "backlinks") {
		t.Errorf("help does not list the wiki commands:\n%s", out)
	}
	if _, stderr, code := f.run("show", "sketch", "grammar"); code == 0 || !strings.Contains(stderr, "no page matches") {
		t.Errorf("show of nothing: exit %d, %s", code, stderr)
	}
	if _, stderr, _ := f.run("show", "lexer"); !strings.Contains(stderr, "could mean") {
		t.Errorf("ambiguous show: %s", stderr)
	}

	outside := &fixture{t: t, dir: t.TempDir(), now: f.now}
	if _, stderr, code := outside.run("ls"); code == 0 || !strings.Contains(stderr, "gwiki init") {
		t.Errorf("outside a wiki: exit %d, %s", code, stderr)
	}
}

// Page text is written by people and agents, so it is untrusted on a terminal.
func TestWikiDoesNotPrintControlCharacters(t *testing.T) {
	f := wikiFixture(t)
	for _, args := range [][]string{{"ls"}, {"show", "evil-title"}, {"orphans"}, {"search", "evil"}} {
		out := f.mustRun(args...)
		if bytes.ContainsAny([]byte(out), "\x1b\x07") {
			t.Errorf("gwiki %s printed a control character: %q", strings.Join(args, " "), out)
		}
	}
}

func TestWikiNewEditAndTag(t *testing.T) {
	f := wikiFixture(t)

	out := f.mustRun("new", "Parser notes", "--in", "lexer", "-t", "parser", "-m", "First line.")
	if !strings.Contains(out, ".gwiki/wiki/lexer/parser-notes.md  Parser notes") {
		t.Fatalf("new: %s", out)
	}
	if out := f.mustRun("new", "Ship the parser", "--task"); !strings.Contains(out, "ship-the-parser.md") {
		t.Fatalf("new --task: %s", out)
	}
	if out := strings.Join(strings.Fields(f.mustRun("tasks", "-s", "open")), " "); !strings.Contains(out, "ship-the-parser [ ] Ship the parser") {
		t.Fatalf("the new task page is not listed:\n%s", out)
	}

	f.mustRun("edit", "parser notes", "-m", "Replaced.")
	page := readPage(t, f, "lexer/parser-notes")
	if page != "---\ntags: [parser]\n---\n\nReplaced.\n" {
		t.Fatalf("edit -m:\n%s", page)
	}
	f.runIn("From stdin.\n", "edit", "parser notes", "--stdin")
	if page := readPage(t, f, "lexer/parser-notes"); !strings.HasSuffix(page, "\nFrom stdin.\n") {
		t.Fatalf("edit --stdin:\n%s", page)
	}

	// The editor gets the whole page and its changes are written back.
	script := filepath.Join(t.TempDir(), "editor.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf 'Appended in the editor.\\n' >> \"$1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VISUAL", "") // $VISUAL wins over $EDITOR
	t.Setenv("EDITOR", script)
	f.mustRun("edit", "parser notes")
	if page := readPage(t, f, "lexer/parser-notes"); !strings.HasSuffix(page, "From stdin.\nAppended in the editor.\n") || !strings.HasPrefix(page, "---\n") {
		t.Fatalf("edit in $EDITOR:\n%s", page)
	}

	if out := f.mustRun("tag", "lexer/parser-notes", "Lexer", "#extra"); !strings.Contains(out, "#extra #lexer #parser") {
		t.Fatalf("tag: %s", out)
	}
	// The page keeps the order tags were added in.
	if page := readPage(t, f, "lexer/parser-notes"); !strings.HasPrefix(page, "---\ntags: [parser, lexer, extra]\n---\n") {
		t.Fatalf("tag wrote:\n%s", page)
	}
	if out := f.mustRun("untag", "lexer/parser-notes", "parser"); !strings.Contains(out, "#extra #lexer") || strings.Contains(out, "#parser") {
		t.Fatalf("untag: %s", out)
	}
}

func readPage(t *testing.T, f *fixture, id string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.dir, ".gwiki", "wiki", filepath.FromSlash(id)+".md"))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestWikiMoveAndRemove(t *testing.T) {
	f := wikiFixture(t)

	out := f.mustRun("mv", "lexer/grammar", "archive/", "--dry-run")
	if !strings.Contains(out, "would move lexer/grammar to archive/grammar") || !strings.Contains(out, "index:3  lexer/grammar.md#rules  ->  archive/grammar.md#rules") {
		t.Fatalf("mv --dry-run:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(f.dir, ".gwiki", "wiki", "archive", "grammar.md")); !os.IsNotExist(err) {
		t.Fatal("--dry-run moved the page")
	}

	out = f.mustRun("mv", "lexer/grammar", "archive/")
	if !strings.Contains(out, "moved lexer/grammar to archive/grammar") || !strings.Contains(out, "index:3") {
		t.Fatalf("mv:\n%s", out)
	}
	if page := readPage(t, f, "index"); !strings.Contains(page, "[the grammar](archive/grammar.md#rules)") {
		t.Fatalf("index after mv:\n%s", page)
	}
	if out := f.mustRun("links", "index"); strings.Contains(out, "missing") {
		t.Fatalf("links after mv:\n%s", out)
	}

	_, stderr, code := f.run("rm", "archive/grammar")
	if code != 1 || !strings.Contains(stderr, "links reach") {
		t.Fatalf("rm of a linked page: exit %d, %s", code, stderr)
	}
	if out := f.mustRun("rm", "archive/grammar", "--force"); !strings.Contains(out, "removed .gwiki/wiki/archive/grammar.md") {
		t.Fatalf("rm --force: %s", out)
	}
}

func TestWikiTasksCommands(t *testing.T) {
	f := wikiFixture(t)

	if out := f.mustRun("done", "benchmark"); !strings.Contains(out, "lexer/design-sketch:8  done") {
		t.Fatalf("done: %s", out)
	}
	if page := readPage(t, f, "lexer/design-sketch"); !strings.Contains(page, "- [x] benchmark") {
		t.Fatalf("item not ticked:\n%s", page)
	}
	f.mustRun("reopen", "lexer/design-sketch:8")
	if _, stderr, code := f.run("doing", "benchmark"); code == 0 || !strings.Contains(stderr, "promote") {
		t.Fatalf("doing on an item: exit %d, %s", code, stderr)
	}
	f.mustRun("done", "Ship it")
	if page := readPage(t, f, "tasks/ship"); !strings.Contains(page, "status: done") {
		t.Fatalf("task page:\n%s", page)
	}

	if out := f.mustRun("promote", "benchmark"); !strings.Contains(out, "tasks/benchmark.md  benchmark  from lexer/design-sketch:8") {
		t.Fatalf("promote: %s", out)
	}
	if page := readPage(t, f, "lexer/design-sketch"); !strings.Contains(page, "- [[benchmark]]") {
		t.Fatalf("item not replaced:\n%s", page)
	}
}

func TestWikiCheckFix(t *testing.T) {
	f := wikiFixture(t)
	if err := os.MkdirAll(filepath.Join(f.dir, "cmd"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.dir, "cmd", "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.mustRun("new", "Missing pages")
	f.mustRun("new", "Missing paper")

	// --fix=first applies the only offer for the file; [[Missing page]] has
	// two similar pages, so it is left.
	out, _, code := f.runIn("", "check", "--fix=first")
	if code != 1 || !strings.Contains(out, "fixed  ../../../main.go -> ../../../cmd/main.go") || !strings.Contains(out, "[[Missing page]]") {
		t.Fatalf("check --fix=first: exit %d\n%s", code, out)
	}

	out, _, code = f.runIn("1\n", "check", "--fix")
	if code != 0 || !strings.Contains(out, "choice:") || !strings.Contains(out, "no broken links") {
		t.Fatalf("check --fix: exit %d\n%s", code, out)
	}
	if page := readPage(t, f, "lexer/design-sketch"); !strings.Contains(page, "|Missing page]]") {
		t.Fatalf("fixed link:\n%s", page)
	}
}

func TestWikiMCP(t *testing.T) {
	f := wikiFixture(t)
	frames := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}
{"jsonrpc":"2.0","method":"notifications/initialized"}
{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"gwiki_read","arguments":{"page":"grammar"}}}
`
	stdout, stderr, code := f.runIn(frames, "mcp")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 frames on stdout, got:\n%s", stdout)
	}
	var reply struct {
		Result struct {
			Content []struct{ Text string } `json:"content"`
			IsError bool                    `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Result.IsError || len(reply.Result.Content) != 1 || !strings.Contains(reply.Result.Content[0].Text, "source:\n# Grammar\n") {
		t.Fatalf("read reply: %s", lines[1])
	}
	if _, _, code := f.run("mcp", "extra"); code == 0 {
		t.Fatal("wiki mcp took an argument")
	}
	if _, _, code := f.run("ui", "extra"); code == 0 {
		t.Fatal("wiki ui took an argument")
	}
}

func TestWikiLSP(t *testing.T) {
	f := wikiFixture(t)
	frame := func(body string) string { return fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body), body) }
	// No rootUri: the server uses the directory gwiki runs in.
	stdin := frame(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"capabilities":{}}}`) +
		frame(`{"jsonrpc":"2.0","method":"initialized","params":{}}`) +
		frame(`{"jsonrpc":"2.0","id":2,"method":"workspace/symbol","params":{"query":"grammar"}}`) +
		frame(`{"jsonrpc":"2.0","id":3,"method":"shutdown"}`) +
		frame(`{"jsonrpc":"2.0","method":"exit"}`)
	stdout, stderr, code := f.runIn(stdin, "lsp")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if n := strings.Count(stdout, "Content-Length: "); n != 3 || !strings.HasPrefix(stdout, "Content-Length: ") {
		t.Fatalf("want 3 frames on stdout, got:\n%s", stdout)
	}
	if !strings.Contains(stdout, `"name":"gwiki"`) || !strings.Contains(stdout, `"name":"Grammar"`) {
		t.Fatalf("replies:\n%s", stdout)
	}
	if _, _, code := f.run("lsp", "extra"); code == 0 {
		t.Fatal("wiki lsp took an argument")
	}
}

func TestADirectoryFromBeforeTheRenameIsNamed(t *testing.T) {
	f := &fixture{t: t, dir: t.TempDir(), now: time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)}
	if err := os.MkdirAll(filepath.Join(f.dir, ".gnotes", "wiki"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code := f.run("ls"); code == 0 || !strings.Contains(stderr, "rename it to .gwiki") {
		t.Fatalf("exit %d, %s", code, stderr)
	}
}

func TestWikiServeFlags(t *testing.T) {
	f := wikiFixture(t)
	out := f.mustRun("help", "serve")
	for _, want := range []string{"--no-open", "access token", "overview", "SSH"} {
		if !strings.Contains(out, want) {
			t.Errorf("help serve lacks %q:\n%s", want, out)
		}
	}
	// Serving would block, so only the flags are exercised.
	if _, stderr, code := f.run("serve", "--nonsense"); code != 2 || !strings.Contains(stderr, "usage:") {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if _, stderr, code := f.run("serve", "--token", "short"); code == 0 || !strings.Contains(stderr, "16 characters") {
		t.Fatalf("a short token: exit %d, %s", code, stderr)
	}
}
