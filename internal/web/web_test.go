package web

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shakfu/gnotes/internal/session"
	"github.com/shakfu/gnotes/internal/state"
	"github.com/shakfu/gnotes/internal/store"
	"github.com/shakfu/gnotes/internal/ulid"
)

var clock = func() time.Time { return time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC) }

// fixture is a server over a real project on disk, driven through httptest so
// the tests exercise routing, authorisation and encoding rather than calling
// handlers directly.
type fixture struct {
	t    *testing.T
	srv  *Server
	http *httptest.Server
	sess *session.Session
	work string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	p, err := store.Init(t.TempDir(), "demo", clock())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := session.OpenProject(p, store.Actor{ID: ulid.NewGenerator().New(), Name: "sa"})
	if err != nil {
		t.Fatal(err)
	}
	sess.SetClock(clock)
	if err := sess.Init("demo"); err != nil {
		t.Fatal(err)
	}

	// A short poll so the outside-change test does not have to wait a second.
	srv, err := New(sess, Options{Token: "test-token", PollInterval: 25 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	f := &fixture{t: t, srv: srv, http: ts, sess: sess}

	// The watcher only runs under Run, which owns a listener; the tests drive
	// the check directly instead.
	work := f.post("/api/notebook", map[string]any{"title": "work"})
	f.work = work["id"].(string)

	return f
}

// do makes a request carrying the token.
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
	req.Header.Set("Authorization", "Bearer test-token")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	res, err := f.http.Client().Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	return res
}

// decodeInto reads a successful response body.
func (f *fixture) decodeInto(res *http.Response, into any) {
	f.t.Helper()
	defer res.Body.Close()

	if res.StatusCode >= 400 {
		body, _ := io.ReadAll(res.Body)
		f.t.Fatalf("%s: %s", res.Status, body)
	}
	if err := json.NewDecoder(res.Body).Decode(into); err != nil {
		f.t.Fatalf("decode: %v", err)
	}
}

func (f *fixture) get(path string) map[string]any {
	f.t.Helper()
	var out map[string]any
	f.decodeInto(f.do("GET", path, nil), &out)
	return out
}

func (f *fixture) post(path string, body any) map[string]any {
	f.t.Helper()
	var out map[string]any
	f.decodeInto(f.do("POST", path, body), &out)
	return out
}

func (f *fixture) patch(path string, body any) map[string]any {
	f.t.Helper()
	var out map[string]any
	f.decodeInto(f.do("PATCH", path, body), &out)
	return out
}

// newTask creates a task and returns its id.
func (f *fixture) newTask(title string) string {
	f.t.Helper()
	got := f.post("/api/node", map[string]any{"kind": "task", "title": title, "notebook": f.work})
	return got["id"].(string)
}

// newNote creates a note and returns its id.
func (f *fixture) newNote(title, body string) string {
	f.t.Helper()
	got := f.post("/api/node", map[string]any{"kind": "note", "title": title, "body": body, "notebook": f.work})
	return got["id"].(string)
}

// entries pulls the titles out of a state response.
func entries(state map[string]any) []string {
	var out []string
	for _, e := range state["entries"].([]any) {
		out = append(out, e.(map[string]any)["title"].(string))
	}
	return out
}

// ---------------------------------------------------------------- access

// The token, not the loopback binding, is what protects the project: any page
// the user has open can reach 127.0.0.1.
func TestAPIRequiresTheToken(t *testing.T) {
	f := newFixture(t)

	cases := map[string]func(*http.Request){
		"no token":    func(r *http.Request) {},
		"wrong token": func(r *http.Request) { r.Header.Set("Authorization", "Bearer nope") },
		"empty token": func(r *http.Request) { r.Header.Set("Authorization", "Bearer ") },
		"bad scheme":  func(r *http.Request) { r.Header.Set("Authorization", "test-token") },
	}

	for name, prepare := range cases {
		t.Run(name, func(t *testing.T) {
			req, _ := http.NewRequest("GET", f.http.URL+"/api/state", nil)
			prepare(req)

			res, err := f.http.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()

			if res.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", res.StatusCode)
			}
		})
	}
}

func TestTokenIsAcceptedInAQueryParameter(t *testing.T) {
	f := newFixture(t)

	// The event stream cannot set headers, so the query form has to work.
	res, err := f.http.Client().Get(f.http.URL + "/api/state?token=test-token")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
}

// A page on another origin must not be able to drive this server, even if it
// somehow learned the token.
func TestCrossOriginRequestsAreRejected(t *testing.T) {
	f := newFixture(t)

	req, _ := http.NewRequest("POST", f.http.URL+"/api/node", strings.NewReader(`{"title":"x"}`))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://evil.example")

	res, err := f.http.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", res.StatusCode)
	}
}

// A request from the page itself carries its own origin and must be allowed.
func TestSameOriginRequestsAreAllowed(t *testing.T) {
	f := newFixture(t)

	req, _ := http.NewRequest("GET", f.http.URL+"/api/state", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Origin", f.http.URL)

	res, err := f.http.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
}

func TestGeneratedTokensDiffer(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		tok, err := newToken()
		if err != nil {
			t.Fatal(err)
		}
		if seen[tok] {
			t.Fatal("newToken repeated itself")
		}
		if len(tok) < 24 {
			t.Fatalf("token %q is too short to be unguessable", tok)
		}
		seen[tok] = true
	}
}

// ---------------------------------------------------------------- assets

// The page is compiled in, so it must work with nothing to fetch and nothing
// installed.
func TestAssetsAreServedAndSelfContained(t *testing.T) {
	f := newFixture(t)

	for _, path := range []string{"/", "/app.css", "/app.js"} {
		res, err := f.http.Client().Get(f.http.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()

		if res.StatusCode != http.StatusOK {
			t.Fatalf("%s: status %d", path, res.StatusCode)
		}
		if len(body) == 0 {
			t.Fatalf("%s is empty", path)
		}
		// Anything fetched from elsewhere would break the offline promise.
		for _, external := range []string{"http://", "https://", "//cdn", "//unpkg", "//fonts."} {
			if bytes.Contains(body, []byte(external)) {
				t.Errorf("%s references something external: %s", path, external)
			}
		}
	}
}

// ---------------------------------------------------------------- reads

func TestStateReturnsEverythingThePageNeeds(t *testing.T) {
	f := newFixture(t)
	f.newNote("design sketch", "The lexer tokenizes input.")
	f.newTask("fix the lexer")

	got := f.get("/api/state")

	if got["project"] != "demo" {
		t.Errorf("project = %v", got["project"])
	}
	if len(got["notebooks"].([]any)) != 1 {
		t.Errorf("notebooks = %v", got["notebooks"])
	}
	if len(entries(got)) != 2 {
		t.Errorf("entries = %v", entries(got))
	}
	for _, key := range []string{"counts", "tags", "version", "me", "location"} {
		if _, ok := got[key]; !ok {
			t.Errorf("state is missing %q", key)
		}
	}
}

// The page iterates these without guards, so they must be arrays rather than
// null even when nothing is there.
func TestEmptyCollectionsAreArraysNotNull(t *testing.T) {
	f := newFixture(t)
	id := f.newNote("bare", "")

	got := f.get("/api/state")
	for _, key := range []string{"notebooks", "entries", "tags"} {
		if got[key] == nil {
			t.Errorf("state.%s is null, want an array", key)
		}
	}
	if got["entries"].([]any)[0].(map[string]any)["tags"] == nil {
		t.Error("node.tags is null, want an array")
	}

	detail := f.get("/api/node/" + id)
	for _, key := range []string{"backlinks", "history"} {
		if detail[key] == nil {
			t.Errorf("detail.%s is null, want an array", key)
		}
	}
}

func TestStateFilters(t *testing.T) {
	f := newFixture(t)
	note := f.newNote("design sketch", "prose about the lexer")
	task := f.newTask("fix the lexer")

	f.patch("/api/node/"+note, map[string]any{"addTag": "design"})
	f.patch("/api/node/"+task, map[string]any{"addTag": "bug", "priority": "high"})

	cases := map[string][]string{
		"?kind=task":         {"fix the lexer"},
		"?kind=note":         {"design sketch"},
		"?tag=bug":           {"fix the lexer"},
		"?status=open":       {"fix the lexer"},
		"?priority=high":     {"fix the lexer"},
		"?q=tokenizes":       {},
		"?q=sketch":          {"design sketch"},
		"?q=lexer&kind=note": {"design sketch"},
	}

	for query, want := range cases {
		t.Run(query, func(t *testing.T) {
			got := entries(f.get("/api/state" + query))
			if len(got) != len(want) {
				t.Fatalf("got %v, want %v", got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("got %v, want %v", got, want)
				}
			}
		})
	}
}

func TestBadFilterIsReportedNotIgnored(t *testing.T) {
	f := newFixture(t)

	res := f.do("GET", "/api/state?kind=widget", nil)
	defer res.Body.Close()

	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", res.StatusCode)
	}
	var body map[string]string
	json.NewDecoder(res.Body).Decode(&body)
	if !strings.Contains(body["error"], "widget") {
		t.Fatalf("error = %q, want it to name the bad value", body["error"])
	}
}

// A search spans the project; not knowing where something is is the reason to
// search for it.
func TestSearchIgnoresTheNotebookConstraint(t *testing.T) {
	f := newFixture(t)
	other := f.post("/api/notebook", map[string]any{"title": "personal"})
	f.post("/api/node", map[string]any{"kind": "note", "title": "hidden treasure", "notebook": other["id"]})

	got := entries(f.get("/api/state?notebook=" + f.work + "&q=treasure"))
	if len(got) != 1 || got[0] != "hidden treasure" {
		t.Fatalf("got %v, want the note from the other notebook", got)
	}
}

func TestNodeDetailIncludesBodyPathAndBacklinks(t *testing.T) {
	f := newFixture(t)
	note := f.newNote("design sketch", "The lexer tokenizes input.")
	task := f.newTask("implement it")
	f.post("/api/node/"+task+"/link", map[string]any{"target": note})

	got := f.get("/api/node/" + note)

	node := got["node"].(map[string]any)
	if !strings.Contains(node["body"].(string), "tokenizes") {
		t.Errorf("body = %v", node["body"])
	}
	path := got["path"].([]any)
	if len(path) != 3 || path[2] != "design sketch" {
		t.Errorf("path = %v", path)
	}
	back := got["backlinks"].([]any)
	if len(back) != 1 || back[0].(map[string]any)["title"] != "implement it" {
		t.Errorf("backlinks = %v", back)
	}
}

// The provenance strip is the one thing this view can show that an ordinary
// notes app cannot.
func TestHistoryShowsTheEventsThatComposedTheEntry(t *testing.T) {
	f := newFixture(t)
	id := f.newTask("fix the lexer")

	f.patch("/api/node/"+id, map[string]any{"addTag": "bug"})
	f.patch("/api/node/"+id, map[string]any{"priority": "high"})
	f.patch("/api/node/"+id, map[string]any{"status": "done"})

	got := f.get("/api/node/" + id)
	history := got["history"].([]any)

	var actions []string
	for _, h := range history {
		actions = append(actions, h.(map[string]any)["action"].(string))
	}
	want := []string{"add.task", "add.tag", "set.priority", "set.status"}
	if strings.Join(actions, ",") != strings.Join(want, ",") {
		t.Fatalf("history = %v, want %v", actions, want)
	}

	// Each entry carries the handle and author needed to read it back.
	first := history[0].(map[string]any)
	if first["ref"] == "" || first["by"] != "sa" || first["at"] == "" {
		t.Fatalf("history entry is incomplete: %v", first)
	}
}

// A link may point at something written on another machine that has not synced
// yet, which the page shows rather than hides.
func TestPendingLinkIsMarkedNotDropped(t *testing.T) {
	f := newFixture(t)
	id := f.newTask("waiting")

	// Reach past the API to make a link whose target genuinely does not exist.
	future := ulid.NewGenerator().New()
	n := f.sess.State.Get(id)
	if err := f.sess.Link(n, &state.Node{ID: future}); err != nil {
		t.Fatal(err)
	}
	if err := f.sess.Commit(); err != nil {
		t.Fatal(err)
	}

	got := f.get("/api/node/" + id)
	links := got["node"].(map[string]any)["links"].([]any)

	if len(links) != 1 {
		t.Fatalf("links = %v", links)
	}
	if links[0].(map[string]any)["pending"] != true {
		t.Fatalf("the unsynced link is not marked pending: %v", links[0])
	}
}

// ---------------------------------------------------------------- writes

func TestCreateNoteAndTask(t *testing.T) {
	f := newFixture(t)

	note := f.post("/api/node", map[string]any{
		"kind": "note", "title": "design sketch", "body": "prose", "notebook": f.work,
		"tags": []string{"design"},
	})
	if note["kind"] != "note" || note["title"] != "design sketch" {
		t.Fatalf("note = %v", note)
	}
	if note["tags"].([]any)[0] != "design" {
		t.Fatalf("tags = %v", note["tags"])
	}

	task := f.post("/api/node", map[string]any{
		"kind": "task", "title": "fix the lexer", "notebook": f.work,
		"due": "2026-09-01", "priority": "high",
	})
	if task["status"] != "open" || task["priority"] != "high" || task["due"] != "2026-09-01" {
		t.Fatalf("task = %v", task)
	}
}

// A first note should not need a notebook created first.
func TestCreateWithoutANotebookUsesTheDefault(t *testing.T) {
	p, err := store.Init(t.TempDir(), "empty", clock())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := session.OpenProject(p, store.Actor{ID: ulid.NewGenerator().New(), Name: "sa"})
	if err != nil {
		t.Fatal(err)
	}
	sess.SetClock(clock)
	if err := sess.Init("empty"); err != nil {
		t.Fatal(err)
	}

	srv, err := New(sess, Options{Token: "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	f := &fixture{t: t, srv: srv, http: ts, sess: sess}
	f.post("/api/node", map[string]any{"kind": "note", "title": "straight in"})

	got := f.get("/api/state")
	if len(got["notebooks"].([]any)) != 1 {
		t.Fatalf("no notebook was created: %v", got["notebooks"])
	}
}

func TestPatchAppliesOnlyWhatIsSent(t *testing.T) {
	f := newFixture(t)
	id := f.newTask("fix the lexer")

	f.patch("/api/node/"+id, map[string]any{"title": "fix the lexer properly"})
	got := f.patch("/api/node/"+id, map[string]any{"status": "doing"})

	// The title set by the earlier request must survive the later one.
	if got["title"] != "fix the lexer properly" {
		t.Fatalf("title = %v", got["title"])
	}
	if got["status"] != "doing" {
		t.Fatalf("status = %v", got["status"])
	}
}

// Fields are pointers so that "set this to empty" differs from "leave it
// alone", which is the only way to clear a due date or a body.
func TestPatchCanClearAField(t *testing.T) {
	f := newFixture(t)
	id := f.newTask("ship it")

	f.patch("/api/node/"+id, map[string]any{"due": "2026-09-01"})
	got := f.patch("/api/node/"+id, map[string]any{"due": ""})

	if got["due"] != nil && got["due"] != "" {
		t.Fatalf("due = %v, want cleared", got["due"])
	}
}

// The rules live in the session, and the browser is subject to them exactly as
// the command line is.
func TestTaskFieldsAreRefusedOnNotes(t *testing.T) {
	f := newFixture(t)
	id := f.newNote("just a note", "")

	for _, body := range []map[string]any{
		{"status": "done"},
		{"priority": "high"},
		{"due": "2026-09-01"},
		{"assign": "me"},
	} {
		res := f.do("PATCH", "/api/node/"+id, body)
		payload, _ := io.ReadAll(res.Body)
		res.Body.Close()

		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("%v: status %d, want 400", body, res.StatusCode)
		}
		if !bytes.Contains(payload, []byte("not a task")) {
			t.Errorf("%v: unhelpful error %s", body, payload)
		}
	}
}

// A rejected request must leave nothing staged, or the next successful write
// would carry it along.
func TestARejectedWriteStagesNothing(t *testing.T) {
	f := newFixture(t)
	id := f.newNote("just a note", "")

	before := len(f.sess.Log())
	res := f.do("PATCH", "/api/node/"+id, map[string]any{"status": "done"})
	res.Body.Close()

	if got := len(f.sess.Log()); got != before {
		t.Fatalf("the log grew by %d after a rejected write", got-before)
	}
	if f.sess.Pending() != 0 {
		t.Fatalf("%d events left staged after a rejected write", f.sess.Pending())
	}
}

func TestTagsAddAndRemove(t *testing.T) {
	f := newFixture(t)
	id := f.newNote("tagged", "")

	f.patch("/api/node/"+id, map[string]any{"addTag": "#Bug"})
	got := f.get("/api/node/" + id)
	tags := got["node"].(map[string]any)["tags"].([]any)
	if len(tags) != 1 || tags[0] != "bug" {
		t.Fatalf("tags = %v, want the normalised form", tags)
	}

	f.patch("/api/node/"+id, map[string]any{"removeTag": "bug"})
	got = f.get("/api/node/" + id)
	if len(got["node"].(map[string]any)["tags"].([]any)) != 0 {
		t.Fatal("the tag survived removal")
	}
}

func TestDeleteAndRestore(t *testing.T) {
	f := newFixture(t)
	id := f.newNote("temporary", "")

	var deleted map[string]any
	f.decodeInto(f.do("DELETE", "/api/node/"+id, nil), &deleted)

	// The id and title come back so the page can offer an undo.
	if deleted["id"] != id || deleted["title"] != "temporary" {
		t.Fatalf("delete returned %v", deleted)
	}
	if len(entries(f.get("/api/state"))) != 0 {
		t.Fatal("the deleted note is still listed")
	}

	f.post("/api/node/"+id+"/restore", nil)
	if got := entries(f.get("/api/state")); len(got) != 1 {
		t.Fatalf("restore did not bring it back: %v", got)
	}
}

func TestMoveBetweenNotebooks(t *testing.T) {
	f := newFixture(t)
	other := f.post("/api/notebook", map[string]any{"title": "personal"})
	id := f.newNote("wanderer", "")

	f.post("/api/node/"+id+"/move", map[string]any{"notebook": other["id"]})

	if got := entries(f.get("/api/state?notebook=" + other["id"].(string))); len(got) != 1 {
		t.Fatalf("the note did not arrive: %v", got)
	}
	if got := entries(f.get("/api/state?notebook=" + f.work)); len(got) != 0 {
		t.Fatalf("the note is still in its old notebook: %v", got)
	}
}

func TestReorderWithinANotebook(t *testing.T) {
	f := newFixture(t)
	first := f.newNote("first", "")
	second := f.newNote("second", "")

	f.post("/api/node/"+first+"/move", map[string]any{"position": "after", "sibling": second})

	got := entries(f.get("/api/state"))
	if got[0] != "second" || got[1] != "first" {
		t.Fatalf("order = %v", got)
	}
}

func TestLinkAndUnlink(t *testing.T) {
	f := newFixture(t)
	note := f.newNote("design sketch", "")
	task := f.newTask("implement it")

	f.post("/api/node/"+task+"/link", map[string]any{"target": note})
	got := f.get("/api/node/" + task)
	if len(got["node"].(map[string]any)["links"].([]any)) != 1 {
		t.Fatal("the link was not made")
	}

	f.post("/api/node/"+task+"/link", map[string]any{"target": note, "remove": true})
	got = f.get("/api/node/" + task)
	if links, ok := got["node"].(map[string]any)["links"]; ok && links != nil && len(links.([]any)) != 0 {
		t.Fatal("the link survived removal")
	}
}

// The command line accepts handles and titles, and so must the browser.
func TestReferencesResolveByHandleAndTitle(t *testing.T) {
	f := newFixture(t)
	id := f.newTask("fix the lexer")
	short := id[len(id)-refLen:]

	for _, ref := range []string{id, short, strings.ToLower(short), "fix the lexer"} {
		res := f.do("GET", "/api/node/"+ref, nil)
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Errorf("could not resolve %q: %d", ref, res.StatusCode)
		}
	}
	res := f.do("GET", "/api/node/nothinglikethis", nil)
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", res.StatusCode)
	}
}

func TestMalformedBodyIsRejected(t *testing.T) {
	f := newFixture(t)

	req, _ := http.NewRequest("POST", f.http.URL+"/api/node", strings.NewReader(`{not json`))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")

	res, err := f.http.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", res.StatusCode)
	}
}

// ---------------------------------------------------------------- live

func TestVersionAdvancesOnEveryWrite(t *testing.T) {
	f := newFixture(t)

	before := f.get("/api/state")["version"].(float64)
	f.newNote("something", "")
	after := f.get("/api/state")["version"].(float64)

	if after <= before {
		t.Fatalf("version went %v -> %v, want an increase", before, after)
	}
}

// The command line and another machine's sync write the same log, so the page
// has to notice changes it did not make.
func TestOutsideWritesAreNoticed(t *testing.T) {
	f := newFixture(t)
	before := f.srv.version.Load()

	other, err := session.OpenProject(f.sess.Project, store.Actor{ID: ulid.NewGenerator().New(), Name: "bob"})
	if err != nil {
		t.Fatal(err)
	}
	other.SetClock(clock)
	if _, err := other.NewNote(f.work, "from elsewhere", ""); err != nil {
		t.Fatal(err)
	}
	if err := other.Commit(); err != nil {
		t.Fatal(err)
	}

	// The watcher runs under Run; the tests drive one poll directly.
	f.srv.checkDisk()

	if f.srv.version.Load() <= before {
		t.Fatal("the outside write did not advance the version")
	}
	got := entries(f.get("/api/state"))
	if len(got) != 1 || got[0] != "from elsewhere" {
		t.Fatalf("entries = %v, want the outside write", got)
	}
}

// A write made here must not then be reported as an outside change, or the
// page would refetch twice for every action.
func TestOwnWritesAreNotSeenAsOutsideChanges(t *testing.T) {
	f := newFixture(t)
	f.newNote("mine", "")

	before := f.srv.version.Load()
	f.srv.checkDisk()

	if got := f.srv.version.Load(); got != before {
		t.Fatalf("version advanced from %d to %d on an unchanged log", before, got)
	}
}

func TestEventStreamPushesOnChange(t *testing.T) {
	f := newFixture(t)

	req, _ := http.NewRequest("GET", f.http.URL+"/api/events", nil)
	req.Header.Set("Authorization", "Bearer test-token")

	res, err := f.http.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()

	if got := res.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q", got)
	}

	reader := bufio.NewReader(res.Body)

	// The first message establishes the baseline.
	first, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(first, "data: ") {
		t.Fatalf("first message = %q", first)
	}

	// A change in another goroutine must arrive on the stream.
	done := make(chan string, 1)
	go func() {
		reader.ReadString('\n') // the blank line closing the first message
		line, _ := reader.ReadString('\n')
		done <- line
	}()

	time.Sleep(50 * time.Millisecond)
	f.newNote("triggers a push", "")

	select {
	case line := <-done:
		if !strings.HasPrefix(line, "data: ") {
			t.Fatalf("pushed message = %q", line)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no message arrived after a change")
	}
}

func TestStreamRequiresTheToken(t *testing.T) {
	f := newFixture(t)

	res, err := f.http.Client().Get(f.http.URL + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", res.StatusCode)
	}
}

// Handlers run concurrently over a session that is not itself safe for it, so
// the lock has to actually hold.
func TestConcurrentRequestsAreSafe(t *testing.T) {
	f := newFixture(t)

	const workers = 12
	done := make(chan error, workers)

	for i := 0; i < workers; i++ {
		go func(i int) {
			res := f.do("POST", "/api/node", map[string]any{
				"kind": "task", "title": fmt.Sprintf("task %d", i), "notebook": f.work,
			})
			res.Body.Close()

			if res.StatusCode != http.StatusOK {
				done <- fmt.Errorf("worker %d: status %d", i, res.StatusCode)
				return
			}
			// Interleave reads with the writes.
			read := f.do("GET", "/api/state", nil)
			read.Body.Close()
			done <- nil
		}(i)
	}

	for i := 0; i < workers; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}

	if got := len(entries(f.get("/api/state"))); got != workers {
		t.Fatalf("got %d entries, want %d; a write was lost", got, workers)
	}
}

func TestURLCarriesTheToken(t *testing.T) {
	f := newFixture(t)

	url := f.srv.URL("127.0.0.1:7777")
	if !strings.Contains(url, "token=test-token") {
		t.Fatalf("URL = %q, want the token in it", url)
	}
	if !strings.HasPrefix(url, "http://127.0.0.1:7777/") {
		t.Fatalf("URL = %q", url)
	}
}

func TestSameOriginHelper(t *testing.T) {
	cases := map[string]bool{
		"http://127.0.0.1:7777":  true,
		"https://127.0.0.1:7777": true,
		"http://localhost:7777":  false,
		"http://evil.example":    false,
		"":                       false,
	}
	for origin, want := range cases {
		if origin == "" {
			continue // an absent Origin is handled before this helper
		}
		if got := sameOrigin(origin, "127.0.0.1:7777"); got != want {
			t.Errorf("sameOrigin(%q) = %v, want %v", origin, got, want)
		}
	}
}
