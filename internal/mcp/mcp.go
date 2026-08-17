// Package mcp serves a gnotes project over the Model Context Protocol, so an
// agent can read and write notes and tasks the same way a person does.
//
// It is a fourth front end over the same session package as the command line,
// the interactive interface and the browser view. Every rule about what an
// operation means lives there, which is what keeps an agent from being able to
// do something the other three cannot.
package mcp

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/shakfu/gnotes/internal/session"
	"github.com/shakfu/gnotes/internal/store"
)

// Protocol versions this server implements, newest first.
//
// A client asks for a version; if it is one of these we answer with the same
// one, otherwise we answer with our newest and let the client decide whether it
// can proceed. That is the negotiation the specification describes.
var protocolVersions = []string{"2025-06-18", "2024-11-05"}

// JSON-RPC 2.0 error codes.
const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternal       = -32603
)

// message is one JSON-RPC frame. Request and notification differ only by
// whether an id is present, so a single struct decodes both.
type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// response is a reply to a request. Exactly one of Result and Error is set.
type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// Error lets a handler return an rpcError as an ordinary error, so the one
// place that distinguishes protocol faults from tool failures is the type
// switch in handle rather than a second return value threaded everywhere.
func (e *rpcError) Error() string { return e.Message }

// Server speaks MCP over a byte stream.
type Server struct {
	sess *session.Session

	// out receives protocol frames and nothing else.
	out *bufio.Writer

	// logw receives diagnostics. On the stdio transport this must not be the
	// same stream as out: a single stray line of human-readable text on the
	// protocol channel makes the next frame unparseable and takes the session
	// down. Everything that is not a frame goes here.
	logw io.Writer

	// fingerprint is the state of the logs when this process last read them,
	// used to notice writes by the command line or another machine.
	fingerprint string

	// name and version identify this server to the client.
	name, version string
}

// New builds a server over an open session.
func New(s *session.Session, name, version string) *Server {
	return &Server{
		sess:        s,
		name:        name,
		version:     version,
		fingerprint: store.Fingerprint(s.Project),
	}
}

// Serve reads frames from in and writes replies to out until in reaches end of
// file, which is how the transport signals shutdown.
//
// Frames are handled one at a time. The protocol permits a client to pipeline
// and accept replies out of order, but every operation here is either an
// in-memory query or a small append, and serialising them means the session
// needs no lock and an agent cannot race itself into a half-applied command.
func (s *Server) Serve(in io.Reader, out io.Writer, logw io.Writer) error {
	s.out = bufio.NewWriter(out)
	s.logw = logw

	reader := bufio.NewReader(in)

	for {
		line, err := readLine(reader)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}

		var msg message
		if err := json.Unmarshal(line, &msg); err != nil {
			// A frame we cannot parse has no id to reply against, so the error
			// carries a null id, as JSON-RPC requires.
			s.send(response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{
				Code: codeParse, Message: "invalid JSON: " + err.Error(),
			}})
			continue
		}

		s.handle(msg)
	}
}

// readLine reads one newline-delimited frame.
//
// bufio.Scanner would be simpler but caps a token at its buffer size, and a
// tools/call carrying a long note body legitimately exceeds any fixed cap. A
// Reader grows to whatever arrives.
func readLine(r *bufio.Reader) ([]byte, error) {
	var full []byte
	for {
		chunk, more, err := r.ReadLine()
		full = append(full, chunk...)
		if err != nil {
			if err == io.EOF && len(full) > 0 {
				return full, nil
			}
			return nil, err
		}
		if !more {
			return full, nil
		}
	}
}

// handle dispatches one frame.
func (s *Server) handle(msg message) {
	// A notification has no id and must never be answered, not even on error.
	notification := len(msg.ID) == 0

	result, err := s.dispatch(msg.Method, msg.Params)
	if notification {
		if err != nil {
			fmt.Fprintf(s.logw, "gnotes mcp: %s: %v\n", msg.Method, err)
		}
		return
	}

	reply := response{JSONRPC: "2.0", ID: msg.ID}
	if err != nil {
		rpc := &rpcError{Code: codeInternal, Message: err.Error()}
		if e := new(rpcError); errors.As(err, &e) {
			rpc = e
		}
		reply.Error = rpc
	} else {
		reply.Result = result
	}
	s.send(reply)
}

// dispatch routes a method to its handler. A nil result with a nil error means
// an empty result object, which several methods return.
func (s *Server) dispatch(method string, params json.RawMessage) (any, error) {
	switch method {
	case "initialize":
		return s.initialize(params)

	case "notifications/initialized", "notifications/cancelled":
		// Nothing to do, and nothing to answer.
		return nil, nil

	case "ping":
		return struct{}{}, nil

	case "tools/list":
		return map[string]any{"tools": s.tools()}, nil

	case "tools/call":
		return s.callTool(params)

	default:
		return nil, &rpcError{Code: codeMethodNotFound, Message: "no method " + method}
	}
}

// initializeParams is the client's half of the handshake. Only the version is
// acted on; the rest is logged so a misbehaving client can be identified.
type initializeParams struct {
	ProtocolVersion string `json:"protocolVersion"`
	ClientInfo      struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"clientInfo"`
}

// initialize answers the handshake with this server's capabilities.
func (s *Server) initialize(raw json.RawMessage) (any, error) {
	var p initializeParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, &rpcError{Code: codeInvalidParams, Message: "malformed initialize params: " + err.Error()}
		}
	}

	// Echo the client's version when it is one we implement; otherwise answer
	// with our newest and let the client decide whether it can continue.
	version := protocolVersions[0]
	for _, known := range protocolVersions {
		if p.ProtocolVersion == known {
			version = known
			break
		}
	}

	return map[string]any{
		"protocolVersion": version,
		"capabilities": map[string]any{
			// The tool list is fixed for the life of the process, so there is
			// nothing to notify about and listChanged stays absent.
			"tools": map[string]any{},
		},
		"serverInfo": map[string]any{
			"name":    s.name,
			"version": s.version,
		},
		"instructions": instructions,
	}, nil
}

// instructions tell the model what this server is for. It is the one piece of
// prose the client puts in front of the model unprompted, so it says what the
// project is and how entries are addressed rather than restating the tool list.
const instructions = `This project's notes and tasks are stored in gnotes, an append-only event log
kept in the repository. Notes hold markdown prose; tasks additionally have a
status, priority, due date and assignees. Both live in notebooks.

Every entry has a six-character handle, shown as its "ref". Pass that handle to
any tool that takes one. A title or a distinctive fragment of one also works,
and an ambiguous reference reports the candidates rather than guessing.

Reading is free of side effects. Writing appends an event, which is durable
immediately and recoverable afterwards: deletion is recorded rather than
applied, so nothing is ever truly lost.`

// send writes one frame, terminated by the newline the transport frames on.
func (s *Server) send(r response) {
	raw, err := json.Marshal(r)
	if err != nil {
		// Encoding our own reply cannot normally fail. If it does, an error
		// frame is still better than silence, which would hang the client on a
		// request that never gets an answer.
		fmt.Fprintf(s.logw, "gnotes mcp: encode reply: %v\n", err)
		raw, _ = json.Marshal(response{JSONRPC: "2.0", ID: r.ID, Error: &rpcError{
			Code: codeInternal, Message: "could not encode the result",
		}})
	}

	s.out.Write(raw)
	s.out.WriteByte('\n')

	// Flushed per frame: the client is blocked waiting for this reply, so
	// holding it in a buffer would deadlock rather than batch.
	if err := s.out.Flush(); err != nil {
		fmt.Fprintf(s.logw, "gnotes mcp: write: %v\n", err)
	}
}

// refresh reloads the project when another process has written to the log.
//
// It runs before every tool call rather than on a timer. The check is a handful
// of stat calls, and an agent that reads a stale tree acts on notes that no
// longer say what it thinks they say.
func (s *Server) refresh() error {
	current := store.Fingerprint(s.sess.Project)
	if current == s.fingerprint {
		return nil
	}
	if err := s.sess.Reload(); err != nil {
		return err
	}
	s.fingerprint = current
	return nil
}

// committed records the state of the log after this server wrote, so its own
// append is not mistaken for an outside change on the next call.
func (s *Server) committed() {
	s.fingerprint = store.Fingerprint(s.sess.Project)
}
