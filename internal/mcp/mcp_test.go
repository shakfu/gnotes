package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/shakfu/gnotes/internal/session"
	"github.com/shakfu/gnotes/internal/state"
	"github.com/shakfu/gnotes/internal/store"
	"github.com/shakfu/gnotes/internal/ulid"
)

var clock = func() time.Time { return time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC) }

// fixture drives a server over in-memory streams, exactly as the transport
// does, so the tests exercise framing and dispatch rather than the handlers
// alone.
type fixture struct {
	t    *testing.T
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
	nb, err := sess.NewNotebook("work")
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Commit(); err != nil {
		t.Fatal(err)
	}

	return &fixture{t: t, sess: sess, work: nb.ID}
}

// exchange feeds frames through a server and returns the replies, in order.
func (f *fixture) exchange(frames ...string) []map[string]any {
	f.t.Helper()

	var out, logw bytes.Buffer
	srv := New(f.sess, "gnotes", "test")

	if err := srv.Serve(strings.NewReader(strings.Join(frames, "\n")+"\n"), &out, &logw); err != nil {
		f.t.Fatalf("Serve: %v\nstderr: %s", err, logw.String())
	}

	var replies []map[string]any
	for _, line := range strings.Split(strings.TrimRight(out.String(), "\n"), "\n") {
		if line == "" {
			continue
		}
		var reply map[string]any
		if err := json.Unmarshal([]byte(line), &reply); err != nil {
			f.t.Fatalf("reply is not JSON: %v\n%s", err, line)
		}
		replies = append(replies, reply)
	}
	return replies
}

// call invokes one tool and returns the text it produced, failing on a
// protocol error.
func (f *fixture) call(name string, args map[string]any) (text string, isError bool) {
	f.t.Helper()

	frame := f.frame(2, "tools/call", map[string]any{"name": name, "arguments": args})
	replies := f.exchange(frame)

	if len(replies) != 1 {
		f.t.Fatalf("got %d replies, want 1", len(replies))
	}
	if e, ok := replies[0]["error"]; ok {
		f.t.Fatalf("protocol error from %s: %v", name, e)
	}

	result := replies[0]["result"].(map[string]any)
	content := result["content"].([]any)
	if len(content) != 1 {
		f.t.Fatalf("got %d content blocks, want 1", len(content))
	}

	block := content[0].(map[string]any)
	if block["type"] != "text" {
		f.t.Fatalf("content type = %v", block["type"])
	}
	return block["text"].(string), result["isError"] == true
}

// mustCall fails when the tool reports an execution error.
func (f *fixture) mustCall(name string, args map[string]any) string {
	f.t.Helper()
	text, isError := f.call(name, args)
	if isError {
		f.t.Fatalf("%s failed: %s", name, text)
	}
	return text
}

func (f *fixture) frame(id int, method string, params any) string {
	f.t.Helper()
	msg := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		msg["params"] = params
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		f.t.Fatal(err)
	}
	return string(raw)
}

// ---------------------------------------------------------------- protocol

func TestInitializeHandshake(t *testing.T) {
	f := newFixture(t)

	replies := f.exchange(f.frame(1, "initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"clientInfo":      map[string]any{"name": "test", "version": "1"},
	}))

	if len(replies) != 1 {
		t.Fatalf("got %d replies, want 1", len(replies))
	}
	result := replies[0]["result"].(map[string]any)

	if result["protocolVersion"] != "2025-06-18" {
		t.Errorf("protocolVersion = %v", result["protocolVersion"])
	}
	if _, ok := result["capabilities"].(map[string]any)["tools"]; !ok {
		t.Errorf("the tools capability is not declared: %v", result["capabilities"])
	}
	info := result["serverInfo"].(map[string]any)
	if info["name"] != "gnotes" || info["version"] != "test" {
		t.Errorf("serverInfo = %v", info)
	}
	if s, _ := result["instructions"].(string); !strings.Contains(s, "handle") {
		t.Errorf("instructions do not explain handles: %q", s)
	}
}

// A client asking for a version we do not implement gets ours back, so it can
// decide whether to proceed rather than being rejected.
func TestInitializeNegotiatesAnUnknownVersion(t *testing.T) {
	f := newFixture(t)

	replies := f.exchange(f.frame(1, "initialize", map[string]any{"protocolVersion": "1999-01-01"}))
	got := replies[0]["result"].(map[string]any)["protocolVersion"]

	if got != protocolVersions[0] {
		t.Fatalf("protocolVersion = %v, want our newest %q", got, protocolVersions[0])
	}
}

func TestInitializeEchoesAnOlderSupportedVersion(t *testing.T) {
	f := newFixture(t)

	replies := f.exchange(f.frame(1, "initialize", map[string]any{"protocolVersion": "2024-11-05"}))
	if got := replies[0]["result"].(map[string]any)["protocolVersion"]; got != "2024-11-05" {
		t.Fatalf("protocolVersion = %v, want the requested one echoed", got)
	}
}

// A notification carries no id and must never be answered, or the client is
// left correlating a reply against a request it never made.
func TestNotificationsAreNotAnswered(t *testing.T) {
	f := newFixture(t)

	replies := f.exchange(
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1}}`,
		`{"jsonrpc":"2.0","method":"notifications/unknown_to_us"}`,
	)

	if len(replies) != 0 {
		t.Fatalf("got %d replies to notifications, want none: %v", len(replies), replies)
	}
}

func TestPing(t *testing.T) {
	f := newFixture(t)

	replies := f.exchange(f.frame(7, "ping", nil))
	if len(replies) != 1 || replies[0]["result"] == nil {
		t.Fatalf("ping = %v", replies)
	}
	if replies[0]["id"].(float64) != 7 {
		t.Fatalf("id = %v, want the request's id echoed", replies[0]["id"])
	}
}

func TestUnknownMethodIsAProtocolError(t *testing.T) {
	f := newFixture(t)

	replies := f.exchange(f.frame(1, "resources/list", nil))
	e := replies[0]["error"].(map[string]any)

	if int(e["code"].(float64)) != codeMethodNotFound {
		t.Fatalf("code = %v, want %d", e["code"], codeMethodNotFound)
	}
}

func TestMalformedFrameIsReportedWithANullID(t *testing.T) {
	f := newFixture(t)

	replies := f.exchange(`{not json at all`)
	if len(replies) != 1 {
		t.Fatalf("got %d replies", len(replies))
	}
	if replies[0]["id"] != nil {
		t.Errorf("id = %v, want null", replies[0]["id"])
	}
	if code := replies[0]["error"].(map[string]any)["code"].(float64); int(code) != codeParse {
		t.Errorf("code = %v, want %d", code, codeParse)
	}
}

// Every reply must be exactly one line: the transport frames on newlines, so an
// embedded one would split a reply into two unparseable halves.
func TestRepliesAreOneLineEach(t *testing.T) {
	f := newFixture(t)
	if _, err := f.sess.NewNote(f.work, "multi\nline\nbody test", "a\nbody\nwith\nnewlines"); err != nil {
		t.Fatal(err)
	}
	if err := f.sess.Commit(); err != nil {
		t.Fatal(err)
	}

	var out, logw bytes.Buffer
	srv := New(f.sess, "gnotes", "test")
	frame := f.frame(1, "tools/call", map[string]any{
		"name": "gnotes_get", "arguments": map[string]any{"ref": "multi"},
	})
	if err := srv.Serve(strings.NewReader(frame+"\n"), &out, &logw); err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("one reply produced %d lines; a body newline leaked into the framing", len(lines))
	}
	var reply map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &reply); err != nil {
		t.Fatalf("reply is not parseable: %v", err)
	}
}

// A frame longer than any fixed buffer must still be read whole.
func TestLargeFrameIsRead(t *testing.T) {
	f := newFixture(t)

	body := strings.Repeat("lorem ipsum dolor sit amet ", 8000) // ~200 KB
	text := f.mustCall("gnotes_create", map[string]any{
		"kind": "note", "title": "large", "body": body, "notebook": f.work,
	})
	if !strings.Contains(text, "Created note") {
		t.Fatalf("create failed: %s", text)
	}

	got := f.mustCall("gnotes_get", map[string]any{"ref": "large"})
	if !strings.Contains(got, "lorem ipsum") {
		t.Fatal("the large body did not round-trip")
	}
}

// Nothing but protocol frames may reach the output stream — a stray log line
// would corrupt the next frame and end the session.
func TestDiagnosticsGoToTheLogStreamOnly(t *testing.T) {
	f := newFixture(t)

	var out, logw bytes.Buffer
	srv := New(f.sess, "gnotes", "test")

	// A notification that fails: its error has nowhere to go but the log.
	frames := strings.Join([]string{
		`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"nonexistent"}}`,
		f.frame(1, "ping", nil),
	}, "\n")

	if err := srv.Serve(strings.NewReader(frames+"\n"), &out, &logw); err != nil {
		t.Fatal(err)
	}

	for _, line := range strings.Split(strings.TrimRight(out.String(), "\n"), "\n") {
		if line == "" {
			continue
		}
		var probe map[string]any
		if err := json.Unmarshal([]byte(line), &probe); err != nil {
			t.Fatalf("non-protocol output on the frame stream: %q", line)
		}
	}
}

// ---------------------------------------------------------------- tools/list

func TestToolsListIsWellFormed(t *testing.T) {
	f := newFixture(t)

	replies := f.exchange(f.frame(1, "tools/list", nil))
	tools := replies[0]["result"].(map[string]any)["tools"].([]any)

	if len(tools) != len(registry) {
		t.Fatalf("listed %d tools, registry has %d", len(tools), len(registry))
	}

	seen := map[string]bool{}
	for _, raw := range tools {
		tl := raw.(map[string]any)
		name, _ := tl["name"].(string)

		if name == "" || seen[name] {
			t.Fatalf("bad or duplicate tool name %q", name)
		}
		seen[name] = true

		if !strings.HasPrefix(name, "gnotes_") {
			t.Errorf("%s is not namespaced; it could collide with another server's tool", name)
		}

		desc, _ := tl["description"].(string)
		// Under-described tools are the most common cause of a model failing to
		// call the right one, so the floor is a real paragraph, not a label.
		if len(desc) < 120 {
			t.Errorf("%s has a %d-character description; say when to call it", name, len(desc))
		}
		// Emphasis written to force a call on an older model over-triggers now.
		for _, shout := range []string{"CRITICAL", "YOU MUST", "ALWAYS use", "NEVER use"} {
			if strings.Contains(desc, shout) {
				t.Errorf("%s description contains %q, which over-triggers", name, shout)
			}
		}

		schema, ok := tl["inputSchema"].(map[string]any)
		if !ok || schema["type"] != "object" {
			t.Fatalf("%s has no object input schema", name)
		}
		// Every parameter needs a description; a bare type tells the model
		// nothing about what to put there.
		for prop, raw := range schema["properties"].(map[string]any) {
			if d, _ := raw.(map[string]any)["description"].(string); d == "" {
				t.Errorf("%s parameter %q has no description", name, prop)
			}
		}
	}
}

// Annotations tell a client what to confirm with the user, so they must match
// what the tool actually does.
func TestAnnotationsMatchBehaviour(t *testing.T) {
	readOnly := map[string]bool{"gnotes_list": true, "gnotes_search": true, "gnotes_get": true}

	for _, entry := range registry {
		if entry.Annotations == nil {
			t.Errorf("%s has no annotations", entry.Name)
			continue
		}
		if got := entry.Annotations.ReadOnlyHint; got != readOnly[entry.Name] {
			t.Errorf("%s readOnlyHint = %v, want %v", entry.Name, got, readOnly[entry.Name])
		}
		if entry.Name == "gnotes_delete" {
			if entry.Annotations.DestructiveHint == nil || !*entry.Annotations.DestructiveHint {
				t.Error("gnotes_delete is not marked destructive")
			}
		}
	}
}

// ---------------------------------------------------------------- tools/call

func TestCreateAndRead(t *testing.T) {
	f := newFixture(t)

	created := f.mustCall("gnotes_create", map[string]any{
		"kind": "task", "title": "fix the lexer", "notebook": "work",
		"tags": []string{"bug"}, "priority": "high", "due": "2026-09-01",
	})
	if !strings.Contains(created, "Created task") {
		t.Fatalf("create said: %s", created)
	}

	got := f.mustCall("gnotes_get", map[string]any{"ref": "fix the lexer"})
	for _, want := range []string{"kind: task", "status: open", "priority: high", "due: 2026-09-01", "tags: bug"} {
		if !strings.Contains(got, want) {
			t.Errorf("get output is missing %q:\n%s", want, got)
		}
	}
}

func TestListFilters(t *testing.T) {
	f := newFixture(t)
	f.mustCall("gnotes_create", map[string]any{"kind": "note", "title": "design sketch", "notebook": "work", "tags": []string{"design"}})
	f.mustCall("gnotes_create", map[string]any{"kind": "task", "title": "fix the lexer", "notebook": "work", "tags": []string{"bug"}})

	cases := map[string]struct {
		args    map[string]any
		want    string
		notWant string
	}{
		"everything":  {map[string]any{}, "design sketch", ""},
		"by kind":     {map[string]any{"kind": "task"}, "fix the lexer", "design sketch"},
		"by tag":      {map[string]any{"tags": []string{"bug"}}, "fix the lexer", "design sketch"},
		"by status":   {map[string]any{"status": "open"}, "fix the lexer", "design sketch"},
		"by text":     {map[string]any{"text": "sketch"}, "design sketch", "fix the lexer"},
		"by notebook": {map[string]any{"notebook": "work"}, "fix the lexer", ""},
		"notebooks":   {map[string]any{"kind": "notebook"}, "work", "fix the lexer"},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got := f.mustCall("gnotes_list", c.args)
			if !strings.Contains(got, c.want) {
				t.Errorf("missing %q:\n%s", c.want, got)
			}
			if c.notWant != "" && strings.Contains(got, c.notWant) {
				t.Errorf("unexpectedly present %q:\n%s", c.notWant, got)
			}
		})
	}
}

func TestListRespectsTheLimitAndSaysSo(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < 5; i++ {
		f.mustCall("gnotes_create", map[string]any{
			"kind": "note", "title": fmt.Sprintf("note %d", i), "notebook": "work",
		})
	}

	got := f.mustCall("gnotes_list", map[string]any{"limit": 2})
	if strings.Count(got, "note ") < 2 {
		t.Fatalf("fewer than the limit returned:\n%s", got)
	}
	if !strings.Contains(got, "more not shown") {
		t.Fatalf("truncation was silent; the model cannot tell it saw a partial list:\n%s", got)
	}
}

func TestSearchFindsBodyText(t *testing.T) {
	f := newFixture(t)
	f.mustCall("gnotes_create", map[string]any{
		"kind": "note", "title": "design sketch", "notebook": "work",
		"body": "The lexer tokenizes input before the parser runs.",
	})

	got := f.mustCall("gnotes_search", map[string]any{"query": "tokenizes"})
	if !strings.Contains(got, "design sketch") {
		t.Fatalf("search missed a body match:\n%s", got)
	}
	if !strings.Contains(got, "tokenizes") {
		t.Fatalf("no snippet explaining the match:\n%s", got)
	}

	if got := f.mustCall("gnotes_search", map[string]any{"query": "nothinglikethis"}); !strings.Contains(got, "Nothing matches") {
		t.Fatalf("a fruitless search said: %s", got)
	}
}

func TestUpdateChangesOnlyWhatIsGiven(t *testing.T) {
	f := newFixture(t)
	f.mustCall("gnotes_create", map[string]any{"kind": "task", "title": "ship it", "notebook": "work", "body": "keep me"})

	f.mustCall("gnotes_update", map[string]any{"ref": "ship it", "status": "done"})
	got := f.mustCall("gnotes_get", map[string]any{"ref": "ship it"})

	if !strings.Contains(got, "status: done") {
		t.Errorf("status did not change:\n%s", got)
	}
	if !strings.Contains(got, "keep me") {
		t.Errorf("the body was lost by an unrelated update:\n%s", got)
	}
}

func TestUpdateWithNoFieldsSaysSo(t *testing.T) {
	f := newFixture(t)
	f.mustCall("gnotes_create", map[string]any{"kind": "note", "title": "untouched", "notebook": "work"})

	got := f.mustCall("gnotes_update", map[string]any{"ref": "untouched"})
	if !strings.Contains(got, "Nothing to change") {
		t.Fatalf("a no-op update said: %s", got)
	}
}

func TestUpdateTagsAndLinks(t *testing.T) {
	f := newFixture(t)
	f.mustCall("gnotes_create", map[string]any{"kind": "note", "title": "design sketch", "notebook": "work"})
	f.mustCall("gnotes_create", map[string]any{"kind": "task", "title": "implement it", "notebook": "work"})

	f.mustCall("gnotes_update", map[string]any{"ref": "implement it", "addTags": []string{"Bug", "parser"}, "link": "design sketch"})

	got := f.mustCall("gnotes_get", map[string]any{"ref": "implement it"})
	if !strings.Contains(got, "tags: bug, parser") {
		t.Errorf("tags = %s", got)
	}
	if !strings.Contains(got, "references:") || !strings.Contains(got, "design sketch") {
		t.Errorf("the link is not shown:\n%s", got)
	}

	// And the other direction.
	back := f.mustCall("gnotes_get", map[string]any{"ref": "design sketch"})
	if !strings.Contains(back, "referenced by:") {
		t.Errorf("no backlink:\n%s", back)
	}

	f.mustCall("gnotes_update", map[string]any{"ref": "implement it", "removeTags": []string{"bug"}})
	if got := f.mustCall("gnotes_get", map[string]any{"ref": "implement it"}); strings.Contains(got, "bug") {
		t.Errorf("the tag survived removal:\n%s", got)
	}
}

// The kind distinction is enforced in the session, and an agent is subject to
// it exactly as a person is.
func TestTaskFieldsAreRefusedOnNotes(t *testing.T) {
	f := newFixture(t)
	f.mustCall("gnotes_create", map[string]any{"kind": "note", "title": "just a note", "notebook": "work"})

	for _, args := range []map[string]any{
		{"ref": "just a note", "status": "done"},
		{"ref": "just a note", "priority": "high"},
		{"ref": "just a note", "due": "2026-09-01"},
	} {
		text, isError := f.call("gnotes_update", args)
		if !isError {
			t.Errorf("%v was accepted on a note", args)
		}
		if !strings.Contains(text, "not a task") {
			t.Errorf("%v: unhelpful message %q", args, text)
		}
	}
}

// A failed operation is a readable result, not a protocol fault: the model has
// to be able to see what went wrong and try something else.
func TestOperationFailuresAreToolErrorsNotProtocolErrors(t *testing.T) {
	f := newFixture(t)

	for name, args := range map[string]map[string]any{
		"unknown entry":   {"ref": "nothing like this exists"},
		"empty reference": {"ref": ""},
	} {
		t.Run(name, func(t *testing.T) {
			text, isError := f.call("gnotes_get", args)
			if !isError {
				t.Fatal("the failure was not reported")
			}
			if text == "" {
				t.Fatal("the error carries no explanation")
			}
		})
	}
}

// An ambiguous reference must list the candidates so the model can pick one.
func TestAmbiguousReferenceListsCandidates(t *testing.T) {
	f := newFixture(t)
	f.mustCall("gnotes_create", map[string]any{"kind": "note", "title": "parser one", "notebook": "work"})
	f.mustCall("gnotes_create", map[string]any{"kind": "note", "title": "parser two", "notebook": "work"})

	text, isError := f.call("gnotes_get", map[string]any{"ref": "parser"})
	if !isError {
		t.Fatal("an ambiguous reference was resolved")
	}
	for _, want := range []string{"parser one", "parser two"} {
		if !strings.Contains(text, want) {
			t.Errorf("the message does not list %q: %s", want, text)
		}
	}
}

// An unknown tool is a client mistake rather than a failed operation, so it
// gets a protocol error.
func TestUnknownToolIsAProtocolError(t *testing.T) {
	f := newFixture(t)

	replies := f.exchange(f.frame(1, "tools/call", map[string]any{"name": "gnotes_teleport"}))
	e, ok := replies[0]["error"].(map[string]any)
	if !ok {
		t.Fatalf("an unknown tool returned a result: %v", replies[0])
	}
	if int(e["code"].(float64)) != codeInvalidParams {
		t.Errorf("code = %v", e["code"])
	}
}

// The schemas forbid unknown properties, so a mistyped argument is reported
// rather than silently dropped.
func TestUnknownArgumentIsRejected(t *testing.T) {
	f := newFixture(t)

	text, isError := f.call("gnotes_list", map[string]any{"kynd": "task"})
	if !isError {
		t.Fatalf("a mistyped argument was ignored: %s", text)
	}
	if !strings.Contains(text, "kynd") {
		t.Errorf("the message does not name the bad field: %s", text)
	}
}

func TestDeleteAndRestore(t *testing.T) {
	f := newFixture(t)
	f.mustCall("gnotes_create", map[string]any{"kind": "note", "title": "temporary", "notebook": "work"})

	deleted := f.mustCall("gnotes_delete", map[string]any{"ref": "temporary"})
	if !strings.Contains(deleted, "gnotes_restore") {
		t.Errorf("the delete message does not say how to undo it: %s", deleted)
	}
	if got := f.mustCall("gnotes_list", nil); strings.Contains(got, "temporary") {
		t.Errorf("the deleted note is still listed:\n%s", got)
	}

	f.mustCall("gnotes_restore", map[string]any{"ref": "temporary"})
	if got := f.mustCall("gnotes_list", nil); !strings.Contains(got, "temporary") {
		t.Errorf("restore did not bring it back:\n%s", got)
	}
}

func TestCreateWithoutANotebookUsesTheDefault(t *testing.T) {
	t.Helper()

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

	f := &fixture{t: t, sess: sess}
	f.mustCall("gnotes_create", map[string]any{"kind": "note", "title": "straight in"})

	if got := f.mustCall("gnotes_list", map[string]any{"kind": "notebook"}); !strings.Contains(got, "inbox") {
		t.Fatalf("no default notebook was created:\n%s", got)
	}
}

// Writes must reach disk when the tool returns, not when the process exits: a
// client may stop the server at any point.
func TestWritesAreCommittedImmediately(t *testing.T) {
	f := newFixture(t)
	f.mustCall("gnotes_create", map[string]any{"kind": "note", "title": "persisted", "notebook": "work"})

	if f.sess.Pending() != 0 {
		t.Fatalf("%d events left uncommitted", f.sess.Pending())
	}
	other, err := session.OpenProject(f.sess.Project, f.sess.Actor)
	if err != nil {
		t.Fatal(err)
	}
	if got := other.State.List(state.Filter{Text: "persisted"}, state.OrderRank); len(got) != 1 {
		t.Fatal("the entry did not reach disk")
	}
}

// The command line or another machine may write while the server is running.
func TestOutsideWritesAreNoticed(t *testing.T) {
	f := newFixture(t)
	srv := New(f.sess, "gnotes", "test")

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

	// Going through the server, which refreshes before each call.
	var out, logw bytes.Buffer
	frame := f.frame(1, "tools/call", map[string]any{"name": "gnotes_list", "arguments": map[string]any{}})
	if err := srv.Serve(strings.NewReader(frame+"\n"), &out, &logw); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(out.String(), "from elsewhere") {
		t.Fatalf("the outside write was not picked up:\n%s", out.String())
	}
}

// Several frames on one connection must each get exactly one reply, in order.
func TestSequentialRequestsOnOneConnection(t *testing.T) {
	f := newFixture(t)

	replies := f.exchange(
		f.frame(1, "initialize", map[string]any{"protocolVersion": "2025-06-18"}),
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		f.frame(2, "tools/list", nil),
		f.frame(3, "tools/call", map[string]any{
			"name": "gnotes_create", "arguments": map[string]any{"kind": "note", "title": "one", "notebook": "work"},
		}),
		f.frame(4, "tools/call", map[string]any{
			"name": "gnotes_list", "arguments": map[string]any{},
		}),
	)

	if len(replies) != 4 {
		t.Fatalf("got %d replies, want 4 (the notification is unanswered)", len(replies))
	}
	for i, want := range []float64{1, 2, 3, 4} {
		if replies[i]["id"].(float64) != want {
			t.Fatalf("reply %d has id %v, want %v", i, replies[i]["id"], want)
		}
	}

	last := replies[3]["result"].(map[string]any)["content"].([]any)[0].(map[string]any)
	if !strings.Contains(last["text"].(string), "one") {
		t.Fatalf("the entry created earlier in the session is not listed: %v", last)
	}
}
