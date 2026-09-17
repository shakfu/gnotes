package webwiki

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/shakfu/gwiki/internal/wiki"
)

// fixture is a server over a real wiki on disk, driven through httptest so the
// tests exercise routing, authorisation and encoding rather than the handlers
// alone.
type fixture struct {
	t    *testing.T
	srv  *Server
	http *httptest.Server
	root string
	w    *wiki.Wiki
}

var pages = map[string]string{
	"index":               "# Home\n\nSee [[Design sketch#Tokens]] and [code](../../src/lexer.go#L2-L3).\n\nBroken: [[Nowhere]].\n\n- [ ] write docs\n",
	"lexer/design-sketch": "---\ntitle: Design sketch\ntags: [design]\n---\n\n# Design sketch\n\n## Tokens\n\nThe lexer tokenizes input. Back to [[index]].\n",
	"orphan":              "# Orphan\n\nNothing links here, and it links nowhere.\n",
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	p, err := wiki.Init(root)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, root: root}
	for id, body := range pages {
		f.write(id, body)
	}
	f.writeFile(filepath.Join(root, "src", "lexer.go"), "package lexer\n\nfunc Lex() {}\nvar x = 1\n")

	if f.w, err = wiki.Open(p); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.w.Close() })

	// A short poll so the outside-change test does not wait a second.
	if f.srv, err = New(f.w, Options{Token: "test-token-value", PollInterval: 25 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	f.http = httptest.NewServer(f.srv)
	t.Cleanup(f.http.Close)
	go f.srv.watch(t.Context().Done())
	return f
}

func (f *fixture) write(page, body string) {
	f.t.Helper()
	f.writeFile(filepath.Join(f.root, ".gwiki", "wiki", filepath.FromSlash(page)+".md"), body)
}

func (f *fixture) writeFile(file, body string) {
	f.t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) source(page string) string {
	f.t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.root, ".gwiki", "wiki", filepath.FromSlash(page)+".md"))
	if err != nil {
		f.t.Fatal(err)
	}
	return string(raw)
}

// do sends a request with the token and returns the response.
func (f *fixture) do(method, path string, body any) *http.Response {
	f.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			f.t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, f.http.URL+path, reader)
	if err != nil {
		f.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer test-token-value")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := f.http.Client().Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	return res
}

// get decodes a successful GET.
func (f *fixture) get(path string, into any) {
	f.t.Helper()
	res := f.do("GET", path, nil)
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(res.Body)
		f.t.Fatalf("GET %s: %s %s", path, res.Status, raw)
	}
	if err := json.NewDecoder(res.Body).Decode(into); err != nil {
		f.t.Fatalf("GET %s: %v", path, err)
	}
}

func TestPageIsServedWithSecurityHeaders(t *testing.T) {
	f := newFixture(t)
	res, err := f.http.Client().Get(f.http.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK || !strings.Contains(string(raw), "<title>gwiki</title>") {
		t.Fatalf("GET /: %s\n%s", res.Status, raw)
	}
	for header, want := range map[string]string{
		"Content-Security-Policy": "default-src 'self'",
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "no-referrer",
	} {
		if got := res.Header.Get(header); !strings.Contains(got, want) {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
}

// The theme script is a file, since the policy forbids inline scripts, and the
// two dark rules, one for the system setting and one for the selector, agree.
func TestThemeAssets(t *testing.T) {
	f := newFixture(t)
	res, err := f.http.Client().Get(f.http.URL + "/theme.js")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || !strings.Contains(res.Header.Get("Content-Type"), "javascript") || !strings.Contains(string(raw), "gwiki-theme") {
		t.Fatalf("GET /theme.js: %s %s\n%s", res.Status, res.Header.Get("Content-Type"), raw)
	}

	page, _ := assets.ReadFile("assets/index.html")
	head, _, _ := strings.Cut(string(page), "</head>")
	if !strings.Contains(head, `<script src="theme.js"></script>`) || !strings.Contains(string(page), `<select id="theme"`) {
		t.Fatalf("index.html lacks the theme script in its head or the selector:\n%s", page)
	}

	css, _ := assets.ReadFile("assets/app.css")
	block := regexp.MustCompile(`(?s)(:root:not\(\[data-theme="light"\]\)|:root\[data-theme="dark"\]) \{(.*?)\}`)
	found := block.FindAllStringSubmatch(string(css), -1)
	if len(found) != 2 {
		t.Fatalf("found %d dark rules, want 2", len(found))
	}
	norm := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	if norm(found[0][2]) != norm(found[1][2]) {
		t.Fatalf("the dark rules differ:\n%s\n%s", found[0][2], found[1][2])
	}
}

func TestAuthorisation(t *testing.T) {
	f := newFixture(t)

	// No token.
	res, err := f.http.Client().Get(f.http.URL + "/api/pages")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("without a token: %s", res.Status)
	}

	// The wrong token.
	req, _ := http.NewRequest("GET", f.http.URL+"/api/pages", nil)
	req.Header.Set("Authorization", "Bearer nope")
	res, err = f.http.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("with a wrong token: %s", res.Status)
	}

	// Another origin, even with the token.
	req, _ = http.NewRequest("GET", f.http.URL+"/api/pages", nil)
	req.Header.Set("Authorization", "Bearer test-token-value")
	req.Header.Set("Origin", "http://evil.example")
	res, err = f.http.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("cross-origin: %s", res.Status)
	}

	// A Host that is not a loopback name, which is how DNS rebinding arrives.
	req, _ = http.NewRequest("GET", f.http.URL+"/", nil)
	req.Host = "wiki.evil.example"
	res, err = f.http.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusMisdirectedRequest {
		t.Errorf("rebinding Host: %s", res.Status)
	}
}

func TestOverviewAndPages(t *testing.T) {
	f := newFixture(t)
	var o struct {
		Name     string                  `json:"name"`
		Pages    int                     `json:"pages"`
		Broken   int                     `json:"broken"`
		Orphans  []struct{ Path string } `json:"orphans"`
		DeadEnds []struct{ Path string } `json:"deadEnds"`
		Recent   []struct {
			Path        string `json:"path"`
			Uncommitted bool   `json:"uncommitted"`
		} `json:"recent"`
		Tasks []struct{ Text string } `json:"tasks"`
		Tags  []struct {
			Name  string `json:"name"`
			Pages int    `json:"pages"`
		} `json:"tags"`
		Hubs []struct{ Name string } `json:"hubs"`
	}
	f.get("/api/overview", &o)
	if o.Pages != 3 || o.Broken != 1 || len(o.Recent) != 3 || len(o.Tasks) != 1 {
		t.Fatalf("overview = %+v", o)
	}
	if len(o.Orphans) != 1 || o.Orphans[0].Path != "orphan" || len(o.DeadEnds) != 1 {
		t.Errorf("orphans %+v, dead ends %+v", o.Orphans, o.DeadEnds)
	}
	if len(o.Tags) != 1 || o.Tags[0].Name != "design" {
		t.Errorf("tags = %+v", o.Tags)
	}

	var list struct {
		Name  string `json:"name"`
		Pages []struct{ Path, Title string }
	}
	f.get("/api/pages", &list)
	if len(list.Pages) != 3 || list.Pages[1].Title != "Design sketch" || list.Name == "" {
		t.Fatalf("pages = %+v", list)
	}
	f.get("/api/pages?tag=design", &list)
	if len(list.Pages) != 1 || list.Pages[0].Path != "lexer/design-sketch" {
		t.Fatalf("pages by tag = %+v", list)
	}
}

// pageResponse is what the browser reads for one page.
type pageResponse struct {
	Path      string   `json:"path"`
	Title     string   `json:"title"`
	Tags      []string `json:"tags"`
	Hash      string   `json:"hash"`
	HTML      string   `json:"html"`
	Source    string   `json:"source"`
	Broken    int      `json:"broken"`
	Links     []link   `json:"links"`
	Backlinks []link   `json:"backlinks"`
	Tasks     []struct {
		Line int    `json:"line"`
		Text string `json:"text"`
	} `json:"tasks"`
}

func TestPageRendersLinks(t *testing.T) {
	f := newFixture(t)
	var p pageResponse
	f.get("/api/page?p=index", &p)

	for _, want := range []string{
		`<a href="#/page/lexer%2Fdesign-sketch#tokens"`,
		`<a href="#/file/src%2Flexer.go#L2-L3"`,
		`class="broken"`,
		`<h1 id="home">Home</h1>`,
		`<input`,
	} {
		if !strings.Contains(p.HTML, want) {
			t.Errorf("html lacks %q:\n%s", want, p.HTML)
		}
	}
	if p.Broken != 1 || len(p.Tasks) != 1 || p.Source != pages["index"] || p.Hash == "" {
		t.Fatalf("page = %+v", p)
	}

	var sketch pageResponse
	f.get("/api/page?p=lexer/design-sketch", &sketch)
	if len(sketch.Backlinks) != 1 || sketch.Backlinks[0].Page != "index" || sketch.Tags[0] != "design" {
		t.Fatalf("backlinks = %+v", sketch.Backlinks)
	}

	// Raw HTML in a page is escaped, not passed through.
	f.write("evil", "# Evil\n\n<script>alert(1)</script>\n\n<img src=x onerror=alert(1)>\n")
	f.w.Refresh()
	var evil pageResponse
	f.get("/api/page?p=evil", &evil)
	if strings.Contains(evil.HTML, "<script>") || strings.Contains(evil.HTML, "onerror") {
		t.Fatalf("raw HTML reached the page:\n%s", evil.HTML)
	}

	res := f.do("GET", "/api/page?p=../../../etc/passwd", nil)
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest && res.StatusCode != http.StatusNotFound {
		t.Fatalf("a path outside the wiki: %s", res.Status)
	}
}

func TestSearchAndCheck(t *testing.T) {
	f := newFixture(t)
	var hits []struct {
		Path    string `json:"path"`
		Snippet string `json:"snippet"`
	}
	f.get("/api/search?q=tokeniz", &hits)
	if len(hits) != 1 || hits[0].Path != "lexer/design-sketch" || !strings.Contains(hits[0].Snippet, "\x02") {
		t.Fatalf("search = %+v", hits)
	}

	var broken []link
	f.get("/api/check", &broken)
	if len(broken) != 1 || broken[0].Written != "[[Nowhere]]" || broken[0].Status != "missing-page" {
		t.Fatalf("check = %+v", broken)
	}
}

func TestFileView(t *testing.T) {
	f := newFixture(t)
	var file struct{ Path, Text string }
	f.get("/api/file?p=src/lexer.go", &file)
	if !strings.Contains(file.Text, "func Lex()") {
		t.Fatalf("file = %+v", file)
	}
	for _, path := range []string{"../../../etc/passwd", "/etc/passwd"} {
		res := f.do("GET", "/api/file?p="+path, nil)
		res.Body.Close()
		if res.StatusCode == http.StatusOK {
			t.Errorf("%s was served", path)
		}
	}
}

func TestSaveNewAndTask(t *testing.T) {
	f := newFixture(t)
	var p pageResponse
	f.get("/api/page?p=index", &p)

	// A write with the hash the page was read at.
	var saved struct{ Hash string }
	res := f.do("POST", "/api/save", map[string]any{"page": "index", "base": p.Hash, "text": "# Home\n\nRewritten."})
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(res.Body)
		t.Fatalf("save: %s %s", res.Status, raw)
	}
	json.NewDecoder(res.Body).Decode(&saved)
	if f.source("index") != "# Home\n\nRewritten.\n" || saved.Hash == "" {
		t.Fatalf("after save: %q", f.source("index"))
	}

	// The same hash again is a conflict, and the current text comes back.
	res2 := f.do("POST", "/api/save", map[string]any{"page": "index", "base": p.Hash, "text": "# Home\n\nAgain."})
	defer res2.Body.Close()
	var conflict struct{ Error, Current, Hash string }
	json.NewDecoder(res2.Body).Decode(&conflict)
	if res2.StatusCode != http.StatusConflict || conflict.Current != "# Home\n\nRewritten.\n" {
		t.Fatalf("conflict: %s %+v", res2.Status, conflict)
	}
	if f.source("index") != "# Home\n\nRewritten.\n" {
		t.Fatal("the conflicting save was written")
	}

	// A new page, then a task on it.
	var made struct{ Path string }
	res3 := f.do("POST", "/api/new", map[string]any{"title": "Fresh page", "dir": "notes", "task": false})
	defer res3.Body.Close()
	json.NewDecoder(res3.Body).Decode(&made)
	if res3.StatusCode != http.StatusOK || made.Path != "notes/fresh-page" {
		t.Fatalf("new: %s %+v", res3.Status, made)
	}

	f.write("plan", "# Plan\n\n- [ ] first\n")
	f.w.Refresh()
	res4 := f.do("POST", "/api/task", map[string]any{"page": "plan", "line": 3, "text": "first", "status": "done"})
	res4.Body.Close()
	if res4.StatusCode != http.StatusOK || !strings.Contains(f.source("plan"), "- [x] first") {
		t.Fatalf("task: %s\n%s", res4.Status, f.source("plan"))
	}

	// A line that now holds another item is refused.
	res5 := f.do("POST", "/api/task", map[string]any{"page": "plan", "line": 3, "text": "second", "status": "open"})
	res5.Body.Close()
	if res5.StatusCode != http.StatusConflict {
		t.Fatalf("task with the wrong text: %s", res5.Status)
	}
}

func TestEventsFollowOutsideChanges(t *testing.T) {
	f := newFixture(t)
	req, _ := http.NewRequest("GET", f.http.URL+"/api/events?token=test-token-value", nil)
	res, err := f.http.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type = %q", ct)
	}
	reader := bufio.NewReader(res.Body)
	first, err := reader.ReadString('\n')
	if err != nil || !strings.HasPrefix(first, "data: ") {
		t.Fatalf("first message = %q, %v", first, err)
	}

	// A page written outside the browser wakes the stream.
	f.write("outside", "# Outside\n")
	done := make(chan string, 1)
	go func() {
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				done <- ""
				return
			}
			if strings.HasPrefix(line, "data: ") {
				done <- line
				return
			}
		}
	}()
	select {
	case line := <-done:
		if line == "" {
			t.Fatal("the stream closed before the change arrived")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no event for a page written outside")
	}

	// The stream needs the token too.
	res2, err := f.http.Client().Get(f.http.URL + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	res2.Body.Close()
	if res2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("events without a token: %s", res2.Status)
	}
}

// Every path the page fetches must exist on the server, or the browser meets
// a 404 that no Go test would otherwise catch.
func TestScriptCallsRealRoutes(t *testing.T) {
	f := newFixture(t)
	script, err := assets.ReadFile("assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	paths := regexp.MustCompile(`"(/api/[a-z]+)`).FindAllStringSubmatch(string(script), -1)
	if len(paths) < 6 {
		t.Fatalf("found %d API calls in the script", len(paths))
	}
	seen := map[string]bool{}
	for _, m := range paths {
		if seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		// A route the page posts to answers 404 to a GET, so both are tried.
		get := f.do("GET", m[1], nil)
		get.Body.Close()
		post := f.do("POST", m[1], map[string]any{})
		post.Body.Close()
		if get.StatusCode == http.StatusNotFound && post.StatusCode == http.StatusNotFound {
			t.Errorf("the page fetches %s, which the server does not serve", m[1])
		}
	}
	for _, want := range []string{"/api/overview", "/api/pages", "/api/page", "/api/search", "/api/check", "/api/save", "/api/task", "/api/new", "/api/events", "/api/file"} {
		if !seen[want] {
			t.Errorf("the page never fetches %s", want)
		}
	}
}

// runPageScript runs app.js under node against a stub browser, followed by
// tail, and returns what it printed. tail must print OK when its checks pass.
func runPageScript(t *testing.T, tail string) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	script, err := assets.ReadFile("assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	// The script runs against a stub browser; only pure helpers are called.
	harness := `
globalThis.location = { search: "", hash: "#/" };
const listeners = {};
const fakeNode = () => ({
  textContent: "", className: "", value: "", style: {},
  addEventListener() {}, append() {}, replaceChildren() {}, setAttribute() {},
  querySelector: () => null, querySelectorAll: () => [], focus() {},
});
globalThis.document = {
  getElementById: fakeNode, querySelector: () => null, querySelectorAll: () => [],
  createElement: fakeNode, addEventListener() {}, activeElement: { tagName: "BODY" },
  documentElement: { dataset: {} }, cookie: "",
};
globalThis.window = { addEventListener() {}, __lists: {} };
globalThis.fetch = async () => ({ ok: true, text: async () => "[]", statusText: "" });
globalThis.EventSource = function () { return { onmessage: null, onerror: null }; };
globalThis.CSS = { escape: (s) => s };
` + string(script) + tail
	file := filepath.Join(t.TempDir(), "harness.mjs")
	if err := os.WriteFile(file, []byte(harness), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, file).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("node: %v\n%s", err, out)
	}
}

// TestListContinuation runs the page's own Enter handling under node, so the
// browser and the terminal editor continue lists the same way.
func TestListContinuation(t *testing.T) {
	runPageScript(t, `
const cases = [
  ["- one", "- one\n- "],
  ["3. one", "3. one\n4. "],
  ["- [x] done", "- [x] done\n- [ ] "],
  ["> quoted", "> quoted\n> "],
  ["  - nested", "  - nested\n  - "],
  ["- ", ""],
  ["plain text", null],
];
let failed = 0;
for (const [line, want] of cases) {
  const area = {
    value: line, selectionStart: line.length, selectionEnd: line.length,
    setRangeText(text, from, to) { this.value = this.value.slice(0, from) + text + this.value.slice(to); },
    dispatchEvent() {},
  };
  let prevented = false;
  continueList({ preventDefault: () => { prevented = true; } }, area);
  if (want === null) {
    if (prevented) { console.log("FAIL", JSON.stringify(line), "was handled"); failed++; }
    continue;
  }
  if (area.value !== want) {
    console.log("FAIL", JSON.stringify(line), "->", JSON.stringify(area.value), "want", JSON.stringify(want));
    failed++;
  }
}
console.log(failed === 0 ? "OK" : "FAILED " + failed);
`)
}

// A task's text is shown with its links as the text they display, as the
// terminal interface shows it.
func TestTaskTextShowsLinksAsText(t *testing.T) {
	runPageScript(t, `
const cases = [
  ["write a README, as [[Writing pages#Sections]] explains", "write a README, as Writing pages#Sections explains"],
  ["see [[lexer/grammar|the grammar]] and [code](../x.go#L2)", "see the grammar and code"],
  ["![flow](img/flow.png) and [[a]], [[b|c]]", "flow and a, c"],
  ["no links here", "no links here"],
];
let failed = 0;
for (const [text, want] of cases) {
  const got = inlineText(text);
  if (got !== want) { console.log("FAIL", JSON.stringify(text), "->", JSON.stringify(got), "want", JSON.stringify(want)); failed++; }
}
console.log(failed === 0 ? "OK" : "FAILED " + failed);
`)
}

// A search snippet is page source; it is shown without markdown syntax, as the
// terminal interface shows it, keeping the match markers. <m> and </m> stand for
// the markers here.
func TestSearchSnippetDropsMarkdown(t *testing.T) {
	runPageScript(t, `
const S = String.fromCharCode(2), E = String.fromCharCode(3);
const mark = (s) => s.split("<m>").join(S).split("</m>").join(E);
const cases = [
  ["## Cache and refresh\n\n### Pages are the <m>truth</m>", "Cache and refresh Pages are the <m>truth</m>"],
  ["- [ ] benchmark the <m>lexer</m> due:2026-10-01\n- [x] done", "benchmark the <m>lexer</m> due:2026-10-01 done"],
  ["1. a **bold** <m>cache</m> and __under__ 2) b", "a bold <m>cache</m> and under b"],
  ["| status | means |\n|---|---|\n| `+"`ok`"+` | <m>resolves</m> |", "status means ok <m>resolves</m>"],
  ["See [[Design sketch#Tokens|the tokens]] and [code](../x.go#L2) ![img](a.png)", "See the tokens and code img"],
  ["a#b is not a heading; # is one; ---- rule", "a#b is not a heading; is one; rule"],
];
let failed = 0;
for (const [text, want] of cases) {
  const got = plainSnippet(mark(text));
  if (got !== mark(want)) { console.log("FAIL", JSON.stringify(text), "->", JSON.stringify(got)); failed++; }
}
console.log(failed === 0 ? "OK" : "FAILED " + failed);
`)
}
