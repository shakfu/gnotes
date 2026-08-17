// Package state turns an ordered event log into the tree of notebooks, notes
// and tasks that the rest of gnotes reads.
//
// Nothing here is persisted. Materialize is a pure function of the events it
// is given, which is what makes viewing the project as it stood at a past
// moment a matter of replaying a prefix rather than storing snapshots.
package state

import (
	"strings"
	"time"
)

// Kind distinguishes the four node types. Notes and tasks are separate kinds
// rather than one kind with an optional status, so that an operation that only
// makes sense for a task is rejected outright when aimed at a note instead of
// quietly giving the note a status field.
type Kind uint8

// The node kinds, in tree order.
const (
	KindWorkspace Kind = iota
	KindNotebook
	KindNote
	KindTask
)

// String returns the lowercase name of the kind, as the command line spells it.
func (k Kind) String() string {
	switch k {
	case KindWorkspace:
		return "workspace"
	case KindNotebook:
		return "notebook"
	case KindNote:
		return "note"
	case KindTask:
		return "task"
	}
	return "unknown"
}

// ParseKind reads a kind name, accepting the plural the command line allows.
func ParseKind(s string) (Kind, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "workspace", "workspaces":
		return KindWorkspace, true
	case "notebook", "notebooks", "nb":
		return KindNotebook, true
	case "note", "notes":
		return KindNote, true
	case "task", "tasks":
		return KindTask, true
	}
	return 0, false
}

// Status is a task's progress. Notes never carry one.
type Status uint8

// The task statuses. Open is the zero value, so a task created without one is
// open rather than in an undefined state.
const (
	StatusOpen Status = iota
	StatusDoing
	StatusDone
)

// String returns the lowercase name of the status.
func (s Status) String() string {
	switch s {
	case StatusOpen:
		return "open"
	case StatusDoing:
		return "doing"
	case StatusDone:
		return "done"
	}
	return "unknown"
}

// ParseStatus reads a status name and its common abbreviations.
func ParseStatus(s string) (Status, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "open", "todo", "o":
		return StatusOpen, true
	case "doing", "wip", "started", "d":
		return StatusDoing, true
	case "done", "closed", "complete", "completed", "x":
		return StatusDone, true
	}
	return 0, false
}

// Priority ranks a task. None is the zero value so that priority is opt-in and
// an unprioritised list stays uncluttered.
type Priority uint8

// The priorities, ascending.
const (
	PriorityNone Priority = iota
	PriorityLow
	PriorityNormal
	PriorityHigh
)

// String returns the lowercase name of the priority, empty for None.
func (p Priority) String() string {
	switch p {
	case PriorityLow:
		return "low"
	case PriorityNormal:
		return "normal"
	case PriorityHigh:
		return "high"
	}
	return ""
}

// ParsePriority reads a priority name. The empty string clears it.
func ParsePriority(s string) (Priority, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "none", "-":
		return PriorityNone, true
	case "low", "l":
		return PriorityLow, true
	case "normal", "medium", "med", "n", "m":
		return PriorityNormal, true
	case "high", "h", "urgent":
		return PriorityHigh, true
	}
	return 0, false
}

// Node is one entry in the tree: the workspace, a notebook, a note or a task.
//
// One struct covers all four kinds rather than an interface per kind. The
// fields a kind does not use stay zero, which costs a little memory and saves
// a type assertion on every access in the interface, the search index and the
// command line.
type Node struct {
	// ID is the node's ULID, minted when it was created.
	ID string

	Kind Kind

	// Parent is the containing node, empty for the workspace.
	Parent string

	// Rank orders the node among its siblings. See package rank.
	Rank string

	// Title is the one-line name of the node.
	Title string

	// Body is markdown content. Notebooks and the workspace leave it empty.
	Body string

	// Deleted marks a node removed. The node stays in the tree because
	// deletion is a forward event, not a rewrite: the log still contains
	// everything that ever referred to it, and a restore has to be able to put
	// it back.
	Deleted bool

	// Tags are normalised tag strings, sorted and deduplicated.
	Tags []string

	// Links are ids of other nodes this one references, the soft link from a
	// task to the note it came out of. They are deliberately not enforced: a
	// link to a node that has not synced yet is shown as pending rather than
	// rejected.
	Links []string

	// Task fields. They stay zero on every other kind.
	Status    Status
	Priority  Priority
	Due       time.Time
	Assignees []string

	// Provenance, all derived from event ids rather than stored separately.
	Created   time.Time
	Updated   time.Time
	CreatedBy string
	UpdatedBy string
}

// IsTask reports whether task fields apply to this node.
func (n *Node) IsTask() bool { return n.Kind == KindTask }

// Overdue reports whether the node is an unfinished task whose due date has
// passed.
func (n *Node) Overdue(now time.Time) bool {
	return n.Kind == KindTask && n.Status != StatusDone &&
		!n.Due.IsZero() && n.Due.Before(now)
}

// HasTag reports whether the node carries the tag, which must already be
// normalised.
func (n *Node) HasTag(tag string) bool {
	for _, t := range n.Tags {
		if t == tag {
			return true
		}
	}
	return false
}

// Contributor is a person who has written to the log.
type Contributor struct {
	// ID is a ULID minted once per user. It is what assignments and log
	// filenames refer to, so a rename never orphans anything.
	ID string

	// Name is the current display name.
	Name string
}

// NormalizeTag reduces a tag to its canonical form: lowercase, trimmed, inner
// whitespace turned into hyphens, and a leading hash dropped so that "#bug"
// and "bug" are the same tag.
//
// Normalising on the way in rather than at comparison time means the stored
// value is already the comparison key, so filtering and indexing never have to
// transform anything.
func NormalizeTag(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "#")

	var b strings.Builder
	b.Grow(len(s))
	lastHyphen := false

	for _, r := range strings.ToLower(s) {
		switch {
		case r == ' ' || r == '\t' || r == '\n' || r == '_':
			if !lastHyphen && b.Len() > 0 {
				b.WriteByte('-')
				lastHyphen = true
			}
		default:
			b.WriteRune(r)
			lastHyphen = false
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}

// ParseDue reads a due date typed by a user. It accepts a plain date, an
// RFC 3339 timestamp, and the handful of relative words that are quicker to
// type. The empty string clears the due date and returns the zero time.
//
// Relative words are resolved here, at the moment the command is given, and
// never during replay: an event storing "tomorrow" would mean a different day
// every time the log was read.
func ParseDue(s string, now time.Time) (time.Time, bool) {
	s = strings.TrimSpace(s)

	// The relative words are matched case-insensitively, but the absolute
	// forms are not folded: RFC 3339 is case-sensitive, and lowercasing a
	// timestamp turns its T and Z separators into something unparseable.
	lower := strings.ToLower(s)
	if lower == "" || lower == "none" || lower == "-" {
		return time.Time{}, true
	}

	// Midnight local time, so "today" means the whole of today.
	day := func(t time.Time) time.Time {
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	}

	switch lower {
	case "today":
		return day(now), true
	case "tomorrow":
		return day(now.AddDate(0, 0, 1)), true
	case "yesterday":
		return day(now.AddDate(0, 0, -1)), true
	}

	// A weekday name means the next such day, which is how people say it.
	for i, name := range []string{"sunday", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday"} {
		if lower == name || lower == name[:3] {
			delta := (i - int(now.Weekday()) + 7) % 7
			if delta == 0 {
				delta = 7
			}
			return day(now.AddDate(0, 0, delta)), true
		}
	}

	return ParseDueAbsolute(s)
}

// ParseDueAbsolute reads only unambiguous date forms, rejecting the relative
// words ParseDue allows. Replay uses it so that a stored due date means the
// same thing on every machine and at every moment.
func ParseDueAbsolute(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, true
	}
	for _, layout := range []string{"2006-01-02", time.RFC3339, "2006-01-02 15:04", "2006/01/02"} {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// FormatDue renders a due date for storage in an event payload. The zero time
// renders empty, which is how an event clears the field.
func FormatDue(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	// A date with no time of day round-trips as a date, keeping the log
	// readable in a diff.
	if t.Hour() == 0 && t.Minute() == 0 && t.Second() == 0 {
		return t.Format("2006-01-02")
	}
	return t.Format(time.RFC3339)
}
