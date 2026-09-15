package lsp

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/textproto"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// JSON-RPC and LSP error codes.
const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternal       = -32603
	codeNotInitialized = -32002
	codeRequestFailed  = -32803
)

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return e.Message }

// maxFrame bounds a frame's declared length, so a corrupt header cannot make
// the server allocate without limit.
const maxFrame = 64 << 20

// readFrame reads one Content-Length framed message.
func readFrame(r *bufio.Reader) ([]byte, error) {
	header, err := textproto.NewReader(r).ReadMIMEHeader()
	if err != nil {
		if errors.Is(err, io.EOF) && len(header) == 0 {
			return nil, io.EOF
		}
		return nil, fmt.Errorf("read header: %w", err)
	}
	n, err := strconv.Atoi(header.Get("Content-Length"))
	if err != nil || n < 0 || n > maxFrame {
		return nil, fmt.Errorf("bad Content-Length %q", header.Get("Content-Length"))
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	return body, nil
}

func writeFrame(w io.Writer, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Content-Length: %d\r\n\r\n", len(body)); err != nil {
		return err
	}
	_, err = w.Write(body)
	return err
}

// ---------------------------------------------------------------- types

type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

type Location struct {
	URI   string `json:"uri"`
	Range Range  `json:"range"`
}

type TextEdit struct {
	Range   Range  `json:"range"`
	NewText string `json:"newText"`
}

type textDocumentPosition struct {
	TextDocument struct {
		URI string `json:"uri"`
	} `json:"textDocument"`
	Position Position `json:"position"`
}

// ---------------------------------------------------------------- URIs

// fileURI is the file:// URI of an absolute path.
func fileURI(path string) string {
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
}

// uriPath is the path a file:// URI names.
func uriPath(uri string) (string, bool) {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" {
		return "", false
	}
	return filepath.FromSlash(u.Path), true
}

// ---------------------------------------------------------------- text

// text is a document with line offsets, converting between byte offsets and
// LSP positions in UTF-8 or UTF-16 code units.
type text struct {
	src   []byte
	lines []int
	utf16 bool
}

func newText(src []byte, utf16 bool) *text {
	lines := []int{0}
	for i, c := range src {
		if c == '\n' {
			lines = append(lines, i+1)
		}
	}
	return &text{src: src, lines: lines, utf16: utf16}
}

// pos is the position of a byte offset.
func (t *text) pos(off int) Position {
	off = max(0, min(off, len(t.src)))
	line := 0
	lo, hi := 0, len(t.lines)
	for lo < hi {
		mid := (lo + hi) / 2
		if t.lines[mid] <= off {
			line, lo = mid, mid+1
		} else {
			hi = mid
		}
	}
	return Position{Line: line, Character: t.units(t.src[t.lines[line]:off])}
}

func (t *text) units(b []byte) int {
	if !t.utf16 {
		return len(b)
	}
	n := 0
	for len(b) > 0 {
		r, size := utf8.DecodeRune(b)
		n += len(utf16.Encode([]rune{r}))
		b = b[size:]
	}
	return n
}

// offset is the byte offset of a position, clamped to the document and to the
// end of its line.
func (t *text) offset(p Position) int {
	if p.Line < 0 {
		return 0
	}
	if p.Line >= len(t.lines) {
		return len(t.src)
	}
	start := t.lines[p.Line]
	end := len(t.src)
	if p.Line+1 < len(t.lines) {
		end = t.lines[p.Line+1] - 1
	}
	off, n := start, 0
	for off < end && n < p.Character {
		r, size := utf8.DecodeRune(t.src[off:end])
		if t.utf16 {
			n += len(utf16.Encode([]rune{r}))
		} else {
			n += size
		}
		off += size
	}
	return off
}

func (t *text) rng(start, end int) Range { return Range{t.pos(start), t.pos(end)} }

// lineBefore is the text from the start of the offset's line to the offset.
func (t *text) lineBefore(off int) string {
	start := t.lines[t.pos(off).Line]
	return string(t.src[start:off])
}

// lineAfter is the text from the offset to the end of its line.
func (t *text) lineAfter(off int) string {
	rest := t.src[off:]
	if i := strings.IndexByte(string(rest), '\n'); i >= 0 {
		rest = rest[:i]
	}
	return string(rest)
}
