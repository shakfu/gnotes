package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shakfu/gnotes/internal/rank"
	"github.com/shakfu/gnotes/internal/search"
	"github.com/shakfu/gnotes/internal/state"
	"github.com/shakfu/gnotes/internal/ulid"
)

// refLen matches the handle length the command line prints, so a handle read
// from one interface works in the other.
const refLen = 6

// tool is one entry in the tools/list response.
type tool struct {
	Name        string       `json:"name"`
	Title       string       `json:"title,omitempty"`
	Description string       `json:"description"`
	InputSchema object       `json:"inputSchema"`
	Annotations *annotations `json:"annotations,omitempty"`
}

// annotations are hints about a tool's effects. Clients use them to decide what
// to confirm with the user, so they describe consequences honestly: nothing
// here is marked read-only unless it truly writes nothing.
type annotations struct {
	Title           string `json:"title,omitempty"`
	ReadOnlyHint    bool   `json:"readOnlyHint,omitempty"`
	DestructiveHint *bool  `json:"destructiveHint,omitempty"`
	IdempotentHint  bool   `json:"idempotentHint,omitempty"`
	OpenWorldHint   *bool  `json:"openWorldHint,omitempty"`
}

// object is a JSON Schema fragment.
type object map[string]any

// props builds an object schema. Naming the required fields separately keeps
// each property's description next to the property it describes.
func props(required []string, properties object) object {
	schema := object{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	// The tools here take a fixed set of arguments; refusing unknown ones turns
	// a model's typo into a clear error rather than a silently ignored field.
	schema["additionalProperties"] = false
	return schema
}

// str builds a string property.
func str(description string) object {
	return object{"type": "string", "description": description}
}

// enum builds a string property constrained to a fixed set.
func enum(description string, values ...string) object {
	return object{"type": "string", "description": description, "enum": values}
}

func boolean(description string) object {
	return object{"type": "boolean", "description": description}
}

func strList(description string) object {
	return object{"type": "array", "items": object{"type": "string"}, "description": description}
}

func ptr[T any](v T) *T { return &v }

// handler runs a tool and returns the text the model reads.
//
// An error becomes a tool execution error (isError on a successful result),
// not a protocol error: the model can read it, understand what went wrong, and
// try something else. Protocol errors are reserved for a call that should never
// have been made at all, such as an unknown tool name.
type handler func(s *Server, args json.RawMessage) (string, error)

// registry is every tool, in the order tools/list returns them.
var registry = []struct {
	tool
	run handler
}{
	// ------------------------------------------------------------ reading

	{
		tool: tool{
			Name:  "gnotes_list",
			Title: "List notes and tasks",
			Description: `List the project's notes and tasks, or its notebooks.

Call this to see what exists before creating something that may already be
there, to find the notebook to put a new entry in, or to answer a question about
what is outstanding. Filters combine, so asking for open high-priority tasks
tagged "bug" returns only entries matching all three.

Returns each entry's handle, kind, title and set fields. Bodies are omitted;
use gnotes_get for one entry in full.`,
			Annotations: &annotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: ptr(false)},
			InputSchema: props(nil, object{
				"kind":     enum("Restrict to one kind. Omit to list notes and tasks together; pass \"notebook\" to list notebooks instead.", "note", "task", "notebook"),
				"notebook": str("Restrict to one notebook, named by handle or title."),
				"status":   enum("Restrict to tasks with this status. Notes have no status, so this excludes them.", "open", "doing", "done"),
				"priority": enum("Restrict to tasks with this priority.", "none", "low", "normal", "high"),
				"tags":     strList("Restrict to entries carrying every one of these tags."),
				"overdue":  boolean("Restrict to unfinished tasks whose due date has passed."),
				"text":     str("Restrict to entries whose title contains this text. To search bodies as well, use gnotes_search."),
				"sort":     enum("Ordering. Defaults to the arrangement the user chose.", "rank", "created", "updated", "title", "due", "priority"),
				"limit":    object{"type": "integer", "description": "Maximum entries to return. Defaults to 50."},
			}),
		},
		run: (*Server).listEntries,
	},

	{
		tool: tool{
			Name:  "gnotes_search",
			Title: "Search note and task text",
			Description: `Search the full text of every note and task, including bodies.

Call this when looking for something by what it says rather than by where it
lives — a topic, a phrase, a name mentioned in passing. Prefer it over
gnotes_list whenever the notebook is unknown, which is the usual case.

All words must match, and results are ranked with title matches above body
matches. Each result includes the fragment of the body that matched.`,
			Annotations: &annotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: ptr(false)},
			InputSchema: props([]string{"query"}, object{
				"query": str("Words to search for. All of them must appear in a matching entry."),
				"limit": object{"type": "integer", "description": "Maximum results to return. Defaults to 20."},
			}),
		},
		run: (*Server).searchEntries,
	},

	{
		tool: tool{
			Name:  "gnotes_get",
			Title: "Read one entry in full",
			Description: `Read one note or task in full: its body, every field, the entries it
references, the entries referencing it, and the events that produced it.

Call this after gnotes_list or gnotes_search has narrowed things to one entry,
or whenever the user names a specific note. This is the only tool that returns
body text.`,
			Annotations: &annotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: ptr(false)},
			InputSchema: props([]string{"ref"}, object{
				"ref":     str("The entry's six-character handle, its title, or a distinctive fragment of the title."),
				"history": boolean("Include the events that produced this entry. Defaults to false."),
			}),
		},
		run: (*Server).getEntry,
	},

	// ------------------------------------------------------------ writing

	{
		tool: tool{
			Name:  "gnotes_create",
			Title: "Create a note, task or notebook",
			Description: `Create a note, a task, or a notebook.

Use a task when the entry is something to be done and its state matters; use a
note for anything else. The distinction is enforced: only tasks accept a status,
priority, due date or assignee.

Without a notebook the entry goes to the first one, creating "inbox" if the
project has none. Returns the new entry's handle.`,
			Annotations: &annotations{DestructiveHint: ptr(false), OpenWorldHint: ptr(false)},
			InputSchema: props([]string{"kind", "title"}, object{
				"kind":     enum("What to create.", "note", "task", "notebook"),
				"title":    str("The entry's title, or the notebook's name."),
				"body":     str("Markdown body. Notes and tasks only."),
				"notebook": str("Which notebook to create it in, by handle or title. Ignored when creating a notebook."),
				"tags":     strList("Tags to attach. Normalised to lowercase with hyphens for spaces."),
				"due":      str("Due date for a task. Accepts a date such as 2026-09-01, a weekday, or \"tomorrow\"."),
				"priority": enum("Priority for a task.", "none", "low", "normal", "high"),
			}),
		},
		run: (*Server).createEntry,
	},

	{
		tool: tool{
			Name:  "gnotes_update",
			Title: "Change an entry",
			Description: `Change an existing note or task: its title, body, task fields, tags,
notebook, or the entries it references.

Only the fields you pass are changed; everything omitted is left alone. Setting
"body" replaces the whole body, so read the entry with gnotes_get first when
appending rather than rewriting.

Marking a task done is the common case and needs only "ref" and "status".`,
			Annotations: &annotations{DestructiveHint: ptr(false), OpenWorldHint: ptr(false)},
			InputSchema: props([]string{"ref"}, object{
				"ref":        str("The entry to change, by handle or title."),
				"title":      str("Replace the title."),
				"body":       str("Replace the entire body."),
				"status":     enum("Set a task's status.", "open", "doing", "done"),
				"priority":   enum("Set a task's priority.", "none", "low", "normal", "high"),
				"due":        str("Set a task's due date, or \"none\" to clear it."),
				"addTags":    strList("Tags to attach."),
				"removeTags": strList("Tags to detach."),
				"notebook":   str("Move the entry to this notebook, by handle or title."),
				"link":       str("Record a reference from this entry to another, by handle or title."),
				"unlink":     str("Remove a reference from this entry to another."),
			}),
		},
		run: (*Server).updateEntry,
	},

	{
		tool: tool{
			Name:  "gnotes_delete",
			Title: "Delete an entry",
			Description: `Delete a note, task or notebook.

Deleting a notebook deletes everything in it. The entry is marked deleted
rather than removed, so gnotes_restore undoes it — but it disappears from every
listing immediately, so confirm with the user before deleting anything they did not
explicitly ask you to remove.`,
			Annotations: &annotations{DestructiveHint: ptr(true), OpenWorldHint: ptr(false)},
			InputSchema: props([]string{"ref"}, object{
				"ref": str("The entry to delete, by handle or title."),
			}),
		},
		run: (*Server).deleteEntry,
	},

	{
		tool: tool{
			Name:  "gnotes_restore",
			Title: "Undo a deletion",
			Description: `Restore a previously deleted entry, bringing back its contents and, for a
notebook, everything that was in it.

Call this to undo a gnotes_delete. The handle is unchanged by deletion, so pass
the same one.`,
			Annotations: &annotations{DestructiveHint: ptr(false), IdempotentHint: true, OpenWorldHint: ptr(false)},
			InputSchema: props([]string{"ref"}, object{
				"ref": str("The handle of the deleted entry."),
			}),
		},
		run: (*Server).restoreEntry,
	},
}

// tools returns the tool definitions for tools/list.
func (s *Server) tools() []tool {
	out := make([]tool, len(registry))
	for i, entry := range registry {
		out[i] = entry.tool
	}
	return out
}

// callParams is the tools/call payload.
type callParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// callTool dispatches one tool invocation.
func (s *Server) callTool(raw json.RawMessage) (any, error) {
	var p callParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: "malformed tools/call params: " + err.Error()}
	}

	for _, entry := range registry {
		if entry.Name != p.Name {
			continue
		}

		// Another front end may have written since the last call.
		if err := s.refresh(); err != nil {
			return textResult("could not read the project: "+err.Error(), true), nil
		}

		text, err := entry.run(s, p.Arguments)
		if err != nil {
			// A tool that failed partway may have staged events already. The
			// server outlives the call, so dropping them here is what keeps
			// them out of the next tool's commit.
			s.sess.Rollback()

			// A failed operation is a result the model can read and react to,
			// not a protocol fault.
			return textResult(err.Error(), true), nil
		}
		return textResult(text, false), nil
	}

	// An unknown tool is a client mistake rather than a failed operation.
	return nil, &rpcError{Code: codeInvalidParams, Message: "no tool named " + p.Name}
}

// textResult builds a tools/call result carrying one block of text.
func textResult(text string, isError bool) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isError,
	}
}

// decodeArgs unmarshals a tool's arguments. Absent arguments are an empty
// object, so a tool whose fields are all optional can be called with none.
func decodeArgs(raw json.RawMessage, into any) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()

	if err := dec.Decode(into); err != nil {
		return fmt.Errorf("bad arguments: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------- reading

type listArgs struct {
	Kind     string   `json:"kind"`
	Notebook string   `json:"notebook"`
	Status   string   `json:"status"`
	Priority string   `json:"priority"`
	Tags     []string `json:"tags"`
	Overdue  bool     `json:"overdue"`
	Text     string   `json:"text"`
	Sort     string   `json:"sort"`
	Limit    int      `json:"limit"`
}

func (s *Server) listEntries(raw json.RawMessage) (string, error) {
	var a listArgs
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}

	st := s.sess.State
	now := s.sess.Now()

	if a.Kind == "notebook" {
		return s.renderNotebooks(), nil
	}

	f := state.Filter{Tags: a.Tags, Text: a.Text, Overdue: a.Overdue, Now: now}

	if a.Kind != "" {
		kind, ok := state.ParseKind(a.Kind)
		if !ok {
			return "", fmt.Errorf("unknown kind %q; use note, task or notebook", a.Kind)
		}
		f.Kinds = []state.Kind{kind}
	}
	if a.Notebook != "" {
		nb, err := st.Resolve(a.Notebook, state.KindNotebook)
		if err != nil {
			return "", err
		}
		f.Notebook = nb.ID
	}
	if a.Status != "" {
		status, ok := state.ParseStatus(a.Status)
		if !ok {
			return "", fmt.Errorf("unknown status %q; use open, doing or done", a.Status)
		}
		f.Status = &status
	}
	if a.Priority != "" {
		p, ok := state.ParsePriority(a.Priority)
		if !ok {
			return "", fmt.Errorf("unknown priority %q; use none, low, normal or high", a.Priority)
		}
		f.Priority = &p
	}

	order, ok := state.ParseOrder(a.Sort)
	if !ok {
		return "", fmt.Errorf("unknown sort %q", a.Sort)
	}

	nodes := st.List(f, order)
	limit := a.Limit
	if limit <= 0 {
		limit = 50
	}

	var b strings.Builder
	total := len(nodes)
	if total > limit {
		nodes = nodes[:limit]
	}
	if total == 0 {
		return "No entries match.", nil
	}

	for _, n := range nodes {
		b.WriteString(s.renderRow(n, now))
		b.WriteByte('\n')
	}
	out := strings.TrimRight(b.String(), "\n")
	if total > len(nodes) {
		out += fmt.Sprintf("\n\n%d more not shown; narrow the filters or raise the limit.", total-len(nodes))
	}
	return out, nil
}

// renderRow is one entry on one line: handle, marker, title, then whichever
// fields are set. Plain text rather than JSON, because it is what the model
// reads and a table of set fields is both shorter and clearer than an object
// full of empty ones.
func (s *Server) renderRow(n *state.Node, now time.Time) string {
	var b strings.Builder

	fmt.Fprintf(&b, "%s  ", ulid.Short(n.ID, refLen))

	switch {
	case n.Kind != state.KindTask:
		b.WriteString("note  ")
	case n.Status == state.StatusDone:
		b.WriteString("done  ")
	case n.Status == state.StatusDoing:
		b.WriteString("doing ")
	default:
		b.WriteString("open  ")
	}

	b.WriteString(n.Title)

	if nb := s.sess.State.Get(n.Parent); nb != nil {
		fmt.Fprintf(&b, "  [%s]", nb.Title)
	}
	for _, tag := range n.Tags {
		fmt.Fprintf(&b, " #%s", tag)
	}
	if n.Priority != state.PriorityNone {
		fmt.Fprintf(&b, " priority:%s", n.Priority)
	}
	if !n.Due.IsZero() {
		fmt.Fprintf(&b, " due:%s", state.FormatDue(n.Due))
		if n.Overdue(now) {
			b.WriteString(" (overdue)")
		}
	}
	for _, id := range n.Assignees {
		fmt.Fprintf(&b, " @%s", s.sess.State.Contributor(id))
	}
	return b.String()
}

func (s *Server) renderNotebooks() string {
	nbs := s.sess.State.Notebooks()
	if len(nbs) == 0 {
		return "No notebooks yet."
	}

	var b strings.Builder
	for _, nb := range nbs {
		kids := s.sess.State.Children(nb.ID)
		open := 0
		for _, k := range kids {
			if k.Kind == state.KindTask && k.Status != state.StatusDone {
				open++
			}
		}
		fmt.Fprintf(&b, "%s  %s  (%d entries, %d open)\n", ulid.Short(nb.ID, refLen), nb.Title, len(kids), open)
	}
	return strings.TrimRight(b.String(), "\n")
}

type searchArgs struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

func (s *Server) searchEntries(raw json.RawMessage) (string, error) {
	var a searchArgs
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Query) == "" {
		return "", fmt.Errorf("give something to search for")
	}

	limit := a.Limit
	if limit <= 0 {
		limit = 20
	}

	results, err := s.sess.Search(a.Query, limit, false)
	if err != nil {
		return "", err
	}
	if len(results) == 0 {
		return fmt.Sprintf("Nothing matches %q.", a.Query), nil
	}

	now := s.sess.Now()
	var b strings.Builder
	for _, n := range results {
		b.WriteString(s.renderRow(n, now))
		b.WriteByte('\n')
		if snippet := search.Snippet(n, a.Query, 160); snippet != "" {
			fmt.Fprintf(&b, "    %s\n", snippet)
		}
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

type getArgs struct {
	Ref     string `json:"ref"`
	History bool   `json:"history"`
}

func (s *Server) getEntry(raw json.RawMessage) (string, error) {
	var a getArgs
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}

	st := s.sess.State
	n, err := resolveRef(st, a.Ref)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s  %s\n", ulid.Short(n.ID, refLen), n.Title)
	fmt.Fprintf(&b, "%s\n", strings.Join(st.Path(n), " / "))
	fmt.Fprintf(&b, "kind: %s\n", n.Kind)

	if n.Kind == state.KindTask {
		fmt.Fprintf(&b, "status: %s\n", n.Status)
		if n.Priority != state.PriorityNone {
			fmt.Fprintf(&b, "priority: %s\n", n.Priority)
		}
		if !n.Due.IsZero() {
			fmt.Fprintf(&b, "due: %s", state.FormatDue(n.Due))
			if n.Overdue(s.sess.Now()) {
				b.WriteString(" (overdue)")
			}
			b.WriteByte('\n')
		}
		if len(n.Assignees) > 0 {
			names := make([]string, len(n.Assignees))
			for i, id := range n.Assignees {
				names[i] = st.Contributor(id)
			}
			fmt.Fprintf(&b, "assigned: %s\n", strings.Join(names, ", "))
		}
	}
	if len(n.Tags) > 0 {
		fmt.Fprintf(&b, "tags: %s\n", strings.Join(n.Tags, ", "))
	}
	fmt.Fprintf(&b, "created: %s by %s\n", n.Created.Format(time.RFC3339), st.Contributor(n.CreatedBy))
	if !n.Updated.Equal(n.Created) {
		fmt.Fprintf(&b, "updated: %s by %s\n", n.Updated.Format(time.RFC3339), st.Contributor(n.UpdatedBy))
	}
	if n.Deleted {
		b.WriteString("deleted: yes\n")
	}

	if len(n.Links) > 0 {
		b.WriteString("\nreferences:\n")
		for _, id := range n.Links {
			if target := st.Get(id); target != nil {
				deleted := ""
				if target.Deleted {
					deleted = "  (deleted)"
				}
				fmt.Fprintf(&b, "  %s  %s%s\n", ulid.Short(id, refLen), target.Title, deleted)
			} else {
				fmt.Fprintf(&b, "  %s  (missing)\n", ulid.Short(id, refLen))
			}
		}
	}
	if back := st.Backlinks(n.ID); len(back) > 0 {
		b.WriteString("\nreferenced by:\n")
		for _, other := range back {
			fmt.Fprintf(&b, "  %s  %s\n", ulid.Short(other.ID, refLen), other.Title)
		}
	}

	if a.History {
		b.WriteString("\nhistory:\n")
		entries, err := s.sess.History(n.ID, 0)
		if err != nil {
			return "", err
		}
		for _, e := range entries {
			// A notebook's history includes changes that moved or linked
			// another entry to it, which name that entry.
			about := ""
			if e.Node != n.ID {
				if other := st.Get(e.Node); other != nil {
					about = fmt.Sprintf("  (%s %s)", ulid.Short(other.ID, refLen), other.Title)
				}
			}
			fmt.Fprintf(&b, "  %-20s %s by %s  %s%s\n",
				e.Action, e.At.Format(time.RFC3339), e.Author, e.Detail, about)
		}
	}

	if n.Body != "" {
		fmt.Fprintf(&b, "\n%s\n", n.Body)
	} else {
		b.WriteString("\n(no body)\n")
	}

	return strings.TrimRight(b.String(), "\n"), nil
}

// ---------------------------------------------------------------- writing

type createArgs struct {
	Kind     string   `json:"kind"`
	Title    string   `json:"title"`
	Body     string   `json:"body"`
	Notebook string   `json:"notebook"`
	Tags     []string `json:"tags"`
	Due      string   `json:"due"`
	Priority string   `json:"priority"`
}

func (s *Server) createEntry(raw json.RawMessage) (string, error) {
	var a createArgs
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Title) == "" {
		return "", fmt.Errorf("a title is required")
	}

	if a.Kind == "notebook" {
		if a.Body != "" || a.Notebook != "" || len(a.Tags) > 0 || a.Due != "" || a.Priority != "" {
			return "", errors.New("a notebook takes only a title; body, notebook, tags, due and priority apply to notes and tasks")
		}
		nb, err := s.sess.NewNotebook(a.Title)
		if err != nil {
			return "", err
		}
		if err := s.commit(); err != nil {
			return "", err
		}
		return fmt.Sprintf("Created notebook %s %q.", ulid.Short(nb.ID, refLen), nb.Title), nil
	}

	notebook := a.Notebook
	if notebook == "" {
		nb, err := s.sess.DefaultNotebook()
		if err != nil {
			return "", err
		}
		notebook = nb.ID
	}

	var n *state.Node
	var err error
	switch a.Kind {
	case "task":
		n, err = s.sess.NewTask(notebook, a.Title, a.Body)
	case "note", "":
		n, err = s.sess.NewNote(notebook, a.Title, a.Body)
	default:
		return "", fmt.Errorf("unknown kind %q; use note, task or notebook", a.Kind)
	}
	if err != nil {
		return "", err
	}

	for _, tag := range a.Tags {
		if err := s.sess.AddTag(n, tag); err != nil {
			return "", err
		}
	}
	if a.Due != "" {
		if err := s.sess.SetDue(n, a.Due); err != nil {
			return "", err
		}
	}
	if a.Priority != "" {
		p, ok := state.ParsePriority(a.Priority)
		if !ok {
			return "", fmt.Errorf("unknown priority %q", a.Priority)
		}
		if err := s.sess.SetPriority(n, p); err != nil {
			return "", err
		}
	}
	if err := s.commit(); err != nil {
		return "", err
	}

	return fmt.Sprintf("Created %s %s %q.\n%s", n.Kind, ulid.Short(n.ID, refLen), n.Title,
		s.renderRow(n, s.sess.Now())), nil
}

// updateArgs uses pointers for the fields where an empty string is a
// meaningful value, so that clearing a body differs from leaving it alone.
type updateArgs struct {
	Ref        string   `json:"ref"`
	Title      *string  `json:"title"`
	Body       *string  `json:"body"`
	Status     string   `json:"status"`
	Priority   string   `json:"priority"`
	Due        *string  `json:"due"`
	AddTags    []string `json:"addTags"`
	RemoveTags []string `json:"removeTags"`
	Notebook   string   `json:"notebook"`
	Link       string   `json:"link"`
	Unlink     string   `json:"unlink"`
}

func (s *Server) updateEntry(raw json.RawMessage) (string, error) {
	var a updateArgs
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}

	st := s.sess.State
	n, err := resolveRef(st, a.Ref)
	if err != nil {
		return "", err
	}

	// A field is reported only if it staged a change: setting a status the
	// task already has, or removing a link that is not there, changes nothing.
	var changed, unchanged []string
	given := 0
	note := func(field string, before int) {
		given++
		if s.sess.Pending() > before {
			changed = append(changed, field)
		} else {
			unchanged = append(unchanged, field)
		}
	}

	if a.Title != nil {
		before := s.sess.Pending()
		if err := s.sess.SetTitle(n, *a.Title); err != nil {
			return "", err
		}
		note("title", before)
	}
	if a.Body != nil {
		before := s.sess.Pending()
		if err := s.sess.SetBody(n, *a.Body); err != nil {
			return "", err
		}
		note("body", before)
	}
	if a.Status != "" {
		before := s.sess.Pending()
		status, ok := state.ParseStatus(a.Status)
		if !ok {
			return "", fmt.Errorf("unknown status %q; use open, doing or done", a.Status)
		}
		if err := s.sess.SetStatus(n, status); err != nil {
			return "", err
		}
		note("status", before)
	}
	if a.Priority != "" {
		before := s.sess.Pending()
		p, ok := state.ParsePriority(a.Priority)
		if !ok {
			return "", fmt.Errorf("unknown priority %q", a.Priority)
		}
		if err := s.sess.SetPriority(n, p); err != nil {
			return "", err
		}
		note("priority", before)
	}
	if a.Due != nil {
		before := s.sess.Pending()
		if err := s.sess.SetDue(n, *a.Due); err != nil {
			return "", err
		}
		note("due", before)
	}
	for _, tag := range a.AddTags {
		before := s.sess.Pending()
		if err := s.sess.AddTag(n, tag); err != nil {
			return "", err
		}
		note("tag "+tag, before)
	}
	for _, tag := range a.RemoveTags {
		before := s.sess.Pending()
		if err := s.sess.RemoveTag(n, tag); err != nil {
			return "", err
		}
		note("untag "+tag, before)
	}
	if a.Notebook != "" {
		before := s.sess.Pending()
		if n.Kind == state.KindNotebook {
			return "", errors.New("a notebook cannot be moved into a notebook")
		}
		if err := s.sess.Move(n, a.Notebook, rank.End()); err != nil {
			return "", err
		}
		note("notebook", before)
	}
	if a.Link != "" {
		before := s.sess.Pending()
		target, err := st.Resolve(a.Link)
		if err != nil {
			return "", err
		}
		if err := s.sess.Link(n, target); err != nil {
			return "", err
		}
		note("link", before)
	}
	if a.Unlink != "" {
		before := s.sess.Pending()
		target, err := st.LinkTarget(n, a.Unlink)
		if err != nil {
			return "", err
		}
		if err := s.sess.Unlink(n, target); err != nil {
			return "", err
		}
		note("unlink", before)
	}

	switch {
	case given == 0:
		return "Nothing to change: no fields were given.", nil
	case len(changed) == 0:
		return fmt.Sprintf("No change: %s already as given.", strings.Join(unchanged, ", ")), nil
	}
	if err := s.commit(); err != nil {
		return "", err
	}

	return fmt.Sprintf("Updated %s (%s).\n%s",
		ulid.Short(n.ID, refLen), strings.Join(changed, ", "), s.renderRow(n, s.sess.Now())), nil
}

// errMissingRef names the argument rather than reporting that nothing matches
// the empty string.
var errMissingRef = errors.New(`"ref" is required: a handle or a title`)

// resolveRef resolves a required ref argument.
func resolveRef(st *state.State, ref string) (*state.Node, error) {
	if strings.TrimSpace(ref) == "" {
		return nil, errMissingRef
	}
	return st.Resolve(ref)
}

type refArgs struct {
	Ref string `json:"ref"`
}

func (s *Server) deleteEntry(raw json.RawMessage) (string, error) {
	var a refArgs
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}

	n, err := resolveRef(s.sess.State, a.Ref)
	if err != nil {
		return "", err
	}
	if err := s.sess.Delete(n); err != nil {
		return "", err
	}
	if err := s.commit(); err != nil {
		return "", err
	}

	return fmt.Sprintf("Deleted %s %q. Restore it with gnotes_restore and the same handle.",
		ulid.Short(n.ID, refLen), n.Title), nil
}

func (s *Server) restoreEntry(raw json.RawMessage) (string, error) {
	var a refArgs
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}

	if strings.TrimSpace(a.Ref) == "" {
		return "", errMissingRef
	}
	n, err := s.sess.State.ResolveDeleted(a.Ref)
	if err != nil {
		return "", fmt.Errorf("no deleted entry: %w", err)
	}
	if err := s.sess.Restore(n.ID); err != nil {
		return "", err
	}
	if err := s.commit(); err != nil {
		return "", err
	}

	return fmt.Sprintf("Restored %s %q.", ulid.Short(n.ID, refLen), n.Title), nil
}

// commit writes the events a tool staged.
func (s *Server) commit() error { return s.sess.Commit() }
