package lsp

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/shakfu/gwiki/internal/wiki"
)

// client is an editor driving a server through its framing.
type client struct {
	t      *testing.T
	root   string
	srv    *Server
	in     *io.PipeWriter
	frames chan message
	nextID int
	notes  []message
	done   chan error
}

var pages = map[string]string{
	"index":               "# Home\n\nSee [[Design sketch#Tokens]] and [code](../../src/lexer.go#L2-L3).\n\nBroken: [[Orphn]] and [gone](nope.md).\n\n- [md](lexer/design-sketch.md)\n",
	"lexer/design-sketch": "---\ntitle: Design sketch\n---\n\n# Design sketch\n\n## Tokens\n\nBack to [[index]].\n",
	"orphan":              "# Orphan\n",
}

func newRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := wiki.Init(root); err != nil {
		t.Fatal(err)
	}
	for id, body := range pages {
		writeFile(t, filepath.Join(root, ".gwiki", "wiki", filepath.FromSlash(id)+".md"), body)
	}
	writeFile(t, filepath.Join(root, "src", "lexer.go"), "package lexer\n\nfunc Lex() {}\nvar x = 1\n")
	return root
}

func writeFile(t *testing.T, file, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func opener(root string) (*wiki.Wiki, error) {
	p, err := wiki.Discover(root)
	if err != nil {
		return nil, err
	}
	return wiki.Open(p)
}

// start runs a server for a repository without initializing it.
func start(t *testing.T, root string) *client {
	t.Helper()
	return startPolling(t, root, 0)
}

// startPolling is start with the server checking pages every poll.
func startPolling(t *testing.T, root string, poll time.Duration) *client {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	c := &client{t: t, root: root, in: inW, frames: make(chan message, 1000), done: make(chan error, 1)}
	c.srv = New(opener, "gwiki", "test")
	c.srv.Poll = poll
	go func() {
		err := c.srv.Serve(inR, outW, io.Discard)
		c.srv.Close()
		outW.Close()
		c.done <- err
	}()
	go func() {
		r := bufio.NewReader(outR)
		for {
			body, err := readFrame(r)
			if err != nil {
				close(c.frames)
				return
			}
			var m message
			if err := json.Unmarshal(body, &m); err != nil {
				panic(err)
			}
			c.frames <- m
		}
	}()
	t.Cleanup(func() {
		inW.Close()
		<-c.done
	})
	return c
}

var fullCapabilities = map[string]any{
	"general":   map[string]any{"positionEncodings": []string{"utf-8", "utf-16"}},
	"workspace": map[string]any{"workspaceEdit": map[string]any{"documentChanges": true, "resourceOperations": []string{"create", "rename"}}},
}

// newClient starts and initializes a server with full capabilities.
func newClient(t *testing.T) *client {
	t.Helper()
	c := start(t, newRepo(t))
	c.initialize(fullCapabilities)
	return c
}

func (c *client) initialize(capabilities map[string]any) map[string]any {
	c.t.Helper()
	var result map[string]any
	if err := c.call("initialize", map[string]any{"rootUri": fileURI(c.root), "capabilities": capabilities}, &result); err != nil {
		c.t.Fatalf("initialize: %v", err)
	}
	c.notify("initialized", map[string]any{})
	return result
}

func (c *client) send(v any) {
	c.t.Helper()
	if err := writeFrame(c.in, v); err != nil {
		c.t.Fatal(err)
	}
}

func (c *client) notify(method string, params any) {
	c.send(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

// call sends a request and decodes its result, returning a protocol error.
func (c *client) call(method string, params, result any) *rpcError {
	c.t.Helper()
	c.nextID++
	id := c.nextID
	c.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	for {
		m := c.next()
		if m.Method != "" {
			c.notes = append(c.notes, m)
			continue
		}
		if string(m.ID) != strings.TrimSpace(mustJSON(id)) {
			c.t.Fatalf("response to %s, want %d", m.ID, id)
		}
		if len(m.Error) > 0 {
			var e rpcError
			json.Unmarshal(m.Error, &e)
			return &e
		}
		if result != nil {
			if err := json.Unmarshal(m.Result, result); err != nil {
				c.t.Fatalf("%s result %s: %v", method, m.Result, err)
			}
		}
		return nil
	}
}

func (c *client) mustCall(method string, params, result any) {
	c.t.Helper()
	if err := c.call(method, params, result); err != nil {
		c.t.Fatalf("%s: %v", method, err)
	}
}

func (c *client) next() message {
	c.t.Helper()
	select {
	case m, ok := <-c.frames:
		if !ok {
			c.t.Fatal("the server closed the connection")
		}
		return m
	case <-time.After(5 * time.Second):
		c.t.Fatal("no message from the server")
	}
	return message{}
}

func mustJSON(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

type diagnostic struct {
	Range   Range  `json:"range"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// diagnostics waits for the next diagnostics published for a URI.
func (c *client) diagnostics(uri string) []diagnostic {
	c.t.Helper()
	for i, m := range c.notes {
		if d, ok := diagnosticsFor(m, uri); ok {
			c.notes = append(c.notes[:i], c.notes[i+1:]...)
			return d
		}
	}
	for {
		m := c.next()
		if d, ok := diagnosticsFor(m, uri); ok {
			return d
		}
		c.notes = append(c.notes, m)
	}
}

func diagnosticsFor(m message, uri string) ([]diagnostic, bool) {
	if m.Method != "textDocument/publishDiagnostics" {
		return nil, false
	}
	var p struct {
		URI         string       `json:"uri"`
		Diagnostics []diagnostic `json:"diagnostics"`
	}
	json.Unmarshal(m.Params, &p)
	return p.Diagnostics, p.URI == uri
}

func (c *client) pageURI(id string) string {
	return fileURI(filepath.Join(c.root, ".gwiki", "wiki", filepath.FromSlash(id)+".md"))
}

func (c *client) open(uri, body string) {
	c.notify("textDocument/didOpen", map[string]any{"textDocument": map[string]any{"uri": uri, "languageId": "markdown", "version": 1, "text": body}})
}

func (c *client) change(uri, body string, version int) {
	c.notify("textDocument/didChange", map[string]any{
		"textDocument":   map[string]any{"uri": uri, "version": version},
		"contentChanges": []any{map[string]any{"text": body}},
	})
}

func at(uri string, line, char int) map[string]any {
	return map[string]any{"textDocument": map[string]any{"uri": uri}, "position": Position{line, char}}
}

// posOf is the UTF-8 position of the first occurrence of needle, plus skip bytes.
func posOf(t *testing.T, body, needle string, skip int) Position {
	t.Helper()
	i := strings.Index(body, needle)
	if i < 0 {
		t.Fatalf("%q not in %q", needle, body)
	}
	return newText([]byte(body), false).pos(i + skip)
}

// ---------------------------------------------------------------- tests

func TestLifecycle(t *testing.T) {
	c := start(t, newRepo(t))
	if err := c.call("textDocument/hover", at("file:///x.md", 0, 0), nil); err == nil || err.Code != codeNotInitialized {
		t.Fatalf("a request before initialize = %v", err)
	}
	result := c.initialize(fullCapabilities)
	caps := result["capabilities"].(map[string]any)
	if caps["positionEncoding"] != "utf-8" || caps["renameProvider"] == nil || caps["completionProvider"] == nil {
		t.Fatalf("capabilities = %v", caps)
	}
	if err := c.call("no/such", nil, nil); err == nil || err.Code != codeMethodNotFound {
		t.Fatalf("unknown method = %v", err)
	}
	c.mustCall("shutdown", nil, nil)
	if err := c.call("textDocument/hover", at("file:///x.md", 0, 0), nil); err == nil || err.Code != codeInvalidRequest {
		t.Fatalf("a request after shutdown = %v", err)
	}
	c.notify("exit", nil)
	if err := <-c.done; err != nil {
		t.Fatalf("Serve = %v", err)
	}
	c.done <- nil // for cleanup
}

func TestInitializeWithoutAWiki(t *testing.T) {
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, ".git"), 0o755)
	c := start(t, root)
	err := c.call("initialize", map[string]any{"rootUri": fileURI(root), "capabilities": map[string]any{}}, nil)
	if err == nil || !strings.Contains(err.Message, "gwiki init") {
		t.Fatalf("initialize without a wiki = %v", err)
	}
}

func TestBadFrameEndsTheSession(t *testing.T) {
	c := start(t, newRepo(t))
	io.WriteString(c.in, "Content-Length: nope\r\n\r\n{}")
	if err := <-c.done; err == nil {
		t.Fatal("a bad header was accepted")
	}
	c.done <- nil
}

func TestDiagnosticsFollowTheBuffer(t *testing.T) {
	c := newClient(t)
	uri := c.pageURI("index")
	c.open(uri, pages["index"])
	d := c.diagnostics(uri)
	if len(d) != 2 || d[0].Message != `no page named "Orphn"` || d[1].Code != wiki.StatusMissingPage || d[1].Message != "no page at nope.md" {
		t.Fatalf("diagnostics = %+v", d)
	}
	if want := posOf(t, pages["index"], "Orphn", 0); d[0].Range.Start != want || d[0].Range.End.Character != want.Character+5 {
		t.Fatalf("range = %+v, want start %+v", d[0].Range, want)
	}

	// Unsaved: the link fixed, a heading added and linked.
	c.change(uri, "# Home\n\n## Added\n\n[[Orphan]] [[#Added]] [gone](nope.md)\n", 2)
	if d := c.diagnostics(uri); len(d) != 1 || d[0].Message != "no page at nope.md" {
		t.Fatalf("after the change = %+v", d)
	}

	// A page created outside the editor clears the last one on save.
	writeFile(t, filepath.Join(c.root, ".gwiki", "wiki", "nope.md"), "# Nope\n")
	c.notify("textDocument/didSave", map[string]any{"textDocument": map[string]any{"uri": uri}})
	if d := c.diagnostics(uri); len(d) != 0 {
		t.Fatalf("after the outside change = %+v", d)
	}

	c.notify("textDocument/didClose", map[string]any{"textDocument": map[string]any{"uri": uri}})
	if d := c.diagnostics(uri); len(d) != 0 {
		t.Fatalf("after close = %+v", d)
	}
}

func TestPollNoticesOutsideChanges(t *testing.T) {
	c := startPolling(t, newRepo(t), 20*time.Millisecond)
	c.initialize(fullCapabilities)
	uri := c.pageURI("index")
	c.open(uri, pages["index"])
	if d := c.diagnostics(uri); len(d) != 2 {
		t.Fatalf("diagnostics = %+v", d)
	}
	writeFile(t, filepath.Join(c.root, ".gwiki", "wiki", "orphn.md"), "# Orphn\n")
	if d := c.diagnostics(uri); len(d) != 1 {
		t.Fatalf("after the poll = %+v", d)
	}
}

type item struct {
	Label    string   `json:"label"`
	Detail   string   `json:"detail"`
	TextEdit TextEdit `json:"textEdit"`
}

func (c *client) complete(uri, body string, line, char int) []item {
	c.t.Helper()
	c.change(uri, body, 9)
	c.diagnostics(uri)
	var items []item
	c.mustCall("textDocument/completion", at(uri, line, char), &items)
	return items
}

func find(items []item, label string) (item, bool) {
	for _, it := range items {
		if it.Label == label {
			return it, true
		}
	}
	return item{}, false
}

func TestCompletion(t *testing.T) {
	c := newClient(t)
	uri := c.pageURI("index")
	c.open(uri, "")
	c.diagnostics(uri)

	items := c.complete(uri, "See [[desk", 0, 10)
	it, ok := find(items, "Design sketch")
	if !ok || it.Detail != "lexer/design-sketch" || it.TextEdit.NewText != "Design sketch]]" || it.TextEdit.Range != (Range{Position{0, 6}, Position{0, 10}}) {
		t.Fatalf("page items = %+v", items)
	}
	if _, ok := find(items, "Orphan"); ok {
		t.Fatal("a page not matching the typed letters was offered")
	}

	items = c.complete(uri, "[[Design sketch#To]]", 0, 18)
	if it, ok := find(items, "Tokens"); !ok || it.TextEdit.NewText != "Tokens" || it.TextEdit.Range.Start.Character != 16 {
		t.Fatalf("heading items = %+v", items)
	}

	// Headings of the buffer itself, unsaved.
	items = c.complete(uri, "## Fresh\n\n[x](#", 2, 5)
	if it, ok := find(items, "Fresh"); !ok || it.TextEdit.NewText != "fresh" {
		t.Fatalf("own heading items = %+v", items)
	}

	items = c.complete(uri, "[x](lexer/", 0, 10)
	if it, ok := find(items, "design-sketch.md"); !ok || it.Detail != "Design sketch" || it.TextEdit.Range.Start.Character != 10 {
		t.Fatalf("path items = %+v", items)
	}
	items = c.complete(uri, "[x](../../s", 0, 11)
	if it, ok := find(items, "src/"); !ok || it.TextEdit.NewText != "src/" || len(items) != 1 {
		t.Fatalf("repository path items = %+v", items)
	}
	if items := c.complete(uri, "[x](../../../../", 0, 16); len(items) != 0 {
		t.Fatalf("completion outside the repository = %+v", items)
	}
	if items := c.complete(uri, "plain text", 0, 5); len(items) != 0 {
		t.Fatalf("completion outside a link = %+v", items)
	}
}

func TestUTF16Positions(t *testing.T) {
	c := start(t, newRepo(t))
	caps := c.initialize(map[string]any{})["capabilities"].(map[string]any)
	if caps["positionEncoding"] != "utf-16" {
		t.Fatalf("encoding = %v", caps["positionEncoding"])
	}
	uri := c.pageURI("index")
	// U+1F600 is two UTF-16 units and four UTF-8 bytes.
	body := "\U0001F600 [[Orphn]]\n"
	c.open(uri, body)
	d := c.diagnostics(uri)
	if len(d) != 1 || d[0].Range.Start != (Position{0, 5}) || d[0].Range.End != (Position{0, 10}) {
		t.Fatalf("UTF-16 range = %+v", d)
	}
	var loc *Location
	c.mustCall("textDocument/hover", at(uri, 0, 6), &loc)
}

func TestDefinitionReferencesAndHover(t *testing.T) {
	c := newClient(t)
	index := c.pageURI("index")
	c.open(index, pages["index"])
	c.diagnostics(index)

	var loc Location
	p := posOf(t, pages["index"], "Design sketch#Tokens", 3)
	c.mustCall("textDocument/definition", at(index, p.Line, p.Character), &loc)
	if loc.URI != c.pageURI("lexer/design-sketch") || loc.Range.Start.Line != 6 {
		t.Fatalf("definition of a heading link = %+v", loc)
	}
	p = posOf(t, pages["index"], "[code]", 2)
	c.mustCall("textDocument/definition", at(index, p.Line, p.Character), &loc)
	if loc.URI != fileURI(filepath.Join(c.root, "src", "lexer.go")) || loc.Range.Start.Line != 1 {
		t.Fatalf("definition of a line link = %+v", loc)
	}
	var none *Location
	c.mustCall("textDocument/definition", at(index, 0, 0), &none)
	if none != nil {
		t.Fatalf("definition off a link = %+v", none)
	}

	var hover struct {
		Contents struct {
			Value string `json:"value"`
		} `json:"contents"`
	}
	c.mustCall("textDocument/hover", at(index, p.Line, p.Character), &hover)
	if !strings.Contains(hover.Contents.Value, "```go\n\nfunc Lex() {}\n```") {
		t.Fatalf("hover on a line link = %q", hover.Contents.Value)
	}
	p = posOf(t, pages["index"], "Orphn", 1)
	c.mustCall("textDocument/hover", at(index, p.Line, p.Character), &hover)
	if !strings.Contains(hover.Contents.Value, "missing-page") || !strings.Contains(hover.Contents.Value, "repairs as code actions") {
		t.Fatalf("hover on a broken link = %q", hover.Contents.Value)
	}

	// References to the page the cursor is on: from the saved index, and from
	// an unsaved buffer that links to it.
	sketch := c.pageURI("lexer/design-sketch")
	c.open(sketch, pages["lexer/design-sketch"])
	c.diagnostics(sketch)
	var refs []Location
	c.mustCall("textDocument/references", map[string]any{"textDocument": map[string]any{"uri": sketch}, "position": Position{0, 0}, "context": map[string]any{"includeDeclaration": false}}, &refs)
	if len(refs) != 2 || refs[0].URI != index || refs[1].URI != index {
		t.Fatalf("references = %+v", refs)
	}
	orphan := c.pageURI("orphan")
	c.open(orphan, "# Orphan\n\n[[Design sketch]]\n")
	c.diagnostics(orphan)
	c.mustCall("textDocument/references", map[string]any{"textDocument": map[string]any{"uri": sketch}, "position": Position{0, 0}, "context": map[string]any{"includeDeclaration": true}}, &refs)
	var uris []string
	for _, r := range refs {
		uris = append(uris, r.URI)
	}
	sort.Strings(uris)
	if strings.Join(uris, " ") != strings.Join([]string{index, index, sketch, orphan}, " ") {
		t.Fatalf("references with an open linking buffer = %+v", refs)
	}
}

func TestSymbols(t *testing.T) {
	c := newClient(t)
	uri := c.pageURI("orphan")
	c.open(uri, "# A\n\ntext\n\n## B\n\n## C\n\n# D\n")
	c.diagnostics(uri)
	var syms []documentSymbol
	c.mustCall("textDocument/documentSymbol", map[string]any{"textDocument": map[string]any{"uri": uri}}, &syms)
	if len(syms) != 2 || syms[0].Name != "A" || len(syms[0].Children) != 2 || syms[0].Children[1].Name != "C" || syms[1].Name != "D" || syms[0].Range.End.Line != 7 {
		t.Fatalf("symbols = %+v", syms)
	}
	var found []struct {
		Name     string   `json:"name"`
		Location Location `json:"location"`
	}
	c.mustCall("workspace/symbol", map[string]any{"query": "desk"}, &found)
	if len(found) != 1 || found[0].Name != "Design sketch" || found[0].Location.URI != c.pageURI("lexer/design-sketch") {
		t.Fatalf("workspace symbols = %+v", found)
	}
}

// apply performs a workspace edit on disk, as an editor would on save.
func apply(t *testing.T, edit map[string]any) {
	t.Helper()
	for _, raw := range edit["documentChanges"].([]any) {
		ch := raw.(map[string]any)
		switch ch["kind"] {
		case "rename":
			from, _ := uriPath(ch["oldUri"].(string))
			to, _ := uriPath(ch["newUri"].(string))
			os.MkdirAll(filepath.Dir(to), 0o755)
			if err := os.Rename(from, to); err != nil {
				t.Fatal(err)
			}
		case "create":
			file, _ := uriPath(ch["uri"].(string))
			writeFile(t, file, "")
		default:
			file, _ := uriPath(ch["textDocument"].(map[string]any)["uri"].(string))
			raw, _ := os.ReadFile(file)
			var edits []TextEdit
			b, _ := json.Marshal(ch["edits"])
			json.Unmarshal(b, &edits)
			txt := newText(raw, false)
			// Later edits first, so earlier offsets hold.
			sort.Slice(edits, func(i, j int) bool { return txt.offset(edits[i].Range.Start) > txt.offset(edits[j].Range.Start) })
			out := string(raw)
			for _, e := range edits {
				s, en := txt.offset(e.Range.Start), txt.offset(e.Range.End)
				out = out[:s] + e.NewText + out[en:]
			}
			writeFile(t, file, out)
		}
	}
}

func TestRename(t *testing.T) {
	c := newClient(t)
	index := c.pageURI("index")
	c.open(index, pages["index"])
	c.diagnostics(index)

	p := posOf(t, pages["index"], "Design sketch#Tokens", 2)
	var prep map[string]any
	c.mustCall("textDocument/prepareRename", at(index, p.Line, p.Character), &prep)
	if prep["placeholder"] != "lexer/design-sketch" {
		t.Fatalf("prepareRename = %v", prep)
	}

	// Unsaved changes in a page the rename edits are refused.
	c.change(index, pages["index"]+"\nunsaved\n", 2)
	c.diagnostics(index)
	params := at(index, p.Line, p.Character)
	params["newName"] = "archive/"
	if err := c.call("textDocument/rename", params, nil); err == nil || !strings.Contains(err.Message, "unsaved changes") {
		t.Fatalf("rename over unsaved changes = %v", err)
	}
	c.change(index, pages["index"], 3)
	c.diagnostics(index)

	var edit map[string]any
	c.mustCall("textDocument/rename", params, &edit)
	changes := edit["documentChanges"].([]any)
	last := changes[len(changes)-1].(map[string]any)
	if last["kind"] != "rename" || last["newUri"] != c.pageURI("archive/design-sketch") {
		t.Fatalf("rename changes = %v", changes)
	}
	apply(t, edit)

	raw, _ := os.ReadFile(filepath.Join(c.root, ".gwiki", "wiki", "index.md"))
	if want := strings.Replace(pages["index"], "[md](lexer/design-sketch.md)", "[md](archive/design-sketch.md)", 1); string(raw) != want {
		t.Fatalf("index after the rename:\n%s", raw)
	}
	w, err := opener(c.root)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	broken, err := w.Check()
	if err != nil {
		t.Fatal(err)
	}
	if len(broken) != 2 { // [[Orphn]] and nope.md, as before
		t.Fatalf("broken after the rename = %+v", broken)
	}

	// An editor that cannot rename files is told so.
	c2 := start(t, newRepo(t))
	c2.initialize(map[string]any{})
	i2 := c2.pageURI("index")
	c2.open(i2, pages["index"])
	c2.diagnostics(i2)
	params = at(i2, p.Line, p.Character)
	params["newName"] = "archive/"
	if err := c2.call("textDocument/rename", params, nil); err == nil || !strings.Contains(err.Message, "gwiki mv") {
		t.Fatalf("rename without file operations = %v", err)
	}
}

func TestWillRenameFiles(t *testing.T) {
	c := newClient(t)
	var edit map[string]any
	c.mustCall("workspace/willRenameFiles", map[string]any{"files": []any{map[string]any{
		"oldUri": c.pageURI("lexer/design-sketch"), "newUri": c.pageURI("notes/sketch"),
	}}}, &edit)
	changes := edit["documentChanges"].([]any)
	var uris []string
	for _, ch := range changes {
		uris = append(uris, ch.(map[string]any)["textDocument"].(map[string]any)["uri"].(string))
	}
	sort.Strings(uris)
	// The page has no relative markdown links of its own to rewrite.
	if len(uris) != 1 || uris[0] != c.pageURI("index") {
		t.Fatalf("will rename edits = %v", uris)
	}
}

func TestCodeActions(t *testing.T) {
	c := newClient(t)
	index := c.pageURI("index")
	c.open(index, pages["index"])
	c.diagnostics(index)

	type action struct {
		Title string         `json:"title"`
		Edit  map[string]any `json:"edit"`
	}
	actionsAt := func(needle string) []action {
		p := posOf(t, pages["index"], needle, 1)
		var out []action
		c.mustCall("textDocument/codeAction", map[string]any{"textDocument": map[string]any{"uri": index}, "range": Range{p, p}, "context": map[string]any{"diagnostics": []any{}}}, &out)
		return out
	}

	got := actionsAt("Orphn")
	if len(got) == 0 || got[0].Title != "Link to Orphan (similar name: orphan)" {
		t.Fatalf("actions for [[Orphn]] = %+v", got)
	}
	edits := got[0].Edit["changes"].(map[string]any)[index].([]any)
	if edits[0].(map[string]any)["newText"] != "Orphan|Orphn" {
		t.Fatalf("edit = %v", edits)
	}

	got = actionsAt("nope.md")
	var create *action
	for i := range got {
		if got[i].Title == "Create page nope" {
			create = &got[i]
		}
	}
	if create == nil {
		t.Fatalf("actions for nope.md = %+v", got)
	}
	apply(t, create.Edit)
	if raw, _ := os.ReadFile(filepath.Join(c.root, ".gwiki", "wiki", "nope.md")); string(raw) != "# gone\n" {
		t.Fatalf("created page = %q", raw)
	}
	if got := actionsAt("See"); len(got) != 0 {
		t.Fatalf("actions away from a broken link = %+v", got)
	}
}

func TestFilesOutsideTheWiki(t *testing.T) {
	c := newClient(t)
	uri := fileURI(filepath.Join(c.root, "README.md"))
	c.open(uri, "[[Nowhere]]\n")
	if d := c.diagnostics(uri); len(d) != 0 {
		t.Fatalf("diagnostics outside the wiki = %+v", d)
	}
	var items []item
	c.mustCall("textDocument/completion", at(uri, 0, 5), &items)
	if len(items) != 0 {
		t.Fatalf("completion outside the wiki = %+v", items)
	}
	if err := c.call("textDocument/rename", map[string]any{"textDocument": map[string]any{"uri": uri}, "position": Position{0, 3}, "newName": "x"}, nil); err == nil {
		t.Fatal("renamed a file outside the wiki")
	}
}
