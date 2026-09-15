package store

import (
	"fmt"
	"strings"

	"github.com/shakfu/gnotes/internal/state"
)

// schemaVersion is stored in PRAGMA user_version.
const schemaVersion = 1

// timeLayout is how every time is stored: UTC with milliseconds, which sorts
// as text and matches strftime('%Y-%m-%dT%H:%M:%fZ').
const timeLayout = "2006-01-02T15:04:05.000Z"

// nodeFields are the nodes columns whose changes are recorded in history.
var nodeFields = []string{"kind", "parent", "rank", "title", "body", "status", "priority", "due", "deleted_at"}

// setTable is a one-to-many table of node values, named field in changes.
type setTable struct{ table, key, val, field string }

var setTables = []setTable{
	{"tags", "node", "tag", "tag"},
	{"links", "src", "dst", "link"},
	{"assignees", "node", "user_id", "assignee"},
}

// values returns the node's members of the set t stores.
func (t setTable) values(n *state.Node) []string {
	switch t.field {
	case "tag":
		return n.Tags
	case "link":
		return n.Links
	}
	return n.Assignees
}

// The writer row carries the time and author of the transaction gnotes is
// committing, for the history triggers to read. gnotes sets it at the start of
// a write and clears it before committing, so a write from any other client
// finds it empty and is recorded at the wall clock with no author.
const (
	clockExpr  = `coalesce((SELECT at FROM writer), strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))`
	authorExpr = `(SELECT author FROM writer)`
)

const tables = `
CREATE TABLE meta (
	k TEXT PRIMARY KEY,
	v TEXT NOT NULL
) STRICT;

CREATE TABLE writer (
	only   INTEGER PRIMARY KEY CHECK (only = 1),
	at     TEXT,
	author TEXT
) STRICT;
INSERT INTO writer (only) VALUES (1);

CREATE TABLE contributors (
	id   TEXT PRIMARY KEY,
	name TEXT NOT NULL CHECK (trim(name) != '')
) STRICT;

-- One table for the workspace, notebooks, notes and tasks. num is a stable
-- integer key for the full-text index; id is the identity everything else uses.
CREATE TABLE nodes (
	num        INTEGER PRIMARY KEY,
	id         TEXT NOT NULL UNIQUE,
	kind       TEXT NOT NULL CHECK (kind IN ('workspace', 'notebook', 'note', 'task')),
	parent     TEXT REFERENCES nodes (id) DEFERRABLE INITIALLY DEFERRED,
	rank       TEXT NOT NULL DEFAULT '',
	title      TEXT NOT NULL CHECK (trim(title) != ''),
	body       TEXT NOT NULL DEFAULT '',
	status     TEXT CHECK (status IN ('open', 'doing', 'done')),
	priority   TEXT CHECK (priority IN ('low', 'normal', 'high')),
	due        TEXT,
	deleted_at TEXT,
	deletion   TEXT,
	created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
	created_by TEXT,
	updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
	updated_by TEXT,
	CHECK ((kind = 'workspace') = (parent IS NULL)),
	CHECK (kind = 'task' OR (status IS NULL AND priority IS NULL AND due IS NULL)),
	CHECK (deletion IS NULL OR deleted_at IS NOT NULL)
) STRICT;
CREATE INDEX nodes_parent ON nodes (parent, rank);

CREATE TABLE tags (
	node TEXT NOT NULL REFERENCES nodes (id) ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED,
	tag  TEXT NOT NULL CHECK (tag != ''),
	PRIMARY KEY (node, tag)
) STRICT;

-- dst has no foreign key: a link may name an entry that no longer exists.
CREATE TABLE links (
	src TEXT NOT NULL REFERENCES nodes (id) ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED,
	dst TEXT NOT NULL CHECK (dst != src),
	PRIMARY KEY (src, dst)
) STRICT;

CREATE TABLE assignees (
	node    TEXT NOT NULL REFERENCES nodes (id) ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED,
	user_id TEXT NOT NULL,
	PRIMARY KEY (node, user_id)
) STRICT;

-- Every change to the tables above, one row per field or set member, written
-- by triggers.
CREATE TABLE changes (
	seq       INTEGER PRIMARY KEY,
	at        TEXT NOT NULL,
	author    TEXT,
	node      TEXT NOT NULL,
	field     TEXT NOT NULL,
	old_value TEXT,
	new_value TEXT
) STRICT;
CREATE INDEX changes_node ON changes (node, seq);
CREATE INDEX changes_at ON changes (at);

CREATE VIRTUAL TABLE nodes_fts USING fts5 (
	title, body, tags,
	tokenize = 'unicode61 remove_diacritics 2'
);
`

// schema returns the whole schema. The trigger bodies repeat one statement per
// field, so they are generated.
func schema() string {
	var b strings.Builder
	b.WriteString(tables)

	change := func(node, field, old, new, when string) {
		fmt.Fprintf(&b, "\tINSERT INTO changes (at, author, node, field, old_value, new_value)\n\t\tSELECT %s, %s, %s, '%s', %s, %s WHERE %s;\n",
			clockExpr, authorExpr, node, field, old, new, when)
	}

	b.WriteString("\nCREATE TRIGGER nodes_inserted AFTER INSERT ON nodes BEGIN\n")
	for _, f := range nodeFields {
		change("new.id", f, "NULL", "new."+f, "coalesce(new."+f+", '') != ''")
	}
	b.WriteString("END;\n\nCREATE TRIGGER nodes_updated AFTER UPDATE ON nodes BEGIN\n")
	for _, f := range nodeFields {
		change("new.id", f, "old."+f, "new."+f, "old."+f+" IS NOT new."+f)
	}
	b.WriteString("END;\n\nCREATE TRIGGER nodes_deleted AFTER DELETE ON nodes BEGIN\n")
	change("old.id", "removed", "old.kind", "NULL", "1")
	b.WriteString("END;\n")

	for _, t := range setTables {
		table, key, val, field := t.table, t.key, t.val, t.field
		fmt.Fprintf(&b, "\nCREATE TRIGGER %s_inserted AFTER INSERT ON %s BEGIN\n", table, table)
		change("new."+key, field, "NULL", "new."+val, "1")
		fmt.Fprintf(&b, "END;\n\nCREATE TRIGGER %s_deleted AFTER DELETE ON %s BEGIN\n", table, table)
		change("old."+key, field, "old."+val, "NULL", "1")
		b.WriteString("END;\n")
	}

	b.WriteString("\nCREATE TRIGGER contributors_inserted AFTER INSERT ON contributors BEGIN\n")
	change("new.id", "contributor", "NULL", "new.name", "1")
	b.WriteString("END;\n\nCREATE TRIGGER contributors_updated AFTER UPDATE ON contributors BEGIN\n")
	change("new.id", "contributor", "old.name", "new.name", "old.name IS NOT new.name")
	b.WriteString("END;\n")

	// An edit from another client that leaves updated_at alone still dates
	// the node.
	b.WriteString(`
CREATE TRIGGER nodes_touched AFTER UPDATE ON nodes
WHEN (SELECT at FROM writer) IS NULL AND new.updated_at IS old.updated_at BEGIN
	UPDATE nodes SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE num = new.num;
END;
`)

	// The index holds notes and tasks, deleted ones included, so a search can
	// ask for them.
	const reindex = `	DELETE FROM nodes_fts WHERE rowid = (SELECT num FROM nodes WHERE id = %[1]s);
	INSERT INTO nodes_fts (rowid, title, body, tags)
		SELECT num, title, body, (SELECT group_concat(tag, ' ') FROM tags WHERE node = %[1]s)
		FROM nodes WHERE id = %[1]s AND kind IN ('note', 'task');
`
	b.WriteString("\nCREATE TRIGGER nodes_fts_inserted AFTER INSERT ON nodes BEGIN\n")
	fmt.Fprintf(&b, reindex, "new.id")
	b.WriteString("END;\n\nCREATE TRIGGER nodes_fts_updated AFTER UPDATE ON nodes\n")
	b.WriteString("WHEN old.title IS NOT new.title OR old.body IS NOT new.body OR old.kind IS NOT new.kind OR old.id IS NOT new.id BEGIN\n")
	b.WriteString("\tDELETE FROM nodes_fts WHERE rowid = old.num;\n")
	fmt.Fprintf(&b, reindex, "new.id")
	b.WriteString("END;\n\nCREATE TRIGGER nodes_fts_deleted AFTER DELETE ON nodes BEGIN\n")
	b.WriteString("\tDELETE FROM nodes_fts WHERE rowid = old.num;\nEND;\n")
	b.WriteString("\nCREATE TRIGGER tags_fts_inserted AFTER INSERT ON tags BEGIN\n")
	fmt.Fprintf(&b, reindex, "new.node")
	b.WriteString("END;\n\nCREATE TRIGGER tags_fts_deleted AFTER DELETE ON tags BEGIN\n")
	fmt.Fprintf(&b, reindex, "old.node")
	b.WriteString("END;\n")

	return b.String()
}
