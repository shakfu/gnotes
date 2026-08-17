// Package event defines the append-only log that is the sole source of truth
// in gnotes. Nothing else is persisted; every view of the data is produced by
// replaying these records.
//
// Each event carries its own ULID and a reference to the last event its author
// had seen. Those references form a tree, and a deterministic traversal of that
// tree yields the same total order on every machine regardless of the order
// files arrived in. That is what lets two people edit offline and converge
// without a merge conflict or a CRDT.
package event

import (
	"encoding/json"
	"fmt"

	"github.com/shakfu/gnotes/internal/ulid"
)

// Version is the schema version stamped on every persisted line. Bump it only
// for a change that older binaries cannot read; additive changes should
// instead take the unknown-action path, which is already tolerated.
const Version = 1

// Action names the kind of change an event records.
type Action string

// The complete set of actions this build understands.
//
// Actions are grouped by what they touch: the node tree, text content, task
// fields, and the registries of tags and contributors. Task-only actions are
// rejected by the materializer when aimed at a note; the log format itself does
// not distinguish them, so the check lives with the domain rules.
const (
	// Tree structure.
	InitWorkspace Action = "init.workspace"
	AddNotebook   Action = "add.notebook"
	AddNote       Action = "add.note"
	AddTask       Action = "add.task"
	MoveNode      Action = "move.node"
	DeleteNode    Action = "delete.node"
	RestoreNode   Action = "restore.node"
	Rebalance     Action = "rebalance.children"

	// Content.
	EditTitle Action = "edit.title"
	EditBody  Action = "edit.body"

	// Task fields.
	SetStatus      Action = "set.status"
	SetDue         Action = "set.due"
	SetPriority    Action = "set.priority"
	AddAssignee    Action = "add.assignee"
	RemoveAssignee Action = "remove.assignee"

	// Cross-references between nodes, the soft link from a task to the note it
	// came out of.
	LinkNode   Action = "link.node"
	UnlinkNode Action = "unlink.node"

	// Tags. A tag is a normalised string rather than an entry in a registry
	// with its own id. A registry would buy renaming a tag everywhere at once,
	// which is not worth an extra event, a lookup table and a layer of
	// indirection on every read; renaming is instead a bulk remove-and-add.
	AddTag    Action = "add.tag"
	RemoveTag Action = "remove.tag"

	// Contributors do keep a registry, because their identity has to outlive
	// their display name: the name appears in log filenames and in historical
	// events, so matching on it would split one person into several.
	CreateContributor Action = "create.contributor"
	RenameContributor Action = "rename.contributor"
)

// known is the runtime registry of understood actions. An event naming an
// action outside this set was written by a newer gnotes; replay skips it rather
// than failing, so a downgrade degrades instead of breaking. The log is never
// rewritten, so upgrading restores the skipped events.
var known = map[Action]bool{
	InitWorkspace: true, AddNotebook: true, AddNote: true, AddTask: true,
	MoveNode: true, DeleteNode: true, RestoreNode: true, Rebalance: true,
	EditTitle: true, EditBody: true,
	SetStatus: true, SetDue: true, SetPriority: true,
	AddAssignee: true, RemoveAssignee: true,
	LinkNode: true, UnlinkNode: true,
	AddTag: true, RemoveTag: true,
	CreateContributor: true, RenameContributor: true,
}

// Known reports whether this build understands the action.
func Known(a Action) bool { return known[a] }

// Payload holds every field any action can carry, in one flat struct.
//
// A union of per-action types would document the schema more precisely, but in
// Go it costs an interface allocation and a type switch on every decoded line,
// and the compiler still could not check which fields an action is allowed to
// set. One struct decodes in a single pass with no indirection, and Validate
// enforces the per-action rules explicitly. Every field is omitempty, so a line
// on disk carries only what its action actually uses.
type Payload struct {
	// ID is the node, tag or contributor the event acts on. Rebalance is the
	// one action that leaves it empty, because it acts on a parent's children
	// rather than on a single node.
	//
	// It is written as "node" so that "id" on a log line unambiguously means
	// the event's own id.
	ID string `json:"node,omitempty"`

	// Parent and Rank position a node among its siblings.
	Parent string `json:"parent,omitempty"`
	Rank   string `json:"rank,omitempty"`

	// Name titles a notebook, tag or contributor; Title titles a note or task.
	// They are separate fields so that a log line reads unambiguously.
	Name  string `json:"name,omitempty"`
	Title string `json:"title,omitempty"`

	// Body is markdown content.
	Body string `json:"md,omitempty"`

	// Tag is a normalised tag string; Assignee is a contributor id.
	Tag      string `json:"tag,omitempty"`
	Assignee string `json:"assignee,omitempty"`

	// Task fields. Due is RFC 3339; an empty Due clears the date.
	Status   string `json:"status,omitempty"`
	Due      string `json:"due,omitempty"`
	Priority string `json:"priority,omitempty"`

	// Target is the other end of a link.
	Target string `json:"target,omitempty"`

	// Ranks carries a whole respacing, node id to new rank, for Rebalance.
	Ranks map[string]string `json:"ranks,omitempty"`
}

// Event is a single record in the log, as replay sees it.
type Event struct {
	// ID is this event's ULID. It orders concurrent events and, being
	// time-sortable, dates them without a separate timestamp field.
	ID string

	// Ref is the ULID of the last event the author had seen when writing this
	// one, or empty for the first event in a log. Refs form the tree that Sort
	// walks; they are what make the merge deterministic rather than dependent
	// on wall-clock skew between machines.
	Ref string

	Action  Action
	Payload Payload

	// UserID and UserName identify the author. They are not stored on the line:
	// every event in a file has the same author, so the name lives in the
	// filename and is attached during load. That keeps a long log meaningfully
	// smaller and makes it impossible for a line to disagree with its file.
	UserID   string
	UserName string
}

// Time returns the instant the event was created, decoded from its ULID.
func (e Event) Time() (uint64, error) { return ulid.Time(e.ID) }

// wire is the on-disk shape of an event, one flat JSON object per line.
//
// Two deviations from epiq, both of which make a line cheaper to read and
// easier to read in a diff.
//
// The action is a fixed field rather than the payload's key. A dynamic key
// forces every reader to decode into a map and scan it just to discover which
// action it is holding.
//
// The payload is inlined rather than nested under "p". Nesting cost a copy of
// the payload bytes and a second decode pass on every line; flattening makes a
// line one struct and one pass. The payload's node reference is named "node"
// so nothing collides with the envelope's "id".
type wire struct {
	V   int    `json:"v"`
	ID  string `json:"id"`
	Ref string `json:"ref,omitempty"`
	A   Action `json:"a"`
	Payload
}

// Encode renders an event as a single JSON line, without the trailing newline.
func Encode(e Event) ([]byte, error) {
	return json.Marshal(wire{V: Version, ID: e.ID, Ref: e.Ref, A: e.Action, Payload: e.Payload})
}

// ErrUnknownAction reports an action this build does not implement. Callers
// distinguish it from a malformed line so they can skip forward rather than
// abort the load.
type ErrUnknownAction struct{ Action Action }

func (e *ErrUnknownAction) Error() string {
	return fmt.Sprintf("unknown action %q", e.Action)
}

// Decode parses one JSON line into an event. The author fields are left empty;
// the loader fills them from the filename.
//
// It returns ErrUnknownAction for a well-formed line naming an action this
// build does not know, so the caller can step over it rather than abort.
func Decode(line []byte) (Event, error) {
	var w wire
	if err := json.Unmarshal(line, &w); err != nil {
		return Event{}, fmt.Errorf("malformed event: %w", err)
	}
	if w.V != Version {
		return Event{}, fmt.Errorf("unsupported schema version %d, this build reads version %d", w.V, Version)
	}
	if !ulid.Valid(w.ID) {
		return Event{}, fmt.Errorf("invalid event id %q", w.ID)
	}
	if w.Ref != "" && !ulid.Valid(w.Ref) {
		return Event{}, fmt.Errorf("invalid ref %q on event %s", w.Ref, w.ID)
	}

	e := Event{ID: w.ID, Ref: w.Ref, Action: w.A, Payload: w.Payload}
	if !known[w.A] {
		return e, &ErrUnknownAction{Action: w.A}
	}
	return e, nil
}
