package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/shakfu/gnotes/internal/event"
	"github.com/shakfu/gnotes/internal/gitsync"
	"github.com/shakfu/gnotes/internal/rank"
	"github.com/shakfu/gnotes/internal/search"
	"github.com/shakfu/gnotes/internal/state"
	"github.com/shakfu/gnotes/internal/ulid"
)

// refLen is how many trailing characters of an id the page shows as a handle,
// matching what the command line prints so the two can be used together.
const refLen = 6

// nodeJSON is the wire shape of a note, task or notebook.
//
// A type of its own rather than tags on state.Node: this is an interface the
// page is written against, and it should not shift every time an internal
// field changes.
type nodeJSON struct {
	ID       string `json:"id"`
	Ref      string `json:"ref"`
	Kind     string `json:"kind"`
	Title    string `json:"title"`
	Body     string `json:"body,omitempty"`
	Notebook string `json:"notebook,omitempty"`

	Tags  []string   `json:"tags"`
	Links []linkJSON `json:"links,omitempty"`

	Status   string `json:"status,omitempty"`
	Priority string `json:"priority,omitempty"`
	Due      string `json:"due,omitempty"`
	Overdue  bool   `json:"overdue,omitempty"`

	Assignees []string `json:"assignees,omitempty"`
	Deleted   bool     `json:"deleted,omitempty"`

	Created   string `json:"created"`
	Updated   string `json:"updated"`
	CreatedBy string `json:"createdBy,omitempty"`
	UpdatedBy string `json:"updatedBy,omitempty"`

	// Snippet is the matching fragment of a body, present only in search
	// results, so the page can show why something matched.
	Snippet string `json:"snippet,omitempty"`
}

// linkJSON is one end of a cross-reference. A link may point at a node that
// has not synced yet, which the page renders differently rather than hiding.
type linkJSON struct {
	ID      string `json:"id"`
	Ref     string `json:"ref"`
	Title   string `json:"title,omitempty"`
	Pending bool   `json:"pending,omitempty"`
}

// notebookJSON is a notebook with the counts the sidebar shows.
type notebookJSON struct {
	ID      string `json:"id"`
	Ref     string `json:"ref"`
	Title   string `json:"title"`
	Entries int    `json:"entries"`
	Open    int    `json:"open"`
}

// stateJSON is everything one page render needs, in one response. A page that
// had to make four requests to draw itself would flicker through four
// intermediate states.
type stateJSON struct {
	Project      string         `json:"project"`
	Location     string         `json:"location"`
	Version      uint64         `json:"version"`
	Me           string         `json:"me"`
	Notebooks    []notebookJSON `json:"notebooks"`
	Entries      []nodeJSON     `json:"entries"`
	Tags         []tagJSON      `json:"tags"`
	Contributors []string       `json:"contributors"`
	Counts       countsJSON     `json:"counts"`
	Problems     []string       `json:"problems,omitempty"`
}

type tagJSON struct {
	Tag   string `json:"tag"`
	Count int    `json:"count"`
}

type countsJSON struct {
	Notebooks int `json:"notebooks"`
	Notes     int `json:"notes"`
	Tasks     int `json:"tasks"`
	Open      int `json:"open"`
	Doing     int `json:"doing"`
	Done      int `json:"done"`
	Overdue   int `json:"overdue"`
}

// toNode converts a node for the wire.
func toNode(s *state.State, n *state.Node, now time.Time, includeBody bool) nodeJSON {
	j := nodeJSON{
		ID:        n.ID,
		Ref:       ulid.Short(n.ID, refLen),
		Kind:      n.Kind.String(),
		Title:     n.Title,
		Tags:      n.Tags,
		Deleted:   n.Deleted,
		Created:   n.Created.UTC().Format(time.RFC3339),
		Updated:   n.Updated.UTC().Format(time.RFC3339),
		CreatedBy: s.Contributor(n.CreatedBy),
		UpdatedBy: s.Contributor(n.UpdatedBy),
	}
	// Tags is never null, so the page can iterate without a guard.
	if j.Tags == nil {
		j.Tags = []string{}
	}
	if includeBody {
		j.Body = n.Body
	}
	if parent := s.Get(n.Parent); parent != nil {
		j.Notebook = parent.ID
	}

	for _, id := range n.Links {
		l := linkJSON{ID: id, Ref: ulid.Short(id, refLen)}
		if target := s.Get(id); target != nil {
			l.Title = target.Title
		} else {
			l.Pending = true
		}
		j.Links = append(j.Links, l)
	}

	if n.Kind == state.KindTask {
		j.Status = n.Status.String()
		j.Priority = n.Priority.String()
		j.Due = state.FormatDue(n.Due)
		j.Overdue = n.Overdue(now)
		for _, id := range n.Assignees {
			j.Assignees = append(j.Assignees, s.Contributor(id))
		}
	}
	return j
}

// handleState returns the whole view, filtered by the query string.
func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	st := s.sess.State
	now := s.sess.Now()
	q := r.URL.Query()

	out := stateJSON{
		Project:  s.sess.Project.Config.Name,
		Location: s.sess.Project.Dir,
		Version:  s.version.Load(),
		Me:       s.sess.Actor.Name,
	}

	for _, nb := range st.Notebooks() {
		entry := notebookJSON{ID: nb.ID, Ref: ulid.Short(nb.ID, refLen), Title: nb.Title}
		for _, k := range st.Children(nb.ID) {
			entry.Entries++
			if k.Kind == state.KindTask && k.Status != state.StatusDone {
				entry.Open++
			}
		}
		out.Notebooks = append(out.Notebooks, entry)
	}
	if out.Notebooks == nil {
		out.Notebooks = []notebookJSON{}
	}

	filter, order, err := parseFilter(st, q, s.sess.Actor.ID, now)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	query := strings.TrimSpace(q.Get("q"))
	var nodes []*state.Node

	if query != "" {
		// A search spans the project. Not knowing which notebook something is
		// in is the usual reason to search for it, so the notebook constraint
		// is deliberately dropped.
		filter.Notebook = ""
		index := search.Build(st.List(state.Filter{IncludeDeleted: filter.IncludeDeleted}, state.OrderRank))

		for _, res := range index.Search(query, intParam(r, "limit", 200)) {
			if matches(res.Node, filter, now) {
				nodes = append(nodes, res.Node)
			}
		}
	} else {
		nodes = st.List(filter, order)
	}

	out.Entries = make([]nodeJSON, 0, len(nodes))
	for _, n := range nodes {
		item := toNode(st, n, now, false)
		if query != "" {
			item.Snippet = search.Snippet(n, query, 160)
		}
		out.Entries = append(out.Entries, item)
	}

	for _, tc := range st.Tags() {
		out.Tags = append(out.Tags, tagJSON{Tag: tc.Tag, Count: tc.Count})
	}
	if out.Tags == nil {
		out.Tags = []tagJSON{}
	}

	for _, c := range st.Contributors {
		out.Contributors = append(out.Contributors, c.Name)
	}

	c := st.Summary(now)
	out.Counts = countsJSON{c.Notebooks, c.Notes, c.Tasks, c.Open, c.Doing, c.Done, c.Overdue}

	for _, p := range s.sess.Problems {
		out.Problems = append(out.Problems, p.String())
	}

	writeJSON(w, out)
}

// parseFilter reads the listing parameters from a query string.
func parseFilter(st *state.State, q map[string][]string, me string, now time.Time) (state.Filter, state.Order, error) {
	get := func(name string) string {
		if v := q[name]; len(v) > 0 {
			return strings.TrimSpace(v[0])
		}
		return ""
	}

	f := state.Filter{Now: now, Tags: q["tag"]}
	f.Overdue = get("overdue") == "1"
	f.IncludeDeleted = get("deleted") == "1"

	if nb := get("notebook"); nb != "" {
		node, err := st.Resolve(nb, state.KindNotebook)
		if err != nil {
			return f, 0, err
		}
		f.Notebook = node.ID
	}
	if k := get("kind"); k != "" {
		kind, ok := state.ParseKind(k)
		if !ok {
			return f, 0, fmt.Errorf("unknown kind %q", k)
		}
		f.Kinds = []state.Kind{kind}
	}
	if v := get("status"); v != "" {
		status, ok := state.ParseStatus(v)
		if !ok {
			return f, 0, fmt.Errorf("unknown status %q", v)
		}
		f.Status = &status
	}
	if v := get("priority"); v != "" {
		p, ok := state.ParsePriority(v)
		if !ok {
			return f, 0, fmt.Errorf("unknown priority %q", v)
		}
		f.Priority = &p
	}
	if v := get("assignee"); v != "" {
		if strings.EqualFold(v, "me") {
			f.Assignee = me
		} else if id, ok := st.FindContributor(v); ok {
			f.Assignee = id
		} else {
			return f, 0, fmt.Errorf("nobody named %q in this project", v)
		}
	}

	order, ok := state.ParseOrder(get("sort"))
	if !ok {
		return f, 0, fmt.Errorf("unknown sort %q", get("sort"))
	}
	return f, order, nil
}

// matches applies a filter to a search result, which does not go through
// State.List.
func matches(n *state.Node, f state.Filter, now time.Time) bool {
	if n.Deleted && !f.IncludeDeleted {
		return false
	}
	if len(f.Kinds) > 0 && f.Kinds[0] != n.Kind {
		return false
	}
	if f.Status != nil && (n.Kind != state.KindTask || n.Status != *f.Status) {
		return false
	}
	if f.Priority != nil && (n.Kind != state.KindTask || n.Priority != *f.Priority) {
		return false
	}
	if f.Overdue && !n.Overdue(now) {
		return false
	}
	for _, tag := range f.Tags {
		if !n.HasTag(state.NormalizeTag(tag)) {
			return false
		}
	}
	if f.Assignee != "" {
		found := false
		for _, a := range n.Assignees {
			if a == f.Assignee {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// detailJSON is one node with everything the detail pane shows.
type detailJSON struct {
	Node      nodeJSON    `json:"node"`
	Path      []string    `json:"path"`
	Backlinks []linkJSON  `json:"backlinks"`
	History   []eventJSON `json:"history"`
}

// eventJSON is one log entry that touched a node.
//
// The page shows these because they are what a note actually is: gnotes stores
// no record of a note, only the events that add up to one. Every other view
// hides that; this is the one place it is worth showing.
type eventJSON struct {
	Ref    string `json:"ref"`
	Action string `json:"action"`
	Detail string `json:"detail,omitempty"`
	At     string `json:"at"`
	By     string `json:"by"`
}

// history returns the events that touched a node, oldest first. It is never
// nil, so the page can iterate it without a guard.
func (s *Server) history(n *state.Node) []eventJSON {
	out := []eventJSON{}

	for _, e := range s.sess.Log() {
		p := e.Payload
		if p.ID != n.ID && p.Parent != n.ID && p.Target != n.ID {
			continue
		}
		at, err := ulid.Timestamp(e.ID)
		if err != nil {
			continue
		}
		out = append(out, eventJSON{
			Ref:    ulid.Short(e.ID, refLen),
			Action: string(e.Action),
			Detail: describe(s.sess.State, p, n.Title),
			At:     at.UTC().Format(time.RFC3339),
			By:     s.sess.State.Contributor(e.UserID),
		})
	}
	return out
}

// describe summarises the value an event set, skipping anything the row
// already says.
func describe(st *state.State, p event.Payload, subject string) string {
	var parts []string

	for _, v := range []string{p.Name, p.Title, p.Status, p.Priority, p.Due, p.Tag} {
		if v != "" && v != subject {
			parts = append(parts, v)
		}
	}
	if p.Body != "" {
		parts = append(parts, fmt.Sprintf("%d bytes", len(p.Body)))
	}
	if p.Assignee != "" {
		parts = append(parts, st.Contributor(p.Assignee))
	}
	if p.Target != "" {
		if target := st.Get(p.Target); target != nil {
			parts = append(parts, target.Title)
		} else {
			parts = append(parts, ulid.Short(p.Target, refLen))
		}
	}
	return strings.Join(parts, " ")
}

// handleNode returns one node in full.
func (s *Server) handleNode(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	n, err := s.sess.State.Resolve(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}

	out := detailJSON{
		Node:      toNode(s.sess.State, n, s.sess.Now(), true),
		Path:      s.sess.State.Path(n),
		Backlinks: []linkJSON{},
		History:   s.history(n),
	}
	for _, b := range s.sess.State.Backlinks(n.ID) {
		out.Backlinks = append(out.Backlinks, linkJSON{ID: b.ID, Ref: ulid.Short(b.ID, refLen), Title: b.Title})
	}
	writeJSON(w, out)
}

// mutate runs a write under the lock, commits it, and reports the change.
//
// Every mutating handler goes through it, so none can forget to commit, to
// refresh the on-disk fingerprint, or to wake the connected browsers.
func (s *Server) mutate(w http.ResponseWriter, action func() (any, error)) {
	s.mu.Lock()

	result, err := action()
	if err != nil {
		// Nothing reached disk, but the in-memory tree may hold events staged
		// before the failure, so it is reloaded rather than left half-applied.
		_ = s.sess.Reload()
		s.mu.Unlock()
		writeError(w, http.StatusBadRequest, err)
		return
	}

	if err := s.sess.Commit(); err != nil {
		s.mu.Unlock()
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.touchDisk()
	s.mu.Unlock()

	s.bump()
	writeJSON(w, result)
}

// newNodeRequest creates a notebook, note or task.
type newNodeRequest struct {
	Kind     string   `json:"kind"`
	Notebook string   `json:"notebook"`
	Title    string   `json:"title"`
	Body     string   `json:"body"`
	Tags     []string `json:"tags"`
	Due      string   `json:"due"`
	Priority string   `json:"priority"`
}

// handleNewNotebook creates a notebook.
func (s *Server) handleNewNotebook(w http.ResponseWriter, r *http.Request) {
	var req newNodeRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	s.mutate(w, func() (any, error) {
		name := req.Title
		if name == "" {
			name = req.Notebook
		}
		nb, err := s.sess.NewNotebook(name)
		if err != nil {
			return nil, err
		}
		return toNode(s.sess.State, nb, s.sess.Now(), false), nil
	})
}

// handleNewNode creates a note or a task.
func (s *Server) handleNewNode(w http.ResponseWriter, r *http.Request) {
	var req newNodeRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	s.mutate(w, func() (any, error) {
		notebook := req.Notebook
		if notebook == "" {
			nb, err := s.sess.DefaultNotebook()
			if err != nil {
				return nil, err
			}
			notebook = nb.ID
		}

		var n *state.Node
		var err error
		if req.Kind == "task" {
			n, err = s.sess.NewTask(notebook, req.Title, req.Body)
		} else {
			n, err = s.sess.NewNote(notebook, req.Title, req.Body)
		}
		if err != nil {
			return nil, err
		}

		for _, tag := range req.Tags {
			if err := s.sess.AddTag(n, tag); err != nil {
				return nil, err
			}
		}
		if req.Due != "" {
			if err := s.sess.SetDue(n, req.Due); err != nil {
				return nil, err
			}
		}
		if req.Priority != "" {
			p, ok := state.ParsePriority(req.Priority)
			if !ok {
				return nil, fmt.Errorf("unknown priority %q", req.Priority)
			}
			if err := s.sess.SetPriority(n, p); err != nil {
				return nil, err
			}
		}
		return toNode(s.sess.State, n, s.sess.Now(), true), nil
	})
}

// patchRequest changes fields of an existing node. Every field is a pointer so
// that "set this to empty" is distinguishable from "leave this alone", which
// matters for clearing a due date or a body.
type patchRequest struct {
	Title     *string `json:"title"`
	Body      *string `json:"body"`
	Status    *string `json:"status"`
	Priority  *string `json:"priority"`
	Due       *string `json:"due"`
	AddTag    *string `json:"addTag"`
	RemoveTag *string `json:"removeTag"`
	Assign    *string `json:"assign"`
	Unassign  *string `json:"unassign"`
}

// handlePatchNode applies whichever fields the request carries.
func (s *Server) handlePatchNode(w http.ResponseWriter, r *http.Request) {
	var req patchRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	id := r.PathValue("id")

	s.mutate(w, func() (any, error) {
		n, err := s.sess.State.Resolve(id)
		if err != nil {
			return nil, err
		}

		if req.Title != nil {
			if err := s.sess.SetTitle(n, *req.Title); err != nil {
				return nil, err
			}
		}
		if req.Body != nil {
			if err := s.sess.SetBody(n, *req.Body); err != nil {
				return nil, err
			}
		}
		if req.Status != nil {
			status, ok := state.ParseStatus(*req.Status)
			if !ok {
				return nil, fmt.Errorf("unknown status %q", *req.Status)
			}
			if err := s.sess.SetStatus(n, status); err != nil {
				return nil, err
			}
		}
		if req.Priority != nil {
			p, ok := state.ParsePriority(*req.Priority)
			if !ok {
				return nil, fmt.Errorf("unknown priority %q", *req.Priority)
			}
			if err := s.sess.SetPriority(n, p); err != nil {
				return nil, err
			}
		}
		if req.Due != nil {
			if err := s.sess.SetDue(n, *req.Due); err != nil {
				return nil, err
			}
		}
		if req.AddTag != nil {
			if err := s.sess.AddTag(n, *req.AddTag); err != nil {
				return nil, err
			}
		}
		if req.RemoveTag != nil {
			if err := s.sess.RemoveTag(n, *req.RemoveTag); err != nil {
				return nil, err
			}
		}
		if req.Assign != nil {
			if err := s.sess.Assign(n, *req.Assign); err != nil {
				return nil, err
			}
		}
		if req.Unassign != nil {
			if err := s.sess.Unassign(n, *req.Unassign); err != nil {
				return nil, err
			}
		}
		return toNode(s.sess.State, n, s.sess.Now(), true), nil
	})
}

// handleDeleteNode tombstones a node.
func (s *Server) handleDeleteNode(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	s.mutate(w, func() (any, error) {
		n, err := s.sess.State.Resolve(id)
		if err != nil {
			return nil, err
		}
		if err := s.sess.Delete(n); err != nil {
			return nil, err
		}
		// The id goes back so the page can offer an undo.
		return map[string]string{"id": n.ID, "title": n.Title}, nil
	})
}

// handleRestoreNode undoes a deletion.
func (s *Server) handleRestoreNode(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	s.mutate(w, func() (any, error) {
		if err := s.sess.Restore(ulid.Canonical(id)); err != nil {
			return nil, err
		}
		return map[string]string{"id": id}, nil
	})
}

// moveRequest reparents or reorders a node.
type moveRequest struct {
	Notebook string `json:"notebook"`
	Position string `json:"position"`
	Sibling  string `json:"sibling"`
}

// handleMoveNode moves a node between notebooks or within one.
func (s *Server) handleMoveNode(w http.ResponseWriter, r *http.Request) {
	var req moveRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	id := r.PathValue("id")

	s.mutate(w, func() (any, error) {
		n, err := s.sess.State.Resolve(id)
		if err != nil {
			return nil, err
		}

		pos := rank.End()
		switch req.Position {
		case "start", "top":
			pos = rank.Start()
		case "before":
			sib, err := s.sess.State.Resolve(req.Sibling)
			if err != nil {
				return nil, err
			}
			pos = rank.Before(sib.ID)
		case "after":
			sib, err := s.sess.State.Resolve(req.Sibling)
			if err != nil {
				return nil, err
			}
			pos = rank.After(sib.ID)
		}

		if err := s.sess.Move(n, req.Notebook, pos); err != nil {
			return nil, err
		}
		return toNode(s.sess.State, n, s.sess.Now(), false), nil
	})
}

// linkRequest points one node at another.
type linkRequest struct {
	Target string `json:"target"`
	Remove bool   `json:"remove"`
}

// handleLinkNode adds or removes a cross-reference.
func (s *Server) handleLinkNode(w http.ResponseWriter, r *http.Request) {
	var req linkRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	id := r.PathValue("id")

	s.mutate(w, func() (any, error) {
		from, err := s.sess.State.Resolve(id)
		if err != nil {
			return nil, err
		}
		if req.Remove {
			target, err := s.sess.State.Resolve(req.Target)
			if err != nil {
				return nil, err
			}
			return map[string]string{"id": from.ID}, s.sess.Unlink(from, target.ID)
		}

		to, err := s.sess.State.Resolve(req.Target)
		if err != nil {
			return nil, err
		}
		return map[string]string{"id": from.ID}, s.sess.Link(from, to)
	})
}

// syncRequest asks for a git sync.
type syncRequest struct {
	Push bool `json:"push"`
}

// handleSync commits the logs and optionally exchanges with the remote.
func (s *Server) handleSync(w http.ResponseWriter, r *http.Request) {
	var req syncRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	s.mu.Lock()
	message := fmt.Sprintf("gnotes: %d events", len(s.sess.Log()))
	project := s.sess.Project
	s.mu.Unlock()

	// Outside the lock: a push contacts the network and can take seconds, and
	// holding the session for that long would stall every other request.
	res, err := gitsync.Sync(project, message, req.Push)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	// A pull may have brought in another machine's events.
	s.mu.Lock()
	if err := s.sess.Reload(); err != nil {
		s.mu.Unlock()
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.touchDisk()
	s.mu.Unlock()
	s.bump()

	writeJSON(w, map[string]any{
		"committed": res.Committed,
		"pulled":    res.Pulled,
		"pushed":    res.Pushed,
		"branch":    res.Branch,
	})
}

// decode reads a JSON request body. An empty body is accepted as an empty
// object, so a request with no options need not send one.
func decode(r *http.Request, into any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<22))
	dec.DisallowUnknownFields()

	if err := dec.Decode(into); err != nil {
		if err.Error() == "EOF" {
			return nil
		}
		return fmt.Errorf("malformed request: %w", err)
	}
	return nil
}

// writeJSON sends a value as JSON.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	// The page is the only consumer and it is served from here, so there is
	// nothing to cache and every response reflects a mutable tree.
	w.Header().Set("Cache-Control", "no-store")

	if err := json.NewEncoder(w).Encode(v); err != nil {
		// The status is already written by now, so there is nowhere to report
		// this except the connection, which is already broken.
		return
	}
}

// writeError sends a failure the page can display verbatim.
func writeError(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}
