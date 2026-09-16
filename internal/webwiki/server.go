// Package webwiki serves the wiki in a browser: the overview, the page tree,
// pages with their links and backlinks, search, and an editor.
//
// It is a front end over the same wiki package as the command line, the
// terminal interface and the agent server, so all of them agree on what a
// write means. The page is compiled into the binary, so there is nothing to
// install and it works with no network.
package webwiki

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shakfu/gwiki/internal/wiki"
)

// assets holds the page, its stylesheet and its script.
//
//go:embed assets
var assets embed.FS

// Options configure a server.
type Options struct {
	// Token authorises API requests. One is generated when this is empty.
	Token string

	// PollInterval is how often the pages are checked for outside changes.
	// Zero selects one second.
	PollInterval time.Duration
}

// Server serves one wiki.
type Server struct {
	// mu guards the wiki. Its queries are short, and a snapshot is not safe
	// for concurrent use.
	mu sync.Mutex
	w  *wiki.Wiki

	token string

	// version increments on every change, whether made here or outside.
	// Browsers watch it to know when to reload.
	version atomic.Uint64

	watchersMu sync.Mutex
	watchers   map[chan uint64]struct{}

	pollInterval time.Duration
	mux          *http.ServeMux
	anyHost      bool
}

// New builds a server over an open wiki.
func New(w *wiki.Wiki, opts Options) (*Server, error) {
	token := opts.Token
	if token == "" {
		buf := make([]byte, 16)
		if _, err := rand.Read(buf); err != nil {
			return nil, fmt.Errorf("generate a token: %w", err)
		}
		token = hex.EncodeToString(buf)
	}
	interval := opts.PollInterval
	if interval == 0 {
		interval = time.Second
	}
	s := &Server{w: w, token: token, watchers: map[chan uint64]struct{}{}, pollInterval: interval}
	s.version.Store(1)
	s.routes()
	return s, nil
}

// Token returns the value a client must present.
func (s *Server) Token() string { return s.token }

// URL is the address to open in a browser, token included.
func (s *Server) URL(addr string) string {
	return fmt.Sprintf("http://%s/?token=%s", addr, s.token)
}

func (s *Server) routes() {
	mux := http.NewServeMux()
	static, err := fs.Sub(assets, "assets")
	if err != nil {
		panic("webwiki: embedded assets are missing: " + err.Error())
	}
	mux.Handle("GET /", http.FileServerFS(static))

	mux.Handle("GET /api/overview", s.guard(s.handleOverview))
	mux.Handle("GET /api/pages", s.guard(s.handlePages))
	mux.Handle("GET /api/page", s.guard(s.handlePage))
	mux.Handle("GET /api/search", s.guard(s.handleSearch))
	mux.Handle("GET /api/check", s.guard(s.handleCheck))
	mux.Handle("GET /api/file", s.guard(s.handleFile))
	mux.Handle("GET /api/events", s.guard(s.handleStream))

	mux.Handle("POST /api/save", s.guard(s.handleSave))
	mux.Handle("POST /api/new", s.guard(s.handleNew))
	mux.Handle("POST /api/task", s.guard(s.handleTask))
	s.mux = mux
}

// ServeHTTP satisfies http.Handler.
//
// The Host check stops DNS rebinding: a page on another site whose name has
// been pointed at 127.0.0.1 reaches this server as same-origin, so the Origin
// check alone cannot tell it apart.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.anyHost && !loopbackHost(r.Host) {
		http.Error(w, "this server only answers on a loopback address", http.StatusMisdirectedRequest)
		return
	}
	h := w.Header()
	h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	s.mux.ServeHTTP(w, r)
}

func loopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// guard wraps an API handler with the token and origin checks.
func (s *Server) guard(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.authorised(r) {
			http.Error(w, "unauthorised: open the URL gwiki printed, token included", http.StatusUnauthorized)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && !sameOrigin(origin, r.Host) {
			http.Error(w, "cross-origin requests are not accepted", http.StatusForbidden)
			return
		}
		h(w, r)
	})
}

// authorised reports whether the request carries the token. It travels in the
// Authorization header, except for the event stream, which a browser opens
// without headers.
func (s *Server) authorised(r *http.Request) bool {
	if bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		return tokenEqual(bearer, s.token)
	}
	if r.Method == http.MethodGet && r.URL.Path == "/api/events" {
		return tokenEqual(r.URL.Query().Get("token"), s.token)
	}
	return false
}

func tokenEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func sameOrigin(origin, host string) bool {
	trimmed := origin
	for _, scheme := range []string{"http://", "https://"} {
		trimmed = strings.TrimPrefix(trimmed, scheme)
	}
	return trimmed == host
}

// Serve opens the listener, defaulting to a loopback address.
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

// Run serves until the listener closes, polling the pages for changes.
func (s *Server) Run(ln net.Listener) error {
	if addr, ok := ln.Addr().(*net.TCPAddr); ok && !addr.IP.IsLoopback() {
		s.anyHost = true
	}
	stop := make(chan struct{})
	defer close(stop)
	go s.watch(stop)

	srv := &http.Server{Handler: s, ReadHeaderTimeout: 10 * time.Second}
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

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

// checkDisk refreshes the cache and wakes the browsers when pages changed.
func (s *Server) checkDisk() {
	s.mu.Lock()
	ch, err := s.w.Refresh()
	s.mu.Unlock()
	if err == nil && !ch.Empty() {
		s.bump()
	}
}

// bump records a change and wakes every connected browser.
func (s *Server) bump() {
	v := s.version.Add(1)
	s.watchersMu.Lock()
	defer s.watchersMu.Unlock()
	for ch := range s.watchers {
		select {
		case ch <- v:
		default:
		}
	}
}

func (s *Server) subscribe() chan uint64 {
	ch := make(chan uint64, 1)
	s.watchersMu.Lock()
	s.watchers[ch] = struct{}{}
	s.watchersMu.Unlock()
	return ch
}

func (s *Server) unsubscribe(ch chan uint64) {
	s.watchersMu.Lock()
	delete(s.watchers, ch)
	s.watchersMu.Unlock()
}

// handleStream pushes the version whenever the wiki changes, so the page
// updates without polling.
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

	fmt.Fprintf(w, "data: %d\n\n", s.version.Load())
	flusher.Flush()

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
