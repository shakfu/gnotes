package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/shakfu/gwiki/internal/state"

	_ "modernc.org/sqlite"
)

// open connects to the database at path and brings its schema up to date.
//
// The rollback journal, not WAL, so the database file is complete after every
// commit: it is what the user commits to git, and WAL keeps recent writes in a
// separate file. Write transactions begin IMMEDIATE, so a second writer waits
// on busy_timeout instead of failing on lock upgrade.
func open(root, path string, create bool) (*Project, error) {
	mode := "rw"
	if create {
		mode = "rwc"
		dir := filepath.Dir(path)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create %s: %w", dir, err)
		}
		// The wiki shares the directory and its .gitignore, so lines are added
		// where missing rather than the file written only when absent.
		for name, body := range map[string]string{".gitignore": gitignore, ".gitattributes": gitattributes} {
			if err := addLines(filepath.Join(dir, name), body); err != nil {
				return nil, err
			}
		}
	}

	q := url.Values{}
	q.Set("mode", mode)
	q.Set("_txlock", "immediate")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "journal_mode(DELETE)")
	dsn := (&url.URL{Scheme: "file", OmitHost: true, Path: path, RawQuery: q.Encode()}).String()

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	p := &Project{Root: root, Path: path, db: db}
	if err := p.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return p, nil
}

// migrate creates the schema in a new database and refuses one written by a
// newer gwiki.
func (p *Project) migrate() error {
	tx, err := p.db.Begin()
	if err != nil {
		return fmt.Errorf("open %s: %w", p.Path, err)
	}
	defer tx.Rollback()

	var version int
	if err := tx.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("read %s: %w", p.Path, err)
	}
	switch {
	case version == schemaVersion:
		return nil
	case version > schemaVersion:
		return fmt.Errorf("%s was written by a newer gwiki (schema %d, this build reads %d)", p.Path, version, schemaVersion)
	}
	if _, err := tx.Exec(schema()); err != nil {
		return fmt.Errorf("create the schema in %s: %w", p.Path, err)
	}
	if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, schemaVersion)); err != nil {
		return fmt.Errorf("create the schema in %s: %w", p.Path, err)
	}
	return tx.Commit()
}

func (p *Project) readConfig() error {
	rows, err := p.db.Query(`SELECT k, v FROM meta WHERE k IN ('name', 'created')`)
	if err != nil {
		return fmt.Errorf("read %s: %w", p.Path, err)
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return fmt.Errorf("read %s: %w", p.Path, err)
		}
		if k == "name" {
			p.Config.Name = v
		} else {
			p.Config.Created = v
		}
	}
	return rows.Err()
}

func (p *Project) writeConfig() error { return writeConfig(p.db, p.Path, p.Config) }

func writeConfig(q querier, path string, c Config) error {
	if _, err := q.Exec(`INSERT OR REPLACE INTO meta (k, v) VALUES ('name', ?), ('created', ?)`, c.Name, c.Created); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// querier is what *sql.DB and *sql.Tx have in common.
type querier interface {
	Query(string, ...any) (*sql.Rows, error)
	QueryRow(string, ...any) *sql.Row
	Exec(string, ...any) (sql.Result, error)
}

// Snapshot is the highest seq in changes. Every write that changes a stored
// value adds a row there, whichever client made it.
type Snapshot int64

// Rows is the stored content of a project.
type Rows struct {
	Nodes        []state.Node
	Contributors []state.Contributor

	// Problems are stored values that could not be read, such as a due date
	// in an unknown format. The rest of the node is kept.
	Problems []state.Problem

	// Head is the Snapshot the rows were read at.
	Head Snapshot
}

// Load reads the current rows in one read transaction.
func Load(p *Project) (Rows, error) {
	out, err := load(p)
	if err != nil {
		return Rows{}, fmt.Errorf("read %s: %w", p.Path, err)
	}
	return out, nil
}

func load(p *Project) (Rows, error) {
	tx, err := p.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Rows{}, err
	}
	defer tx.Rollback()

	var out Rows
	if err := tx.QueryRow(`SELECT coalesce(max(seq), 0) FROM changes`).Scan(&out.Head); err != nil {
		return Rows{}, err
	}

	rows, err := tx.Query(`SELECT id, kind, coalesce(parent, ''), rank, title, body, coalesce(status, ''),
		coalesce(priority, ''), coalesce(due, ''), coalesce(deleted_at, ''), coalesce(deletion, ''),
		created_at, coalesce(created_by, ''), updated_at, coalesce(updated_by, '') FROM nodes`)
	if err != nil {
		return Rows{}, err
	}
	index := map[string]int{}
	for rows.Next() {
		var r record
		if err := rows.Scan(&r.id, &r.kind, &r.parent, &r.rank, &r.title, &r.body, &r.status, &r.priority, &r.due,
			&r.deletedAt, &r.deletion, &r.created, &r.createdBy, &r.updated, &r.updatedBy); err != nil {
			rows.Close()
			return Rows{}, err
		}
		n, problems := r.node()
		out.Problems = append(out.Problems, problems...)
		index[n.ID] = len(out.Nodes)
		out.Nodes = append(out.Nodes, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return Rows{}, err
	}

	for _, t := range setTables {
		rows, err := tx.Query(fmt.Sprintf(`SELECT %s, %s FROM %s ORDER BY 1, 2`, t.key, t.val, t.table))
		if err != nil {
			return Rows{}, err
		}
		for rows.Next() {
			var id, v string
			if err := rows.Scan(&id, &v); err != nil {
				rows.Close()
				return Rows{}, err
			}
			if i, ok := index[id]; ok {
				addMember(&out.Nodes[i], t.field, v)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return Rows{}, err
		}
	}

	rows, err = tx.Query(`SELECT id, name FROM contributors ORDER BY id`)
	if err != nil {
		return Rows{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var c state.Contributor
		if err := rows.Scan(&c.ID, &c.Name); err != nil {
			return Rows{}, err
		}
		out.Contributors = append(out.Contributors, c)
	}
	return out, rows.Err()
}

// record is a nodes row as text.
type record struct {
	id, kind, parent, rank, title, body, status, priority, due  string
	deletedAt, deletion, created, createdBy, updated, updatedBy string
}

// node converts a row, reporting values it cannot read.
func (r record) node() (state.Node, []state.Problem) {
	var problems []state.Problem
	bad := func(field, value string) {
		problems = append(problems, state.Problem{ID: r.id, Reason: fmt.Sprintf("unreadable %s %q", field, value)})
	}

	n := state.Node{
		ID: r.id, Parent: r.parent, Rank: r.rank, Title: r.title, Body: r.body,
		Deleted: r.deletedAt != "", Deletion: r.deletion,
		CreatedBy: r.createdBy, UpdatedBy: r.updatedBy,
	}
	var ok bool
	if n.Kind, ok = state.ParseKind(r.kind); !ok {
		bad("kind", r.kind)
	}
	if r.status != "" {
		if n.Status, ok = state.ParseStatus(r.status); !ok {
			bad("status", r.status)
		}
	}
	if n.Priority, ok = state.ParsePriority(r.priority); !ok {
		bad("priority", r.priority)
	}
	if n.Due, ok = state.ParseDueAbsolute(r.due); !ok {
		bad("due", r.due)
	}
	for _, t := range []struct {
		field, value string
		into         *time.Time
	}{{"deleted_at", r.deletedAt, &n.DeletedAt}, {"created_at", r.created, &n.Created}, {"updated_at", r.updated, &n.Updated}} {
		if t.value == "" {
			continue
		}
		v, err := time.Parse(time.RFC3339Nano, t.value)
		if err != nil {
			bad(t.field, t.value)
			continue
		}
		*t.into = v
	}
	return n, problems
}

// addMember adds a value to one of a node's sets, named as in changes. Tags
// are normalised, since a row may have been written by hand.
func addMember(n *state.Node, field, v string) {
	switch field {
	case "tag":
		if tag := state.NormalizeTag(v); tag != "" && !n.HasTag(tag) {
			n.Tags = append(n.Tags, tag)
		}
	case "link":
		n.Links = append(n.Links, v)
	case "assignee":
		n.Assignees = append(n.Assignees, v)
	}
}

// Snap returns the current Snapshot, or -1 when the database cannot be read, so
// that a caller comparing it sees a change and reloads into the error.
func Snap(p *Project) Snapshot {
	var head Snapshot
	if err := p.db.QueryRow(`SELECT coalesce(max(seq), 0) FROM changes`).Scan(&head); err != nil {
		return -1
	}
	return head
}

// Write stores the given nodes and contributors, as they now are, in one
// transaction dated at and attributed to actor. It returns the Snapshot from
// before the write and after it; a caller whose last load was at prev has seen
// everything up to head.
func Write(p *Project, actor Actor, at time.Time, nodes []*state.Node, contributors []*state.Contributor) (prev, head Snapshot, err error) {
	if !actor.Valid() {
		return 0, 0, errors.New("cannot write: the current user is not configured; run 'gwiki notes init'")
	}
	tx, err := p.db.Begin()
	if err != nil {
		return 0, 0, fmt.Errorf("write %s: %w", p.Path, err)
	}
	defer tx.Rollback()

	if err := tx.QueryRow(`SELECT coalesce(max(seq), 0) FROM changes`).Scan(&prev); err != nil {
		return 0, 0, fmt.Errorf("write %s: %w", p.Path, err)
	}
	if err := writeRows(tx, at, actor.ID, nodes, contributors); err != nil {
		return 0, 0, fmt.Errorf("write %s: %w", p.Path, err)
	}
	if err := tx.QueryRow(`SELECT coalesce(max(seq), 0) FROM changes`).Scan(&head); err != nil {
		return 0, 0, fmt.Errorf("write %s: %w", p.Path, err)
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, fmt.Errorf("write %s: %w", p.Path, err)
	}
	return prev, head, nil
}

// writeRows upserts nodes and contributors inside tx. The writer row is set
// for the duration, so the history triggers record at and author.
func writeRows(tx *sql.Tx, at time.Time, author string, nodes []*state.Node, contributors []*state.Contributor) error {
	if _, err := tx.Exec(`UPDATE writer SET at = ?, author = ?`, formatTime(at), nullable(author)); err != nil {
		return err
	}
	for _, c := range contributors {
		if _, err := tx.Exec(`INSERT INTO contributors (id, name) VALUES (?, ?)
			ON CONFLICT (id) DO UPDATE SET name = excluded.name`, c.ID, c.Name); err != nil {
			return fmt.Errorf("contributor %s: %w", c.ID, err)
		}
	}
	for _, n := range nodes {
		if err := writeNode(tx, n); err != nil {
			return fmt.Errorf("entry %q: %w", n.Title, err)
		}
	}
	_, err := tx.Exec(`UPDATE writer SET at = NULL, author = NULL`)
	return err
}

func writeNode(tx *sql.Tx, n *state.Node) error {
	var status, priority, due, deletedAt any
	if n.Kind == state.KindTask {
		status = n.Status.String()
		priority = nullable(n.Priority.String())
		due = nullable(state.FormatDue(n.Due))
	}
	if n.Deleted {
		deletedAt = formatTime(n.DeletedAt)
	}
	_, err := tx.Exec(`INSERT INTO nodes (id, kind, parent, rank, title, body, status, priority, due,
			deleted_at, deletion, created_at, created_by, updated_at, updated_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET kind = excluded.kind, parent = excluded.parent, rank = excluded.rank,
			title = excluded.title, body = excluded.body, status = excluded.status, priority = excluded.priority,
			due = excluded.due, deleted_at = excluded.deleted_at, deletion = excluded.deletion,
			updated_at = excluded.updated_at, updated_by = excluded.updated_by`,
		n.ID, n.Kind.String(), nullable(n.Parent), n.Rank, n.Title, n.Body, status, priority, due,
		deletedAt, nullable(n.Deletion), formatTime(n.Created), nullable(n.CreatedBy), formatTime(n.Updated), nullable(n.UpdatedBy))
	if err != nil {
		return err
	}

	for _, t := range setTables {
		if err := syncSet(tx, t, n.ID, t.values(n)); err != nil {
			return err
		}
	}
	return nil
}

// syncSet makes one node's rows in a one-to-many table match values. Only the
// difference is written, so history records only real changes.
func syncSet(tx *sql.Tx, t setTable, id string, values []string) error {
	args := make([]any, 0, len(values)+1)
	args = append(args, id)
	for _, v := range values {
		args = append(args, v)
	}
	marks := strings.TrimSuffix(strings.Repeat("?, ", len(values)), ", ")
	if _, err := tx.Exec(fmt.Sprintf(`DELETE FROM %s WHERE %s = ? AND %s NOT IN (%s)`, t.table, t.key, t.val, marks), args...); err != nil {
		return err
	}
	for _, v := range values {
		if _, err := tx.Exec(fmt.Sprintf(`INSERT OR IGNORE INTO %s (%s, %s) VALUES (?, ?)`, t.table, t.key, t.val), id, v); err != nil {
			return err
		}
	}
	return nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func formatTime(t time.Time) string { return t.UTC().Format(timeLayout) }

// Change is one recorded edit: a field of a node set from Old to New, or a set
// member added (Old empty) or removed (New empty).
type Change struct {
	Seq    int64
	At     time.Time
	Author string
	Node   string
	Field  string
	Old    string
	New    string
}

// History returns the last limit changes, oldest first. With a node id it keeps
// the changes to that node and those that moved or linked something to it. A
// limit of zero or less returns them all.
func History(p *Project, node string, limit int) ([]Change, error) {
	if limit <= 0 {
		limit = -1
	}
	where, args := "1", []any{}
	if node != "" {
		where = `node = ? OR (field IN ('parent', 'link') AND ? IN (old_value, new_value))`
		args = append(args, node, node)
	}
	args = append(args, limit)
	rows, err := p.db.Query(fmt.Sprintf(`SELECT * FROM (
		SELECT seq, at, coalesce(author, ''), node, field, coalesce(old_value, ''), coalesce(new_value, '')
		FROM changes WHERE %s ORDER BY seq DESC LIMIT ?) ORDER BY seq`, where), args...)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", p.Path, err)
	}
	defer rows.Close()

	var out []Change
	for rows.Next() {
		var c Change
		var at string
		if err := rows.Scan(&c.Seq, &at, &c.Author, &c.Node, &c.Field, &c.Old, &c.New); err != nil {
			return nil, fmt.Errorf("read %s: %w", p.Path, err)
		}
		c.At, _ = time.Parse(time.RFC3339Nano, at)
		out = append(out, c)
	}
	return out, rows.Err()
}

// RowsAt reconstructs the rows as they stood at cutoff by replaying changes.
func RowsAt(p *Project, cutoff time.Time) (Rows, error) {
	rows, err := p.db.Query(`SELECT seq, at, coalesce(author, ''), node, field, old_value, new_value
		FROM changes WHERE at <= ? ORDER BY seq`, formatTime(cutoff))
	if err != nil {
		return Rows{}, fmt.Errorf("read %s: %w", p.Path, err)
	}
	defer rows.Close()

	type past struct {
		record
		members map[string][]string
		removed bool
	}
	var order []string
	nodes := map[string]*past{}
	names := map[string]string{}
	var out Rows

	for rows.Next() {
		var seq int64
		var at, author, id, field string
		var old, new sql.NullString
		if err := rows.Scan(&seq, &at, &author, &id, &field, &old, &new); err != nil {
			return Rows{}, fmt.Errorf("read %s: %w", p.Path, err)
		}
		out.Head = Snapshot(seq)
		if field == "contributor" {
			names[id] = new.String
			continue
		}

		n := nodes[id]
		if n == nil {
			n = &past{record: record{id: id, created: at, createdBy: author}, members: map[string][]string{}}
			nodes[id] = n
			order = append(order, id)
		}
		n.updated, n.updatedBy = at, author

		switch field {
		case "removed":
			n.removed = true
		case "kind":
			n.removed, n.kind = false, new.String
		case "parent":
			n.parent = new.String
		case "rank":
			n.rank = new.String
		case "title":
			n.title = new.String
		case "body":
			n.body = new.String
		case "status":
			n.status = new.String
		case "priority":
			n.priority = new.String
		case "due":
			n.due = new.String
		case "deleted_at":
			n.deletedAt = new.String
		default:
			set := n.members[field]
			if old.Valid {
				if i := slices.Index(set, old.String); i >= 0 {
					set = append(set[:i], set[i+1:]...)
				}
			}
			if new.Valid {
				set = append(set, new.String)
			}
			n.members[field] = set
		}
	}
	if err := rows.Err(); err != nil {
		return Rows{}, fmt.Errorf("read %s: %w", p.Path, err)
	}

	for _, id := range order {
		r := nodes[id]
		if r.removed || r.kind == "" {
			continue
		}
		n, problems := r.node()
		for field, values := range r.members {
			for _, v := range values {
				addMember(&n, field, v)
			}
		}
		out.Nodes = append(out.Nodes, n)
		out.Problems = append(out.Problems, problems...)
	}
	for id, name := range names {
		out.Contributors = append(out.Contributors, state.Contributor{ID: id, Name: name})
	}
	return out, nil
}

// Hit is one full-text match.
type Hit struct {
	ID string

	// Score is BM25 with title, body and tags weighted 8, 1 and 5. Lower is
	// better.
	Score float64
}

// Search runs an FTS5 MATCH expression over notes and tasks, deleted ones
// included.
func Search(p *Project, match string) ([]Hit, error) {
	rows, err := p.db.Query(`SELECT (SELECT id FROM nodes WHERE num = nodes_fts.rowid), bm25(nodes_fts, 8.0, 1.0, 5.0)
		FROM nodes_fts WHERE nodes_fts MATCH ?`, match)
	if err != nil {
		return nil, fmt.Errorf("search %s: %w", p.Path, err)
	}
	defer rows.Close()
	var out []Hit
	for rows.Next() {
		var id sql.NullString
		var h Hit
		if err := rows.Scan(&id, &h.Score); err != nil {
			return nil, fmt.Errorf("search %s: %w", p.Path, err)
		}
		if id.Valid {
			h.ID = id.String
			out = append(out, h)
		}
	}
	return out, rows.Err()
}

// addLines appends each line of body that file does not already hold,
// creating the file when it is missing.
func addLines(file, body string) error {
	raw, err := os.ReadFile(file)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", file, err)
	}
	have := map[string]bool{}
	for _, l := range strings.Split(string(raw), "\n") {
		have[l] = true
	}
	var missing string
	for _, l := range strings.Split(strings.TrimRight(body, "\n"), "\n") {
		if !have[l] {
			missing += l + "\n"
		}
	}
	if missing == "" {
		return nil
	}
	if len(raw) > 0 && raw[len(raw)-1] != '\n' {
		missing = "\n" + missing
	}
	f, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("write %s: %w", file, err)
	}
	if _, err := f.WriteString(missing); err != nil {
		f.Close()
		return fmt.Errorf("write %s: %w", file, err)
	}
	return f.Close()
}
