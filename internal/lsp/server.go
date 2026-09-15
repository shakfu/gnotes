// Package lsp serves the wiki to editors over the Language Server Protocol:
// completion of links, diagnostics for broken ones, following a link, backlinks,
// and renaming a page with its links rewritten.
//
// It runs as `gnotes wiki lsp`, speaking LSP on standard input and output.
// Buffers the editor has open are resolved as they are, saved or not; every
// other page is read from the cache, which the server refreshes once a second
// and when a buffer is saved.
package lsp

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"github.com/shakfu/gnotes/internal/wiki"
)

// Opener opens the wiki for a workspace root, which is empty when the editor
// names none.
type Opener func(root string) (*wiki.Wiki, error)

// Server is one editor's connection.
type Server struct {
	open          Opener
	name, version string

	// Poll is how often pages are checked for changes made outside the
	// editor. Zero disables polling.
	Poll time.Duration

	out  io.Writer
	logw io.Writer

	w    *wiki.Wiki
	snap *wiki.Snapshot

	docs map[string]*document // by URI

	initialized, shutdown bool
	utf16                 bool
	documentChanges       bool
	renameFiles           bool
	createFiles           bool
}

// document is a buffer the editor has open.
type document struct {
	uri     string
	page    string // empty when the file is not a page
	version int
	text    *text
}

// New builds a server that opens the wiki when the editor initializes.
func New(open Opener, name, version string) *Server {
	return &Server{open: open, name: name, version: version, Poll: time.Second, docs: map[string]*document{}}
}

// Close closes the wiki the server opened.
func (s *Server) Close() error {
	if s.w == nil {
		return nil
	}
	return s.w.Close()
}

// Serve handles messages until the editor sends exit or closes the input.
func (s *Server) Serve(in io.Reader, out io.Writer, logw io.Writer) error {
	s.out, s.logw = out, logw
	type frame struct {
		body []byte
		err  error
	}
	frames := make(chan frame)
	done := make(chan struct{})
	defer close(done)
	go func() {
		r := bufio.NewReader(in)
		for {
			body, err := readFrame(r)
			select {
			case frames <- frame{body, err}:
			case <-done:
				return
			}
			if err != nil {
				return
			}
		}
	}()

	var tick <-chan time.Time
	if s.Poll > 0 {
		t := time.NewTicker(s.Poll)
		defer t.Stop()
		tick = t.C
	}
	for {
		select {
		case f := <-frames:
			if errors.Is(f.err, io.EOF) {
				return nil
			}
			if f.err != nil {
				return f.err
			}
			if s.handle(f.body) {
				return nil
			}
		case <-tick:
			s.poll()
		}
	}
}

func (s *Server) logf(format string, args ...any) {
	fmt.Fprintf(s.logw, "gnotes lsp: "+format+"\n", args...)
}

func (s *Server) send(v any) {
	if err := writeFrame(s.out, v); err != nil {
		s.logf("write: %v", err)
	}
}

func (s *Server) reply(id json.RawMessage, result any, err error) {
	resp := map[string]any{"jsonrpc": "2.0", "id": id}
	if err != nil {
		rpc := &rpcError{Code: codeRequestFailed, Message: err.Error()}
		if e := new(rpcError); errors.As(err, &e) {
			rpc = e
		}
		resp["error"] = rpc
	} else {
		resp["result"] = result
	}
	s.send(resp)
}

func (s *Server) notify(method string, params any) {
	s.send(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

// handle dispatches one message and reports whether the server should stop.
func (s *Server) handle(body []byte) (exit bool) {
	var msg message
	if err := json.Unmarshal(body, &msg); err != nil {
		s.send(map[string]any{"jsonrpc": "2.0", "id": nil, "error": &rpcError{Code: codeParse, Message: "invalid JSON"}})
		return false
	}
	if msg.Method == "" {
		return false // a response; the server sends no requests
	}
	request := len(msg.ID) > 0

	if msg.Method == "exit" {
		return true
	}
	if !s.initialized && msg.Method != "initialize" {
		if request {
			s.reply(msg.ID, nil, &rpcError{Code: codeNotInitialized, Message: "initialize first"})
		}
		return false
	}
	if s.shutdown && request {
		s.reply(msg.ID, nil, &rpcError{Code: codeInvalidRequest, Message: "the server is shutting down"})
		return false
	}

	h, ok := handlers[msg.Method]
	if !ok {
		if request {
			s.reply(msg.ID, nil, &rpcError{Code: codeMethodNotFound, Message: "no method " + msg.Method})
		}
		return false
	}
	result, err := h(s, msg.Params)
	if request {
		s.reply(msg.ID, result, err)
	} else if err != nil {
		s.logf("%s: %v", msg.Method, err)
	}
	return false
}

type handler func(s *Server, params json.RawMessage) (any, error)

var handlers map[string]handler

func init() {
	handlers = map[string]handler{
		"initialize":  (*Server).initialize,
		"initialized": func(*Server, json.RawMessage) (any, error) { return nil, nil },
		"shutdown": func(s *Server, _ json.RawMessage) (any, error) {
			s.shutdown = true
			return nil, nil
		},
		"$/cancelRequest":                  func(*Server, json.RawMessage) (any, error) { return nil, nil },
		"$/setTrace":                       func(*Server, json.RawMessage) (any, error) { return nil, nil },
		"workspace/didChangeConfiguration": func(*Server, json.RawMessage) (any, error) { return nil, nil },
		"workspace/didChangeWatchedFiles":  func(s *Server, _ json.RawMessage) (any, error) { s.poll(); return nil, nil },
		"textDocument/didOpen":             (*Server).didOpen,
		"textDocument/didChange":           (*Server).didChange,
		"textDocument/didSave":             func(s *Server, _ json.RawMessage) (any, error) { s.poll(); return nil, nil },
		"textDocument/didClose":            (*Server).didClose,
		"textDocument/completion":          (*Server).completion,
		"textDocument/definition":          (*Server).definition,
		"textDocument/references":          (*Server).references,
		"textDocument/hover":               (*Server).hover,
		"textDocument/documentSymbol":      (*Server).documentSymbol,
		"workspace/symbol":                 (*Server).workspaceSymbol,
		"textDocument/prepareRename":       (*Server).prepareRename,
		"textDocument/rename":              (*Server).rename,
		"workspace/willRenameFiles":        (*Server).willRenameFiles,
		"textDocument/codeAction":          (*Server).codeAction,
	}
}

func decode(raw json.RawMessage, v any) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return &rpcError{Code: codeInvalidParams, Message: err.Error()}
	}
	return nil
}

// ---------------------------------------------------------------- lifecycle

func (s *Server) initialize(raw json.RawMessage) (any, error) {
	var p struct {
		RootURI          string `json:"rootUri"`
		RootPath         string `json:"rootPath"`
		WorkspaceFolders []struct {
			URI string `json:"uri"`
		} `json:"workspaceFolders"`
		Capabilities struct {
			General struct {
				PositionEncodings []string `json:"positionEncodings"`
			} `json:"general"`
			Workspace struct {
				WorkspaceEdit struct {
					DocumentChanges    bool     `json:"documentChanges"`
					ResourceOperations []string `json:"resourceOperations"`
				} `json:"workspaceEdit"`
			} `json:"workspace"`
		} `json:"capabilities"`
	}
	if err := decode(raw, &p); err != nil {
		return nil, err
	}

	root := ""
	switch {
	case len(p.WorkspaceFolders) > 0:
		if dir, ok := uriPath(p.WorkspaceFolders[0].URI); ok {
			root = dir
		}
	case p.RootURI != "":
		if dir, ok := uriPath(p.RootURI); ok {
			root = dir
		}
	case p.RootPath != "":
		root = p.RootPath
	}
	w, err := s.open(root)
	if err != nil {
		return nil, fmt.Errorf("%w; run 'gnotes wiki init' in the project", err)
	}
	snap, err := w.Snapshot()
	if err != nil {
		w.Close()
		return nil, err
	}
	s.w, s.snap, s.initialized = w, snap, true

	encoding := "utf-16"
	for _, e := range p.Capabilities.General.PositionEncodings {
		if e == "utf-8" {
			encoding = "utf-8"
		}
	}
	s.utf16 = encoding == "utf-16"
	edit := p.Capabilities.Workspace.WorkspaceEdit
	s.documentChanges = edit.DocumentChanges
	for _, op := range edit.ResourceOperations {
		s.renameFiles = s.renameFiles || op == "rename"
		s.createFiles = s.createFiles || op == "create"
	}

	pagesGlob := map[string]any{"scheme": "file", "pattern": map[string]any{"glob": "**/*.md"}}
	return map[string]any{
		"capabilities": map[string]any{
			"positionEncoding": encoding,
			"textDocumentSync": map[string]any{"openClose": true, "change": 1, "save": map[string]any{"includeText": false}},
			"completionProvider": map[string]any{
				"triggerCharacters": []string{"[", "#", "(", "/"},
			},
			"definitionProvider":      true,
			"referencesProvider":      true,
			"hoverProvider":           true,
			"documentSymbolProvider":  true,
			"workspaceSymbolProvider": true,
			"renameProvider":          map[string]any{"prepareProvider": true},
			"codeActionProvider":      map[string]any{"codeActionKinds": []string{"quickfix"}},
			"workspace": map[string]any{
				"fileOperations": map[string]any{
					"willRename": map[string]any{"filters": []any{pagesGlob}},
				},
			},
		},
		"serverInfo": map[string]any{"name": s.name, "version": s.version},
	}, nil
}

// ---------------------------------------------------------------- documents

func (s *Server) didOpen(raw json.RawMessage) (any, error) {
	var p struct {
		TextDocument struct {
			URI     string `json:"uri"`
			Version int    `json:"version"`
			Text    string `json:"text"`
		} `json:"textDocument"`
	}
	if err := decode(raw, &p); err != nil {
		return nil, err
	}
	d := &document{uri: p.TextDocument.URI, version: p.TextDocument.Version, text: newText([]byte(p.TextDocument.Text), s.utf16)}
	if file, ok := uriPath(d.uri); ok {
		d.page, _ = s.w.PageOf(file)
	}
	s.docs[d.uri] = d
	s.publish(d)
	return nil, nil
}

func (s *Server) didChange(raw json.RawMessage) (any, error) {
	var p struct {
		TextDocument struct {
			URI     string `json:"uri"`
			Version int    `json:"version"`
		} `json:"textDocument"`
		ContentChanges []struct {
			Range *Range `json:"range"`
			Text  string `json:"text"`
		} `json:"contentChanges"`
	}
	if err := decode(raw, &p); err != nil {
		return nil, err
	}
	d, ok := s.docs[p.TextDocument.URI]
	if !ok {
		return nil, fmt.Errorf("%s is not open", p.TextDocument.URI)
	}
	for _, c := range p.ContentChanges {
		if c.Range == nil {
			d.text = newText([]byte(c.Text), s.utf16)
			continue
		}
		// The server asks for whole documents, but a client may send ranges.
		start, end := d.text.offset(c.Range.Start), d.text.offset(c.Range.End)
		src := append(append(append([]byte{}, d.text.src[:start]...), c.Text...), d.text.src[end:]...)
		d.text = newText(src, s.utf16)
	}
	d.version = p.TextDocument.Version
	s.publish(d)
	return nil, nil
}

func (s *Server) didClose(raw json.RawMessage) (any, error) {
	var p struct {
		TextDocument struct {
			URI string `json:"uri"`
		} `json:"textDocument"`
	}
	if err := decode(raw, &p); err != nil {
		return nil, err
	}
	delete(s.docs, p.TextDocument.URI)
	s.notify("textDocument/publishDiagnostics", map[string]any{"uri": p.TextDocument.URI, "diagnostics": []any{}})
	return nil, nil
}

// poll refreshes the cache, and on a change reloads the index and publishes
// diagnostics for every open buffer again.
func (s *Server) poll() {
	if s.w == nil {
		return
	}
	ch, err := s.w.Refresh()
	if err != nil {
		s.logf("refresh: %v", err)
		return
	}
	if ch.Empty() {
		return
	}
	snap, err := s.w.Snapshot()
	if err != nil {
		s.logf("index: %v", err)
		return
	}
	s.snap = snap
	uris := make([]string, 0, len(s.docs))
	for uri := range s.docs {
		uris = append(uris, uri)
	}
	sort.Strings(uris)
	for _, uri := range uris {
		s.publish(s.docs[uri])
	}
}

// source is a page's text: the open buffer, or the file.
func (s *Server) source(page string) (*text, error) {
	uri := fileURI(s.w.PageFile(page))
	if d, ok := s.docs[uri]; ok {
		return d.text, nil
	}
	src, err := os.ReadFile(s.w.PageFile(page))
	if err != nil {
		return nil, err
	}
	return newText(src, s.utf16), nil
}

// doc looks up an open document for a request.
func (s *Server) doc(uri string) (*document, error) {
	d, ok := s.docs[uri]
	if !ok {
		return nil, fmt.Errorf("%s is not open", uri)
	}
	return d, nil
}

// ---------------------------------------------------------------- diagnostics

func (s *Server) links(d *document) []wiki.Link {
	if d.page == "" {
		return nil
	}
	return s.snap.Links(d.page, d.text.src)
}

// linkRange is where a link is drawn: its destination, else the whole link,
// else its line.
func linkRange(t *text, l wiki.Link) Range {
	switch {
	case l.DestStart >= 0:
		return t.rng(l.DestStart, l.DestEnd)
	case l.Start >= 0:
		return t.rng(l.Start, l.End)
	}
	line := max(0, l.Line-1)
	return Range{Position{line, 0}, Position{line, 0}}
}

func problem(l wiki.Link) string {
	switch l.Status {
	case wiki.StatusMissingPage:
		if l.Form == "wiki" {
			return fmt.Sprintf("no page named %q", l.Target)
		}
		return fmt.Sprintf("no page at %s", l.Target)
	case wiki.StatusAmbiguous:
		return fmt.Sprintf("%q names more than one page", l.Target)
	case wiki.StatusMissingHeading:
		return fmt.Sprintf("%s has no heading %q", l.Resolved, l.Anchor)
	case wiki.StatusMissingFile:
		return fmt.Sprintf("no file at %s", l.Target)
	case wiki.StatusLineOutOfRange:
		return fmt.Sprintf("%s has fewer lines than %s", l.Resolved, l.Anchor)
	case wiki.StatusOutsideRepo:
		return fmt.Sprintf("%s is outside the repository", l.Target)
	}
	return l.Status
}

func (s *Server) publish(d *document) {
	diags := []map[string]any{}
	for _, l := range s.links(d) {
		if l.Status == wiki.StatusOK {
			continue
		}
		diags = append(diags, map[string]any{
			"range":    linkRange(d.text, l),
			"severity": 2,
			"source":   "gnotes",
			"code":     l.Status,
			"message":  problem(l),
		})
	}
	s.notify("textDocument/publishDiagnostics", map[string]any{"uri": d.uri, "version": d.version, "diagnostics": diags})
}
