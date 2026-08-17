package sqlexport

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shakfu/gnotes/internal/event"
	"github.com/shakfu/gnotes/internal/state"
	"github.com/shakfu/gnotes/internal/ulid"
)

// builder assembles a synthetic log the way the session package does: one
// author, each event referring to the last, which is already canonical order.
type builder struct {
	gen    *ulid.Generator
	user   string
	last   string
	events []event.Event
}

func newBuilder() *builder {
	return &builder{gen: ulid.NewGenerator(), user: "01M0AUTHORAUTHORAUTHOR00"}
}

func (b *builder) emit(action event.Action, p event.Payload) string {
	e := event.Event{
		ID:      b.gen.New(),
		Ref:     b.last,
		Action:  action,
		Payload: p,
		UserID:  b.user,
	}
	b.last = e.ID
	b.events = append(b.events, e)
	return e.ID
}

func (b *builder) id() string { return b.gen.New() }

// project builds a log exercising every shape the exporter has to render: a
// tree three deep, a note and a task, a tag added and later removed, a link, a
// deletion, and text containing the one character SQL cares about.
func project(t *testing.T) (*state.State, []event.Event) {
	t.Helper()
	b := newBuilder()

	ws := b.id()
	b.emit(event.InitWorkspace, event.Payload{ID: ws, Name: "demo"})
	b.emit(event.CreateContributor, event.Payload{ID: b.user, Name: "sa"})

	nb := b.id()
	b.emit(event.AddNotebook, event.Payload{ID: nb, Parent: ws, Rank: "7fff", Name: "work"})

	note := b.id()
	b.emit(event.AddNote, event.Payload{ID: note, Parent: nb, Rank: "7fff", Title: "design sketch"})
	b.emit(event.EditBody, event.Payload{ID: note, Body: "it's a lexer;\nsecond line"})

	task := b.id()
	b.emit(event.AddTask, event.Payload{ID: task, Parent: nb, Rank: "bfff", Title: "fix the lexer"})
	b.emit(event.AddTag, event.Payload{ID: task, Tag: "bug"})
	b.emit(event.AddTag, event.Payload{ID: task, Tag: "parser"})
	b.emit(event.SetDue, event.Payload{ID: task, Due: "2026-08-21"})
	b.emit(event.SetPriority, event.Payload{ID: task, Priority: "high"})
	b.emit(event.AddAssignee, event.Payload{ID: task, Assignee: b.user})
	b.emit(event.LinkNode, event.Payload{ID: task, Target: note})
	b.emit(event.RemoveTag, event.Payload{ID: task, Tag: "bug"})

	gone := b.id()
	b.emit(event.AddTask, event.Payload{ID: gone, Parent: nb, Rank: "dfff", Title: "benchmark it"})
	b.emit(event.SetStatus, event.Payload{ID: gone, Status: "done"})
	b.emit(event.DeleteNode, event.Payload{ID: gone})

	events := event.Sort(b.events)
	st, problems := state.Materialize(events)
	if len(problems) > 0 {
		t.Fatalf("the fixture log does not replay cleanly: %v", problems)
	}
	return st, events
}

func export(t *testing.T, st *state.State, log []event.Event, opt Options) string {
	t.Helper()
	if opt.Now.IsZero() {
		opt.Now = time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	}
	var buf bytes.Buffer
	if err := Write(&buf, st, log, opt); err != nil {
		t.Fatalf("export: %v", err)
	}
	return buf.String()
}

func fullOptions() Options {
	return Options{Project: "demo", Version: "test", History: true, FTS: true}
}

// -----------------------------------------------------------------------------

// The export is a derived artefact people will regenerate and diff. Two runs
// over the same log must therefore differ in nothing but the timestamp, which
// means every map in the encoder has to be walked in sorted order.
func TestExportIsReproducible(t *testing.T) {
	st, log := project(t)

	first := export(t, st, log, fullOptions())
	for i := 0; i < 5; i++ {
		if again := export(t, st, log, fullOptions()); again != first {
			t.Fatal("two exports of the same log differ; a map is being iterated unsorted")
		}
	}
}

// Foreign keys stay on for the whole load, so a child inserted before its
// parent would abort the import. The ordering is checked on the encoder rather
// than on the emitted text because a body may contain newlines, which makes
// "one row, one line" false and any line-based parse of the script wrong.
func TestNodesAreEmittedParentsFirst(t *testing.T) {
	st, log := project(t)
	e := &encoder{st: st, log: log}

	ordered := e.orderedNodes()
	if len(ordered) != len(st.Nodes) {
		t.Fatalf("ordered %d nodes, the tree has %d", len(ordered), len(st.Nodes))
	}

	seen := map[string]bool{}
	for _, n := range ordered {
		if _, isNode := st.Nodes[n.Parent]; isNode && !seen[n.Parent] {
			t.Fatalf("node %q is emitted before its parent", n.Title)
		}
		seen[n.ID] = true
	}
}

// Ties are broken by id so the script is the same on every run, whatever order
// the node map happened to iterate in.
func TestNodeOrderIsTotal(t *testing.T) {
	st, log := project(t)

	first := ids((&encoder{st: st, log: log}).orderedNodes())
	for i := 0; i < 20; i++ {
		if again := ids((&encoder{st: st, log: log}).orderedNodes()); again != first {
			t.Fatalf("node order is not stable:\n%s\n%s", first, again)
		}
	}
}

func ids(nodes []*state.Node) string {
	var b strings.Builder
	for _, n := range nodes {
		b.WriteString(n.ID)
		b.WriteByte(' ')
	}
	return b.String()
}

// The CHECK constraint refuses task fields on anything that is not a task, so
// the encoder has to render them as NULL rather than as a zero value. A note
// with a status would abort the load, which the integration test then confirms.
func TestNonTaskKindsGetNullTaskColumns(t *testing.T) {
	st, log := project(t)

	for _, n := range (&encoder{st: st, log: log}).orderedNodes() {
		if n.Kind == state.KindTask {
			continue
		}
		for name, got := range map[string]string{
			"status":   taskStatus(n),
			"priority": taskPriority(n),
			"due":      taskDue(n),
		} {
			if got != "NULL" {
				t.Errorf("%s %q has a %s column of %s, want NULL", n.Kind, n.Title, name, got)
			}
		}
	}
}

// A task with no priority set is not a task with priority zero: the column is
// NULL, so "ORDER BY priority DESC" puts it last rather than inventing a level.
func TestUnsetTaskFieldsAreNull(t *testing.T) {
	st, _ := project(t)

	var plain *state.Node
	for _, n := range st.Nodes {
		if n.Kind == state.KindTask && n.Title == "benchmark it" {
			plain = n
		}
	}
	if plain == nil {
		t.Fatal("the fixture no longer has a task without priority or due date")
	}
	if got := taskPriority(plain); got != "NULL" {
		t.Errorf("an unprioritised task rendered priority %s, want NULL", got)
	}
	if got := taskDue(plain); got != "NULL" {
		t.Errorf("an undated task rendered due %s, want NULL", got)
	}
	if got := taskStatus(plain); got != "'done'" {
		t.Errorf("status rendered %s, want 'done'", got)
	}
}

// The history table is the reason for the whole exercise. A tag added and
// later removed has to leave a closed interval, and a scalar set twice has to
// close the first before opening the second.
func TestHistoryClosesIntervals(t *testing.T) {
	st, log := project(t)
	sql := export(t, st, log, fullOptions())

	rows := historyRows(sql)

	var bug, parser *histRow
	for i := range rows {
		if rows[i].field == "tag" && rows[i].value == "'bug'" {
			bug = &rows[i]
		}
		if rows[i].field == "tag" && rows[i].value == "'parser'" {
			parser = &rows[i]
		}
	}
	if bug == nil || parser == nil {
		t.Fatal("the tag history is missing an entry")
	}
	if bug.to == "NULL" {
		t.Error("a removed tag is still recorded as current")
	}
	if parser.to != "NULL" {
		t.Error("a tag that was never removed has been closed")
	}
	if num(t, bug.to) <= num(t, bug.from) {
		t.Errorf("a tag interval ends at or before it starts: %s..%s", bug.from, bug.to)
	}

	// Two statuses on the deleted task: open at creation, then done.
	var statuses []histRow
	for _, r := range rows {
		if r.field == "status" {
			statuses = append(statuses, r)
		}
	}
	if len(statuses) != 3 {
		t.Fatalf("expected three status intervals (two tasks created open, one closed), got %d", len(statuses))
	}
	closed := 0
	for _, r := range statuses {
		if r.to != "NULL" {
			closed++
		}
	}
	if closed != 1 {
		t.Errorf("expected exactly one superseded status, got %d", closed)
	}
}

type histRow struct{ node, field, value, from, to string }

// historyRows parses the node_history inserts back out of the script. Reading
// the emitted SQL rather than an internal structure is deliberate: what the
// test cares about is what a database would see.
//
// The value is taken as whatever lies between the fixed leading columns and
// the two trailing sequence numbers, so a title containing a comma does not
// shift the parse.
func historyRows(sql string) []histRow {
	var out []histRow
	for _, line := range strings.Split(sql, "\n") {
		const prefix = "INSERT INTO node_history (hid, node, field, value, from_seq, to_seq) VALUES ("
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		body := strings.TrimSuffix(strings.TrimPrefix(line, prefix), ");")

		head := strings.SplitN(body, ", ", 4)
		if len(head) != 4 {
			continue
		}
		rest, to, ok := cutLast(head[3])
		if !ok {
			continue
		}
		value, from, ok := cutLast(rest)
		if !ok {
			continue
		}
		out = append(out, histRow{
			node:  strings.Trim(head[1], "'"),
			field: strings.Trim(head[2], "'"),
			value: value,
			from:  from,
			to:    to,
		})
	}
	return out
}

// cutLast splits off the final comma-separated field.
func cutLast(s string) (head, tail string, ok bool) {
	i := strings.LastIndex(s, ", ")
	if i < 0 {
		return "", "", false
	}
	return s[:i], s[i+2:], true
}

func num(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatalf("expected a sequence number, got %q", s)
	}
	return n
}

func TestOptionsOmitTables(t *testing.T) {
	st, log := project(t)

	full := export(t, st, log, fullOptions())
	for _, want := range []string{"CREATE TABLE node_history", "CREATE VIRTUAL TABLE nodes_fts"} {
		if !strings.Contains(full, want) {
			t.Errorf("a full export is missing %q", want)
		}
	}

	slim := export(t, st, log, Options{Project: "demo", Version: "test"})
	for _, unwanted := range []string{"node_history", "nodes_fts", "nodes_text"} {
		if strings.Contains(slim, unwanted) {
			t.Errorf("a slim export still mentions %q", unwanted)
		}
	}
}

// An apostrophe in a title or body is the one character that can turn data
// into syntax.
func TestQuotingEscapesApostrophes(t *testing.T) {
	cases := map[string]string{
		"plain":       "'plain'",
		"it's":        "'it''s'",
		"''":          "''''''",
		"a\nb":        "'a\nb'",
		"":            "''",
		`back\slash`:  `'back\slash'`,
		"drop'; --":   "'drop''; --'",
		"semi;colon":  "'semi;colon'",
		"tab\there":   "'tab\there'",
		"unicode: ok": "'unicode: ok'",
	}
	for in, want := range cases {
		if got := quote(in); got != want {
			t.Errorf("quote(%q) = %s, want %s", in, got, want)
		}
	}

	if got := quoteOrNull(""); got != "NULL" {
		t.Errorf("quoteOrNull(\"\") = %s, want NULL", got)
	}
	if got := quoteOrNull("x"); got != "'x'" {
		t.Errorf("quoteOrNull(%q) = %s, want 'x'", "x", got)
	}
}

// An empty project is a valid project, and the script it produces has to load.
func TestEmptyProjectExports(t *testing.T) {
	st, problems := state.Materialize(nil)
	if len(problems) > 0 {
		t.Fatalf("materializing nothing produced problems: %v", problems)
	}

	sql := export(t, st, nil, fullOptions())
	if !strings.Contains(sql, "CREATE TABLE nodes") {
		t.Error("an empty project produced no schema")
	}
	if strings.Contains(sql, "INSERT INTO nodes (") {
		t.Error("an empty project produced node rows")
	}
	loadIntoSQLite(t, sql)
}

// -----------------------------------------------------------------------------
// Integration
// -----------------------------------------------------------------------------

// The script is only worth anything if a database accepts it. This is the
// check that the schema, the constraints and the insert order agree, which no
// amount of string comparison can establish.
func TestScriptLoadsIntoSQLite(t *testing.T) {
	st, log := project(t)
	db := loadIntoSQLite(t, export(t, st, log, fullOptions()))

	// The load runs with foreign keys on, so a bad parent would already have
	// failed. These check the parts a load cannot notice.
	if got := query(t, db, "PRAGMA integrity_check;"); got != "ok" {
		t.Errorf("integrity_check: %s", got)
	}
	if got := query(t, db, "PRAGMA foreign_key_check;"); got != "" {
		t.Errorf("foreign_key_check reported violations: %s", got)
	}

	if got := query(t, db, "SELECT count(*) FROM nodes;"); got != "5" {
		t.Errorf("nodes: got %s, want 5", got)
	}
	if got := query(t, db, "SELECT count(*) FROM events;"); got != "16" {
		t.Errorf("events: got %s, want 16", got)
	}
	if got := query(t, db, "SELECT group_concat(tag) FROM tags;"); got != "parser" {
		t.Errorf("tags: got %q, want the one surviving tag", got)
	}
	if got := query(t, db, "SELECT title FROM nodes WHERE deleted = 1;"); got != "benchmark it" {
		t.Errorf("deleted node: got %q", got)
	}
	if got := query(t, db, "SELECT priority_name FROM nodes WHERE title = 'fix the lexer';"); got != "high" {
		t.Errorf("generated priority_name: got %q, want high", got)
	}

	// The body carries an apostrophe and a newline, so this is the round trip
	// that proves the quoting.
	if got := query(t, db, "SELECT body FROM nodes WHERE title = 'design sketch';"); got != "it's a lexer;\nsecond line" {
		t.Errorf("body did not survive quoting: %q", got)
	}
}

func TestFullTextIndexAnswersQueries(t *testing.T) {
	st, log := project(t)
	db := loadIntoSQLite(t, export(t, st, log, fullOptions()))

	got := query(t, db, `SELECT group_concat(n.title, '|') FROM nodes_fts f
	    JOIN nodes n ON n.rowid_ = f.rowid WHERE nodes_fts MATCH 'lexer'
	    ORDER BY f.rank;`)
	if !strings.Contains(got, "fix the lexer") || !strings.Contains(got, "design sketch") {
		t.Errorf("matching 'lexer' returned %q, want both the title and the body hit", got)
	}

	// Tags live in their own table and reach the index through the view, so
	// this is the check that the external-content wiring is right.
	if got := query(t, db, `SELECT n.title FROM nodes_fts f JOIN nodes n ON n.rowid_ = f.rowid
	    WHERE nodes_fts MATCH 'tags:parser';`); got != "fix the lexer" {
		t.Errorf("matching a tag returned %q", got)
	}

	// The view excludes deleted entries, so a removed task must not be found.
	if got := query(t, db, `SELECT count(*) FROM nodes_fts WHERE nodes_fts MATCH 'benchmark';`); got != "0" {
		t.Errorf("a deleted entry is still in the index (%s hits)", got)
	}
}

// The point of the history table: the project as it stood at a past event,
// without replaying anything.
func TestHistoryAnswersPointInTimeQueries(t *testing.T) {
	st, log := project(t)
	db := loadIntoSQLite(t, export(t, st, log, fullOptions()))

	at := func(seq, field string) string {
		return query(t, db, `SELECT group_concat(value, ',') FROM node_history
		    WHERE field = '`+field+`'
		      AND node = (SELECT id FROM nodes WHERE title = 'fix the lexer')
		      AND from_seq <= `+seq+` AND (to_seq IS NULL OR to_seq > `+seq+`);`)
	}

	// The bug tag is removed by the last event touching that task, so an early
	// cutoff has to see both tags and the present has to see one.
	early := at("8", "tag")
	if early != "bug,parser" && early != "parser,bug" {
		t.Errorf("tags at seq 8 = %q, want both", early)
	}
	if now := query(t, db, `SELECT group_concat(tag) FROM tags
	    WHERE node = (SELECT id FROM nodes WHERE title = 'fix the lexer');`); now != "parser" {
		t.Errorf("tags now = %q, want parser", now)
	}

	// A task that is deleted today still existed then.
	if got := query(t, db, `SELECT count(*) FROM node_history
	    WHERE field = 'deleted' AND value = '0' AND node = (SELECT id FROM nodes WHERE title = 'benchmark it')
	      AND from_seq <= 13 AND (to_seq IS NULL OR to_seq > 13);`); got != "1" {
		t.Errorf("the deleted task was not live at seq 13 (got %s)", got)
	}
}

// loadIntoSQLite runs the script through the sqlite3 binary and returns the
// path of the resulting database.
//
// Shelling out rather than linking a driver is the whole point of emitting SQL
// in the first place: the package under test has no database dependency, so
// neither does its test. Where sqlite3 is absent the integration checks are
// skipped and the string-level tests above still run.
func loadIntoSQLite(t *testing.T, sql string) string {
	t.Helper()

	bin, err := exec.LookPath("sqlite3")
	if err != nil {
		t.Skip("sqlite3 is not installed; skipping the checks that need a database")
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "export.sql")
	if err := os.WriteFile(script, []byte(sql), 0o644); err != nil {
		t.Fatalf("write script: %v", err)
	}

	db := filepath.Join(dir, "notes.db")
	cmd := exec.Command(bin, "-bail", db)
	cmd.Stdin = strings.NewReader(sql)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sqlite3 refused the script: %v\n%s\nscript at %s", err, out, script)
	}
	return db
}

// query runs one statement and returns its output with the trailing newline
// removed, which is enough for the single values these tests assert on.
func query(t *testing.T, db, sql string) string {
	t.Helper()

	cmd := exec.Command("sqlite3", "-bail", db, sql)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("query %q: %v\n%s", sql, err, out)
	}
	return strings.TrimRight(string(out), "\n")
}
