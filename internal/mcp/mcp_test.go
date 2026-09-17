package mcp

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shakfu/gwiki/internal/wiki"
)

// fixture drives a server over in-memory streams, exactly as the transport
// does, so the tests exercise framing and dispatch rather than the handlers
// alone.
type fixture struct {
	t    *testing.T
	wiki *wiki.Wiki
}

// newFixture serves an empty wiki in a new repository.
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
	w, err := wiki.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	return &fixture{t: t, wiki: w}
}

// exchange feeds frames through a server and returns the replies, in order.
func (f *fixture) exchange(frames ...string) []map[string]any {
	f.t.Helper()

	var out, logw bytes.Buffer
	srv := New(f.wiki, "gwiki", "test")

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
	if info["name"] != "gwiki" || info["version"] != "test" {
		t.Errorf("serverInfo = %v", info)
	}
	if s, _ := result["instructions"].(string); !strings.Contains(s, ".gwiki/wiki") {
		t.Errorf("instructions do not say where pages are: %q", s)
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
	f.page("multi", "# multi\nline\nbody test\n")

	// exchange parses each output line as one reply, so a leaked newline shows
	// up as an extra reply or a parse failure.
	replies := f.exchange(f.frame(1, "tools/call", map[string]any{
		"name": "gwiki_read", "arguments": map[string]any{"page": "multi"},
	}))
	if len(replies) != 1 {
		t.Fatalf("one request produced %d replies; a body newline leaked into the framing", len(replies))
	}
}

// A frame longer than any fixed buffer must still be read whole.
func TestLargeFrameIsRead(t *testing.T) {
	f := newFixture(t)

	body := strings.Repeat("lorem ipsum dolor sit amet ", 8000) // ~200 KB
	f.mustCall("gwiki_create", map[string]any{"title": "large", "body": body})

	got := f.mustCall("gwiki_read", map[string]any{"page": "large"})
	if !strings.Contains(got, "lorem ipsum") {
		t.Fatal("the large body did not round-trip")
	}
}

// Nothing but protocol frames may reach the output stream — a stray log line
// would corrupt the next frame and end the session.
func TestDiagnosticsGoToTheLogStreamOnly(t *testing.T) {
	f := newFixture(t)

	var out, logw bytes.Buffer
	srv := New(f.wiki, "gwiki", "test")

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

	if len(tools) != len(wikiRegistry) {
		t.Fatalf("listed %d tools, registry has %d", len(tools), len(wikiRegistry))
	}

	seen := map[string]bool{}
	for _, raw := range tools {
		tl := raw.(map[string]any)
		name, _ := tl["name"].(string)

		if name == "" || seen[name] {
			t.Fatalf("bad or duplicate tool name %q", name)
		}
		seen[name] = true

		if !strings.HasPrefix(name, "gwiki_") {
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

// ---------------------------------------------------------------- tools/call

func TestOperationFailuresAreToolErrorsNotProtocolErrors(t *testing.T) {
	f := newFixture(t)

	for name, args := range map[string]map[string]any{
		"unknown page":    {"page": "nothing like this exists"},
		"empty page name": {"page": ""},
	} {
		t.Run(name, func(t *testing.T) {
			text, isError := f.call("gwiki_read", args)
			if !isError {
				t.Fatal("the failure was not reported")
			}
			if text == "" {
				t.Fatal("the error carries no explanation")
			}
		})
	}
}

// An ambiguous page name must list the candidates so the model can pick one.
func TestAmbiguousPageNameListsCandidates(t *testing.T) {
	f := newFixture(t)
	f.page("parser-one", "# parser one\n")
	f.page("parser-two", "# parser two\n")

	text, isError := f.call("gwiki_read", map[string]any{"page": "parser"})
	if !isError {
		t.Fatal("an ambiguous page name was resolved")
	}
	for _, want := range []string{"parser-one", "parser-two"} {
		if !strings.Contains(text, want) {
			t.Errorf("the message does not list %q: %s", want, text)
		}
	}
}

// An unknown tool is a client mistake rather than a failed operation, so it
// gets a protocol error.
func TestUnknownToolIsAProtocolError(t *testing.T) {
	f := newFixture(t)

	replies := f.exchange(f.frame(1, "tools/call", map[string]any{"name": "gwiki_teleport"}))
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

	text, isError := f.call("gwiki_list", map[string]any{"tga": "design"})
	if !isError {
		t.Fatalf("a mistyped argument was ignored: %s", text)
	}
	if !strings.Contains(text, "tga") {
		t.Errorf("the message does not name the bad field: %s", text)
	}
}

// The command line or an editor may write while the server is running.
func TestOutsideWritesAreNoticed(t *testing.T) {
	f := newFixture(t)
	f.mustCall("gwiki_list", nil)
	f.page("elsewhere", "# from elsewhere\n")

	// The server refreshes before each call.
	if out := f.mustCall("gwiki_list", nil); !strings.Contains(out, "from elsewhere") {
		t.Fatalf("the outside write was not picked up:\n%s", out)
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
			"name": "gwiki_create", "arguments": map[string]any{"title": "one"},
		}),
		f.frame(4, "tools/call", map[string]any{
			"name": "gwiki_list", "arguments": map[string]any{},
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
		t.Fatalf("the page created earlier on the connection is not listed: %v", last)
	}
}

// Frames that are not valid JSON-RPC 2.0 requests get -32600, with a null id
// where the frame's own id cannot be echoed.
func TestInvalidEnvelopesAreRejected(t *testing.T) {
	cases := map[string]string{
		"batch":         `[{"jsonrpc":"2.0","id":1,"method":"ping"}]`,
		"bare value":    `42`,
		"null id":       `{"jsonrpc":"2.0","id":null,"method":"ping"}`,
		"object id":     `{"jsonrpc":"2.0","id":{"a":1},"method":"ping"}`,
		"wrong version": `{"jsonrpc":"1.0","id":1,"method":"ping"}`,
		"no version":    `{"id":1,"method":"ping"}`,
		"no method":     `{"jsonrpc":"2.0","id":1}`,
	}
	for name, frame := range cases {
		t.Run(name, func(t *testing.T) {
			replies := newFixture(t).exchange(frame)
			if len(replies) != 1 {
				t.Fatalf("got %d replies", len(replies))
			}
			e, ok := replies[0]["error"].(map[string]any)
			if !ok || int(e["code"].(float64)) != codeInvalidRequest {
				t.Fatalf("reply = %v, want -32600", replies[0])
			}
		})
	}
}

// A response frame from the client, and a request without an id, get no
// reply. The request must also not run: a tools/call without an id would change
// the project with nobody told.
func TestFramesWithoutARequestAreNotAnsweredOrRun(t *testing.T) {
	f := newFixture(t)
	call := `{"jsonrpc":"2.0","method":"tools/call","params":{"name":"gwiki_create","arguments":{"title":"ghost"}}}`
	replies := f.exchange(`{"jsonrpc":"2.0","id":5,"result":{}}`, call)
	if len(replies) != 0 {
		t.Fatalf("got replies %v, want none", replies)
	}
	if _, err := os.Stat(filepath.Join(f.wiki.PagesPath(), "ghost.md")); err == nil {
		t.Fatal("a tools/call without an id created a page")
	}
}

// A method with nothing to return still answers with a result object.
func TestEveryReplyCarriesAResultOrAnError(t *testing.T) {
	replies := newFixture(t).exchange(`{"jsonrpc":"2.0","id":4,"method":"notifications/initialized"}`)
	if len(replies) != 1 {
		t.Fatalf("got %d replies", len(replies))
	}
	if _, ok := replies[0]["result"]; !ok {
		t.Fatalf("reply = %v, want a result", replies[0])
	}
}
