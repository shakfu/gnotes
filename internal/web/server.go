// Package web serves an optional browser view of a gnotes project.
//
// It is a third front end over the same session package the command line and
// the interactive interface use, so all three agree on what an operation means
// and what it records. The assets are compiled into the binary, so there is
// nothing to install, nothing to fetch at runtime, and the page works with no
// network at all.
package web

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shakfu/gnotes/internal/session"
	"github.com/shakfu/gnotes/internal/store"
)

// assets holds the page, its stylesheet and its script.
//
//go:embed assets
var assets embed.FS

// Options configure a server.
type Options struct {
	// Addr is the listen address. It defaults to a loopback address, because
	// a notes server has no authentication model beyond its token and no
	// business being reachable from the network.
	Addr string

	// Token authorises API requests. One is generated when this is empty.
	Token string

	// PollInterval is how often the event logs are checked for changes made by
	// another process. Zero selects a sensible default.
	PollInterval time.Duration
}

// Server serves the browser view of one project.
type Server struct {
	// mu guards the session, which is not safe for concurrent use. Every
	// handler takes it. The critical sections are a replay or a query over an
	// in-memory tree, so the contention this creates is not worth a more
	// elaborate scheme.
	mu   sync.Mutex
	sess *session.Session

	token string

	// version increments on every change, whether made here or by another
	// process. Browsers watch it to know when to refetch.
	version atomic.Uint64

	// watchers are the connected event streams.
	watchersMu sync.Mutex
	watchers   map[chan uint64]struct{}

	// fingerprint is the last observed state of the log files on disk, used to
	// notice writes made by the command line or another machine's sync.
	fingerprint string

	pollInterval time.Duration
	mux          *http.ServeMux
}

// New builds a server over an open session.
func New(s *session.Session, opts Options) (*Server, error) {
	token := opts.Token
	if token == "" {
		var err error
		if token, err = newToken(); err != nil {
			return nil, err
		}
	}

	interval := opts.PollInterval
	if interval == 0 {
		interval = time.Second
	}

	srv := &Server{
		sess:         s,
		token:        token,
		watchers:     make(map[chan uint64]struct{}),
		pollInterval: interval,
	}
	srv.fingerprint = store.Fingerprint(s.Project)
	srv.version.Store(1)
	srv.routes()

	return srv, nil
}

// Token returns the value a client must present.
func (s *Server) Token() string { return s.token }

// newToken mints an unguessable token.
//
// The token is what actually protects the project. Binding to loopback does
// not: any page the user has open can make requests to 127.0.0.1, and without
// a secret it could read and rewrite their notes. Because the token travels in
// a header the page reads from its own URL, a cross-origin script cannot
// obtain it, which also makes request forgery impossible.
func newToken() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate a token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// URL returns the address to open in a browser, token included.
func (s *Server) URL(addr string) string {
	return fmt.Sprintf("http://%s/?token=%s", addr, s.token)
}

// routes registers the handlers.
func (s *Server) routes() {
	mux := http.NewServeMux()

	static, err := fs.Sub(assets, "assets")
	if err != nil {
		panic("web: embedded assets are missing: " + err.Error())
	}
	mux.Handle("GET /", http.FileServerFS(static))

	// Reads.
	mux.Handle("GET /api/state", s.guard(s.handleState))
	mux.Handle("GET /api/node/{id}", s.guard(s.handleNode))
	mux.Handle("GET /api/events", s.guard(s.handleStream))

	// Writes.
	mux.Handle("POST /api/notebook", s.guard(s.handleNewNotebook))
	mux.Handle("POST /api/node", s.guard(s.handleNewNode))
	mux.Handle("PATCH /api/node/{id}", s.guard(s.handlePatchNode))
	mux.Handle("DELETE /api/node/{id}", s.guard(s.handleDeleteNode))
	mux.Handle("POST /api/node/{id}/restore", s.guard(s.handleRestoreNode))
	mux.Handle("POST /api/node/{id}/move", s.guard(s.handleMoveNode))
	mux.Handle("POST /api/node/{id}/link", s.guard(s.handleLinkNode))
	mux.Handle("POST /api/sync", s.guard(s.handleSync))

	s.mux = mux
}

// ServeHTTP satisfies http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// guard wraps an API handler with token and origin checks.
func (s *Server) guard(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.authorised(r) {
			http.Error(w, "unauthorised: open the URL gnotes printed, token included", http.StatusUnauthorized)
			return
		}
		// A browser sends Origin on any cross-site request. Rejecting a
		// mismatch costs nothing and stops a page on another origin from
		// reaching this one even if the token ever leaked.
		if origin := r.Header.Get("Origin"); origin != "" && !sameOrigin(origin, r.Host) {
			http.Error(w, "cross-origin requests are not accepted", http.StatusForbidden)
			return
		}
		h(w, r)
	})
}

// authorised reports whether the request carries the token, in a header or in
// the query string. The query form exists so that the initial page load and
// the event stream, which cannot set headers, still work.
func (s *Server) authorised(r *http.Request) bool {
	if bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		return constantTimeEqual(bearer, s.token)
	}
	if got := r.Header.Get("X-Gnotes-Token"); got != "" {
		return constantTimeEqual(got, s.token)
	}
	return constantTimeEqual(r.URL.Query().Get("token"), s.token)
}

// constantTimeEqual compares two tokens without leaking their contents through
// how long the comparison takes.
func constantTimeEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := 0; i < len(a); i++ {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}

// sameOrigin reports whether an Origin header refers to the host being served.
func sameOrigin(origin, host string) bool {
	trimmed := origin
	for _, scheme := range []string{"http://", "https://"} {
		trimmed = strings.TrimPrefix(trimmed, scheme)
	}
	return trimmed == host
}

// Serve listens and serves until the context of the caller ends the process.
// It returns the resolved address so a caller can print it before blocking.
func (s *Server) Serve(addr string) (net.Listener, error) {
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", addr, err)
	}
	return ln, nil
}

// Run serves on a listener and polls for outside changes until the listener is
// closed.
func (s *Server) Run(ln net.Listener) error {
	stop := make(chan struct{})
	defer close(stop)
	go s.watch(stop)

	srv := &http.Server{
		Handler: s,
		// A generous read timeout, but not none: the event stream is a
		// long-lived response, so only the request side is bounded.
		ReadHeaderTimeout: 10 * time.Second,
	}
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// watch polls the log files and reloads when another process has written.
//
// Polling rather than filesystem notifications: the set of files is small and
// changes rarely, the check is a handful of stat calls, and it avoids a
// dependency plus the platform differences that come with watching a directory
// that git may replace wholesale during a merge.
func (s *Server) watch(stop <-chan struct{}) {
	ticker := time.NewTicker(s.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			s.checkDisk()
		}
	}
}

// checkDisk reloads the session if the logs changed underneath it.
func (s *Server) checkDisk() {
	current := store.Fingerprint(s.sess.Project)

	s.mu.Lock()
	changed := current != s.fingerprint
	if changed {
		s.fingerprint = current
		if err := s.sess.Reload(); err != nil {
			// A transient read error during someone else's write is not worth
			// tearing the server down; the next tick will retry.
			s.mu.Unlock()
			return
		}
	}
	s.mu.Unlock()

	if changed {
		s.bump()
	}
}

// bump records a change and wakes every connected browser.
func (s *Server) bump() {
	v := s.version.Add(1)

	s.watchersMu.Lock()
	defer s.watchersMu.Unlock()

	for ch := range s.watchers {
		// Non-blocking: a browser that has fallen behind only needs to know
		// that something changed, not how many times.
		select {
		case ch <- v:
		default:
		}
	}
}

// touchDisk records the on-disk state after this server wrote, so its own
// write is not mistaken for an outside one on the next poll.
func (s *Server) touchDisk() {
	s.fingerprint = store.Fingerprint(s.sess.Project)
}

// subscribe registers a channel for change notifications.
func (s *Server) subscribe() chan uint64 {
	ch := make(chan uint64, 1)

	s.watchersMu.Lock()
	s.watchers[ch] = struct{}{}
	s.watchersMu.Unlock()

	return ch
}

// unsubscribe removes a channel.
func (s *Server) unsubscribe(ch chan uint64) {
	s.watchersMu.Lock()
	delete(s.watchers, ch)
	s.watchersMu.Unlock()
}

// handleStream pushes a line whenever the project changes, so the page updates
// without polling.
//
// Server-sent events rather than a websocket: the traffic is one-way and tiny,
// the browser reconnects on its own, and it is plain HTTP with no handshake,
// no framing and no dependency.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming is not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")

	ch := s.subscribe()
	defer s.unsubscribe(ch)

	// An immediate first message tells the page the stream is live and gives
	// it the version to compare against.
	fmt.Fprintf(w, "data: %d\n\n", s.version.Load())
	flusher.Flush()

	// A periodic comment keeps proxies and sleeping laptops from dropping an
	// idle connection.
	keepalive := time.NewTicker(25 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case v := <-ch:
			fmt.Fprintf(w, "data: %d\n\n", v)
			flusher.Flush()
		case <-keepalive.C:
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

// intParam reads an integer query parameter, returning fallback when absent or
// malformed.
func intParam(r *http.Request, name string, fallback int) int {
	v, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil {
		return fallback
	}
	return v
}
