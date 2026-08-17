// Package sqlexport renders a gnotes project as a SQL script that builds an
// equivalent SQLite database.
//
// It answers the "let me query my notes with SQL" question without SQLite
// becoming a dependency of gnotes: the output is plain text, so the binary
// grows by nothing, the result is exact enough to compare in a test, and the
// same script loads into anything that speaks SQLite. Pipe it into sqlite3 to
// get a database; attach that database from duckdb for analytics.
//
// The database is derived and disposable. The per-author JSONL logs remain the
// only source of truth, and re-running the export from the same log reproduces
// the same script byte for byte.
//
// Beyond the tree, the script carries the event log itself and a bitemporal
// history table, so a question the command line cannot answer -- what was open
// on a given day, which tags were on an entry last month -- becomes an indexed
// lookup rather than a replay.
package sqlexport

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/shakfu/gnotes/internal/event"
	"github.com/shakfu/gnotes/internal/state"
	"github.com/shakfu/gnotes/internal/ulid"
)

// SchemaVersion is stamped into the meta table. Bump it when the emitted
// schema changes in a way that a query written against the old one would
// misread.
const SchemaVersion = 1

// Options configure one export.
type Options struct {
	// Project is the human label recorded in the meta table.
	Project string

	// Version is the gnotes build that produced the script.
	Version string

	// Now is the export timestamp, a field so tests are reproducible.
	Now time.Time

	// History emits the bitemporal node_history table. It costs roughly one
	// row per event and is what makes point-in-time queries an index seek
	// instead of a replay.
	History bool

	// FTS emits the full-text index. It is optional only because a SQLite
	// build without FTS5 would reject the statement outright.
	FTS bool
}

// Write renders the project as SQL.
func Write(w io.Writer, st *state.State, log []event.Event, opt Options) error {
	e := &encoder{w: bufio.NewWriter(w), st: st, log: log, opt: opt}
	e.header()
	e.schema()
	e.line("BEGIN;")
	e.meta()
	e.contributors()
	e.nodes()
	e.events()
	if opt.History {
		e.history()
	}
	e.line("COMMIT;")
	if opt.FTS {
		e.blank()
		e.comment("The index is built after the load so it reads each row once.")
		e.line("INSERT INTO nodes_fts(nodes_fts) VALUES('rebuild');")
	}
	e.footer()
	if e.err != nil {
		return e.err
	}
	return e.w.Flush()
}

// encoder accumulates the script. The first write error is kept and every
// later write becomes a no-op, so the emitting code stays free of error checks.
type encoder struct {
	w   *bufio.Writer
	st  *state.State
	log []event.Event
	opt Options
	err error

	// rowid assigns each node the integer key the FTS index joins on. Node ids
	// are ULIDs, which cannot be a rowid, and an INTEGER PRIMARY KEY is what
	// makes every join in the emitted schema a direct lookup.
	rowid map[string]int

	// depths memoises distance from the root, used to emit parents before
	// children so the foreign key can stay enabled during the load.
	depths map[string]int
}

func (e *encoder) line(format string, args ...any) {
	if e.err != nil {
		return
	}
	if len(args) > 0 {
		format = fmt.Sprintf(format, args...)
	}
	if _, err := e.w.WriteString(format + "\n"); err != nil {
		e.err = err
	}
}

func (e *encoder) blank()                             { e.line("") }
func (e *encoder) comment(format string, args ...any) { e.line("-- " + fmt.Sprintf(format, args...)) }
func (e *encoder) section(title string) {
	e.blank()
	e.line("-- %s", strings.Repeat("-", 68))
	e.comment("%s", title)
	e.line("-- %s", strings.Repeat("-", 68))
	e.blank()
}

// -----------------------------------------------------------------------------
// Preamble and schema
// -----------------------------------------------------------------------------

func (e *encoder) header() {
	e.comment("gnotes SQL export")
	e.comment("project: %s", e.opt.Project)
	e.comment("exported: %s by gnotes %s", e.opt.Now.UTC().Format(time.RFC3339), e.opt.Version)
	e.comment("events: %d", len(e.log))
	e.blank()
	e.comment("This database is derived. The event logs under .gnotes/events are the")
	e.comment("source of truth; nothing here should be written to by hand, and a stale")
	e.comment("copy is fixed by exporting again rather than by patching it.")
	e.blank()
	e.comment("  gnotes export | sqlite3 notes.db")
	e.comment("  duckdb -c \"ATTACH 'notes.db' AS n (TYPE sqlite); SELECT * FROM n.nodes;\"")
	e.blank()

	// Foreign keys are off by default in SQLite and have to be asked for. The
	// load orders parents before children so they can stay on throughout,
	// which turns the import into a check that the exported tree is sound.
	e.line("PRAGMA foreign_keys = ON;")

	// Durability is pointless here and costs an fsync per statement: a crash
	// during the load is recovered by exporting again. journal_mode is left
	// alone deliberately -- setting it returns a row, which would land in the
	// middle of a pipe and read as an error.
	e.line("PRAGMA synchronous = OFF;")
}

func (e *encoder) schema() {
	e.section("Schema")

	e.comment("Key/value stamps describing this export.")
	e.line(`CREATE TABLE meta (
  k TEXT PRIMARY KEY,
  v TEXT NOT NULL
) STRICT;`)
	e.blank()

	e.comment("People who have written to the log. The id outlives the display name,")
	e.comment("which is why assignments refer to the id and never to the name.")
	e.line(`CREATE TABLE contributors (
  id   TEXT PRIMARY KEY,
  name TEXT NOT NULL
) STRICT;`)
	e.blank()

	e.comment("The tree. One table for all four kinds, matching the in-memory model.")
	e.comment("The CHECK is the interesting part: 'task fields are refused on notes' is")
	e.comment("a rule the materializer enforces by convention across four front ends,")
	e.comment("and here it becomes an invariant the storage cannot be talked out of.")
	e.line(`CREATE TABLE nodes (
  rowid_        INTEGER PRIMARY KEY,
  id            TEXT NOT NULL UNIQUE,
  kind          TEXT NOT NULL,
  parent        TEXT REFERENCES nodes(id),
  rank          TEXT NOT NULL DEFAULT '',
  title         TEXT NOT NULL DEFAULT '',
  body          TEXT NOT NULL DEFAULT '',
  deleted       INTEGER NOT NULL DEFAULT 0,
  status        TEXT,
  priority      INTEGER,
  due           TEXT,
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL,
  created_by    TEXT NOT NULL DEFAULT '',
  updated_by    TEXT NOT NULL DEFAULT '',
  priority_name TEXT GENERATED ALWAYS AS (
    CASE priority WHEN 3 THEN 'high' WHEN 2 THEN 'normal' WHEN 1 THEN 'low' END) VIRTUAL,
  CHECK (kind IN ('workspace','notebook','note','task')),
  CHECK (deleted IN (0,1)),
  CHECK (status IS NULL OR status IN ('open','doing','done')),
  CHECK (priority IS NULL OR priority BETWEEN 1 AND 3),
  CHECK (kind = 'task' OR (status IS NULL AND priority IS NULL AND due IS NULL))
) STRICT;
CREATE INDEX nodes_parent_rank ON nodes(parent, rank);
CREATE INDEX nodes_kind        ON nodes(kind) WHERE deleted = 0;
CREATE INDEX nodes_due         ON nodes(due) WHERE due IS NOT NULL AND deleted = 0;`)
	e.blank()

	e.comment("Tags are plain normalised strings, so the tag is its own key.")
	e.line(`CREATE TABLE tags (
  node TEXT NOT NULL REFERENCES nodes(id),
  tag  TEXT NOT NULL,
  PRIMARY KEY (node, tag)
) STRICT;
CREATE INDEX tags_tag ON tags(tag);`)
	e.blank()

	e.comment("Links have no foreign key on the target on purpose: a link written")
	e.comment("before its target has synced is pending, not wrong, and a constraint")
	e.comment("here would refuse to load a perfectly valid project.")
	e.line(`CREATE TABLE links (
  src TEXT NOT NULL REFERENCES nodes(id),
  dst TEXT NOT NULL,
  PRIMARY KEY (src, dst)
) STRICT;
CREATE INDEX links_dst ON links(dst);`)
	e.blank()

	e.comment("Assignees likewise: a person can be assigned before their registry")
	e.comment("entry arrives from the machine that created them.")
	e.line(`CREATE TABLE assignees (
  node    TEXT NOT NULL REFERENCES nodes(id),
  user_id TEXT NOT NULL,
  PRIMARY KEY (node, user_id)
) STRICT;`)
	e.blank()

	e.comment("The log, in canonical replay order. seq is that order, not arrival")
	e.comment("order and not wall-clock order: it is a depth-first walk of the tree")
	e.comment("the ref column forms, which is what makes it identical on every machine.")
	e.comment("The at column is decoded from the event id so a query need not know")
	e.comment("how to read a ULID.")
	e.line(`CREATE TABLE events (
  seq     INTEGER PRIMARY KEY,
  id      TEXT NOT NULL UNIQUE,
  ref     TEXT,
  at      TEXT NOT NULL,
  action  TEXT NOT NULL,
  user_id TEXT NOT NULL,
  node    TEXT,
  payload TEXT NOT NULL
) STRICT;
CREATE INDEX events_node   ON events(node, seq);
CREATE INDEX events_action ON events(action, seq);
CREATE INDEX events_user   ON events(user_id, seq);`)

	if e.opt.History {
		e.blank()
		e.comment("Bitemporal history: one row per value a field held, valid over the")
		e.comment("half-open interval [from_seq, to_seq). A NULL to_seq means current.")
		e.comment("Scalar fields have at most one live row per (node, field); the set")
		e.comment("fields -- tag, assignee, link -- have one per member, so the same")
		e.comment("query shape answers both.")
		e.line(`CREATE TABLE node_history (
  hid      INTEGER PRIMARY KEY,
  node     TEXT NOT NULL,
  field    TEXT NOT NULL,
  value    TEXT,
  from_seq INTEGER NOT NULL,
  to_seq   INTEGER,
  CHECK (to_seq IS NULL OR to_seq > from_seq)
) STRICT;
CREATE INDEX node_history_node  ON node_history(node, from_seq);
CREATE INDEX node_history_field ON node_history(field, value);
CREATE INDEX node_history_span  ON node_history(from_seq, to_seq);`)
	}

	if e.opt.FTS {
		e.blank()
		e.comment("Full text. The index is external-content over a view, so the text is")
		e.comment("stored once in nodes and tags rather than copied into the index. The")
		e.comment("view is also what lets tags be a searchable column despite living in")
		e.comment("their own table.")
		e.line(`CREATE VIEW nodes_text AS
  SELECT n.rowid_ AS rowid_,
         n.title  AS title,
         n.body   AS body,
         (SELECT group_concat(t.tag, ' ') FROM tags t WHERE t.node = n.id) AS tags
    FROM nodes n
   WHERE n.deleted = 0 AND n.kind IN ('note','task');

CREATE VIRTUAL TABLE nodes_fts USING fts5(
  title, body, tags,
  content = 'nodes_text',
  content_rowid = 'rowid_',
  tokenize = "unicode61 remove_diacritics 2"
);`)
	}
}

// -----------------------------------------------------------------------------
// Data
// -----------------------------------------------------------------------------

func (e *encoder) meta() {
	e.section("Data: meta")

	rows := [][2]string{
		{"schema_version", fmt.Sprint(SchemaVersion)},
		{"event_schema_version", fmt.Sprint(event.Version)},
		{"gnotes_version", e.opt.Version},
		{"project", e.opt.Project},
		{"exported_at", e.opt.Now.UTC().Format(time.RFC3339)},
		{"event_count", fmt.Sprint(len(e.log))},
		{"workspace", e.st.Workspace},
		{"has_history", boolText(e.opt.History)},
		{"has_fts", boolText(e.opt.FTS)},
	}
	for _, r := range rows {
		e.line("INSERT INTO meta (k, v) VALUES (%s, %s);", quote(r[0]), quote(r[1]))
	}
}

func (e *encoder) contributors() {
	e.section("Data: contributors")

	ids := make([]string, 0, len(e.st.Contributors))
	for id := range e.st.Contributors {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		e.line("INSERT INTO contributors (id, name) VALUES (%s, %s);",
			quote(id), quote(e.st.Contributors[id].Name))
	}
}

// nodes emits the tree and its three side tables.
//
// Rows are ordered by depth so that every parent is inserted before its
// children and the foreign key can stay enabled for the whole load. That turns
// the import into a structural check rather than merely a copy.
func (e *encoder) nodes() {
	ordered := e.orderedNodes()

	e.rowid = make(map[string]int, len(ordered))
	for i, n := range ordered {
		e.rowid[n.ID] = i + 1
	}

	e.section("Data: nodes")
	for _, n := range ordered {
		e.line("INSERT INTO nodes (rowid_, id, kind, parent, rank, title, body, deleted, status, priority, due, created_at, updated_at, created_by, updated_by)")
		e.line("  VALUES (%d, %s, %s, %s, %s, %s, %s, %d, %s, %s, %s, %s, %s, %s, %s);",
			e.rowid[n.ID],
			quote(n.ID),
			quote(n.Kind.String()),
			e.parentRef(n),
			quote(n.Rank),
			quote(n.Title),
			quote(n.Body),
			boolInt(n.Deleted),
			taskStatus(n),
			taskPriority(n),
			taskDue(n),
			quote(stamp(n.Created)),
			quote(stamp(n.Updated)),
			quote(n.CreatedBy),
			quote(n.UpdatedBy),
		)
	}

	e.section("Data: tags, links, assignees")
	for _, n := range ordered {
		for _, tag := range sorted(n.Tags) {
			e.line("INSERT INTO tags (node, tag) VALUES (%s, %s);", quote(n.ID), quote(tag))
		}
	}
	e.blank()
	for _, n := range ordered {
		for _, dst := range sorted(n.Links) {
			e.line("INSERT INTO links (src, dst) VALUES (%s, %s);", quote(n.ID), quote(dst))
		}
	}
	e.blank()
	for _, n := range ordered {
		for _, who := range sorted(n.Assignees) {
			e.line("INSERT INTO assignees (node, user_id) VALUES (%s, %s);", quote(n.ID), quote(who))
		}
	}
}

// parentRef renders a node's parent, as NULL for a root. A parent that is not
// itself in the tree would violate the foreign key, so it is also rendered as
// NULL: an export that refuses to load would be worse than one that reports an
// orphan as a root, and materialize already rejects the events that could
// create one.
func (e *encoder) parentRef(n *state.Node) string {
	if n.Parent == "" {
		return "NULL"
	}
	if _, ok := e.st.Nodes[n.Parent]; !ok {
		return "NULL"
	}
	return quote(n.Parent)
}

func (e *encoder) events() {
	e.section("Data: events")

	for i := range e.log {
		ev := &e.log[i]

		at := ""
		if t, err := ulid.Timestamp(ev.ID); err == nil {
			at = stamp(t.UTC())
		}

		payload, err := json.Marshal(ev.Payload)
		if err != nil {
			if e.err == nil {
				e.err = fmt.Errorf("encode payload of event %s: %w", ev.ID, err)
			}
			return
		}

		e.line("INSERT INTO events (seq, id, ref, at, action, user_id, node, payload) VALUES (%d, %s, %s, %s, %s, %s, %s, %s);",
			i,
			quote(ev.ID),
			quoteOrNull(ev.Ref),
			quote(at),
			quote(string(ev.Action)),
			quote(ev.UserID),
			quoteOrNull(ev.Payload.ID),
			quote(string(payload)),
		)
	}
}

// -----------------------------------------------------------------------------
// History
// -----------------------------------------------------------------------------

// span is one open interval during the fold, before its end is known.
type span struct {
	node  string
	field string
	value string
	null  bool // the field was cleared, so value is SQL NULL
	from  int
	to    int // -1 while still open
}

// history folds the log into intervals.
//
// This is the piece the SQLite variant would reuse verbatim: it is the same
// walk as state.Materialize, recording what each event changed instead of only
// where it ended up. Scalar fields close their previous interval when set
// again; set members close theirs when removed.
func (e *encoder) history() {
	e.section("Data: history")

	var spans []*span
	open := map[string]*span{} // node|field|value -> the interval still running

	key := func(node, field, value string) string { return node + "\x00" + field + "\x00" + value }

	// set opens a new interval for a scalar field, closing whatever it held.
	set := func(node, field, value string, isNull bool, seq int) {
		if node == "" {
			return
		}
		if _, known := e.st.Nodes[node]; !known {
			// A superseded workspace root, or a node whose creation event was
			// rejected. Nothing in the nodes table would join to it.
			return
		}
		k := key(node, field, "")
		if prev, ok := open[k]; ok {
			if prev.value == value && prev.null == isNull {
				return // the event restated what was already true
			}
			prev.to = seq
			delete(open, k)
		}
		s := &span{node: node, field: field, value: value, null: isNull, from: seq, to: -1}
		open[k] = s
		spans = append(spans, s)
	}

	// add and remove open and close a set membership.
	add := func(node, field, value string, seq int) {
		if node == "" || value == "" {
			return
		}
		if _, known := e.st.Nodes[node]; !known {
			return
		}
		k := key(node, field, value)
		if _, ok := open[k]; ok {
			return
		}
		s := &span{node: node, field: field, value: value, from: seq, to: -1}
		open[k] = s
		spans = append(spans, s)
	}
	remove := func(node, field, value string, seq int) {
		k := key(node, field, value)
		if prev, ok := open[k]; ok {
			prev.to = seq
			delete(open, k)
		}
	}

	for seq := range e.log {
		ev := &e.log[seq]
		p := &ev.Payload

		switch ev.Action {
		case event.InitWorkspace:
			set(p.ID, "title", p.Name, false, seq)
			set(p.ID, "deleted", "0", false, seq)

		case event.AddNotebook, event.AddNote, event.AddTask:
			title := p.Title
			if ev.Action == event.AddNotebook {
				title = p.Name
			}
			set(p.ID, "title", title, false, seq)
			set(p.ID, "parent", p.Parent, p.Parent == "", seq)
			set(p.ID, "rank", p.Rank, false, seq)
			set(p.ID, "deleted", "0", false, seq)
			if ev.Action == event.AddTask {
				// A task is open the moment it exists; the materializer says
				// so by leaving the zero value alone, and the history has to
				// say it explicitly or a point-in-time query would find a task
				// with no status at all.
				set(p.ID, "status", state.StatusOpen.String(), false, seq)
			}

		case event.EditTitle:
			title := p.Title
			if title == "" {
				title = p.Name
			}
			set(p.ID, "title", title, false, seq)

		case event.EditBody:
			set(p.ID, "body", p.Body, false, seq)

		case event.MoveNode:
			set(p.ID, "parent", p.Parent, p.Parent == "", seq)
			set(p.ID, "rank", p.Rank, false, seq)

		case event.Rebalance:
			// One event respaces every sibling, so it opens an interval per
			// node. Sorted, because a map iterates in a different order each
			// run and the script has to be reproducible.
			for _, id := range sortedKeys(p.Ranks) {
				set(id, "rank", p.Ranks[id], false, seq)
			}

		case event.DeleteNode:
			set(p.ID, "deleted", "1", false, seq)

		case event.RestoreNode:
			set(p.ID, "deleted", "0", false, seq)

		case event.SetStatus:
			if s, ok := state.ParseStatus(p.Status); ok {
				set(p.ID, "status", s.String(), false, seq)
			}

		case event.SetPriority:
			// The canonical form is stored, not the typed one, so that "prio"
			// and "priority" and "h" all compare equal in a query.
			if pr, ok := state.ParsePriority(p.Priority); ok {
				set(p.ID, "priority", fmt.Sprint(int(pr)), pr == state.PriorityNone, seq)
			}

		case event.SetDue:
			if due, ok := state.ParseDueAbsolute(p.Due); ok {
				set(p.ID, "due", state.FormatDue(due), due.IsZero(), seq)
			}

		case event.AddTag:
			add(p.ID, "tag", state.NormalizeTag(p.Tag), seq)
		case event.RemoveTag:
			remove(p.ID, "tag", state.NormalizeTag(p.Tag), seq)

		case event.AddAssignee:
			add(p.ID, "assignee", p.Assignee, seq)
		case event.RemoveAssignee:
			remove(p.ID, "assignee", p.Assignee, seq)

		case event.LinkNode:
			add(p.ID, "link", p.Target, seq)
		case event.UnlinkNode:
			remove(p.ID, "link", p.Target, seq)
		}
	}

	for i, s := range spans {
		e.line("INSERT INTO node_history (hid, node, field, value, from_seq, to_seq) VALUES (%d, %s, %s, %s, %d, %s);",
			i+1,
			quote(s.node),
			quote(s.field),
			nullable(s.value, s.null),
			s.from,
			seqOrNull(s.to),
		)
	}
}

// -----------------------------------------------------------------------------
// Footer
// -----------------------------------------------------------------------------

func (e *encoder) footer() {
	e.section("Worked examples")

	e.comment("Everything below is commented out. It is here because the point of the")
	e.comment("export is the questions the command line cannot ask.")
	e.blank()

	e.comment("Open tasks by priority, most urgent first:")
	e.comment("")
	e.comment("  SELECT n.priority_name, n.title, n.due, nb.title AS notebook")
	e.comment("    FROM nodes n JOIN nodes nb ON nb.id = n.parent")
	e.comment("   WHERE n.kind = 'task' AND n.status <> 'done' AND n.deleted = 0")
	e.comment("   ORDER BY n.priority DESC NULLS LAST, n.due;")
	e.blank()

	e.comment("Tags that occur together, which no gnotes command reports:")
	e.comment("")
	e.comment("  SELECT a.tag, b.tag, count(*) AS n")
	e.comment("    FROM tags a JOIN tags b ON a.node = b.node AND a.tag < b.tag")
	e.comment("   GROUP BY 1, 2 ORDER BY n DESC LIMIT 20;")
	e.blank()

	if e.opt.FTS {
		e.comment("Ranked full text, with the title weighted above the body and a snippet:")
		e.comment("")
		e.comment("  SELECT n.id, n.title, snippet(nodes_fts, 1, '[', ']', '...', 8)")
		e.comment("    FROM nodes_fts f JOIN nodes n ON n.rowid_ = f.rowid")
		e.comment("   WHERE nodes_fts MATCH 'lexer OR parser'")
		e.comment("   ORDER BY bm25(nodes_fts, 10.0, 1.0, 5.0) LIMIT 10;")
		e.blank()
		e.comment("Restrict a term to one column with a prefix: 'tags:bug', 'title:lexer'.")
		e.blank()
	}

	if e.opt.History {
		e.comment("The state of the project as it stood at event 120. This is the query")
		e.comment("that justifies the whole exercise: gnotes answers it by replaying the")
		e.comment("log, which is linear in history, and here it is an index seek.")
		e.comment("")
		e.comment("  SELECT node, value AS title FROM node_history")
		e.comment("   WHERE field = 'title'")
		e.comment("     AND from_seq <= 120 AND (to_seq IS NULL OR to_seq > 120);")
		e.blank()

		e.comment("Every task that was open at the end of July, whatever it is now:")
		e.comment("")
		e.comment("  WITH cutoff AS (SELECT max(seq) AS seq FROM events WHERE at < '2026-08-01')")
		e.comment("  SELECT h.node FROM node_history h, cutoff c")
		e.comment("   WHERE h.field = 'status' AND h.value = 'open'")
		e.comment("     AND h.from_seq <= c.seq AND (h.to_seq IS NULL OR h.to_seq > c.seq);")
		e.blank()

		e.comment("How long tasks take, from creation to done:")
		e.comment("")
		e.comment("  SELECT n.title,")
		e.comment("         julianday(d.at) - julianday(c.at) AS days")
		e.comment("    FROM node_history h")
		e.comment("    JOIN nodes  n ON n.id = h.node")
		e.comment("    JOIN events c ON c.seq = (SELECT min(from_seq) FROM node_history")
		e.comment("                               WHERE node = h.node AND field = 'title')")
		e.comment("    JOIN events d ON d.seq = h.from_seq")
		e.comment("   WHERE h.field = 'status' AND h.value = 'done'")
		e.comment("   ORDER BY days DESC;")
		e.blank()
	}

	e.comment("Who has been writing, by week:")
	e.comment("")
	e.comment("  SELECT strftime('%%Y-W%%W', at) AS week, c.name, count(*)")
	e.comment("    FROM events e LEFT JOIN contributors c ON c.id = e.user_id")
	e.comment("   GROUP BY 1, 2 ORDER BY 1 DESC;")
}

// -----------------------------------------------------------------------------
// Ordering and rendering helpers
// -----------------------------------------------------------------------------

// orderedNodes returns every node, deleted ones included, parents before
// children and otherwise by id so the script is reproducible.
func (e *encoder) orderedNodes() []*state.Node {
	out := make([]*state.Node, 0, len(e.st.Nodes))
	for _, n := range e.st.Nodes {
		out = append(out, n)
	}
	e.depths = make(map[string]int, len(out))
	sort.Slice(out, func(i, j int) bool {
		di, dj := e.depth(out[i]), e.depth(out[j])
		if di != dj {
			return di < dj
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// depth is the number of hops to a root. A node whose parent is missing counts
// as a root, and the walk is bounded by the number of nodes so a cycle
// introduced by a malformed log cannot hang the export.
func (e *encoder) depth(n *state.Node) int {
	if d, ok := e.depths[n.ID]; ok {
		return d
	}
	d, cur := 0, n
	for i := 0; i <= len(e.st.Nodes); i++ {
		parent, ok := e.st.Nodes[cur.Parent]
		if !ok {
			break
		}
		d++
		cur = parent
	}
	e.depths[n.ID] = d
	return d
}

// quote renders a SQL string literal. Doubling the apostrophe is the whole of
// SQLite's escaping rule; newlines and every other byte are literal.
func quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// quoteOrNull renders the empty string as NULL, for columns where absent and
// empty are the same thing.
func quoteOrNull(s string) string {
	if s == "" {
		return "NULL"
	}
	return quote(s)
}

func nullable(s string, isNull bool) string {
	if isNull {
		return "NULL"
	}
	return quote(s)
}

func seqOrNull(seq int) string {
	if seq < 0 {
		return "NULL"
	}
	return fmt.Sprint(seq)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func boolText(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// stamp renders a time for storage: RFC 3339 in UTC, so string comparison and
// chronological comparison are the same operation.
func stamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// taskStatus, taskPriority and taskDue render the columns the CHECK constraint
// requires to be NULL on anything that is not a task.
func taskStatus(n *state.Node) string {
	if n.Kind != state.KindTask {
		return "NULL"
	}
	return quote(n.Status.String())
}

func taskPriority(n *state.Node) string {
	if n.Kind != state.KindTask || n.Priority == state.PriorityNone {
		return "NULL"
	}
	return fmt.Sprint(int(n.Priority))
}

func taskDue(n *state.Node) string {
	if n.Kind != state.KindTask || n.Due.IsZero() {
		return "NULL"
	}
	return quote(state.FormatDue(n.Due))
}

// sorted copies and sorts, so the caller's slice keeps the order the tree
// depends on.
func sorted(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
