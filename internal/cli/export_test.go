package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// populated returns a fixture with one of everything the exporter has to
// render, so each test below can ask a different question of the same project.
func populated(t *testing.T) *fixture {
	t.Helper()

	f := newFixture(t)
	f.mustRun("nb", "work")
	f.mustRun("note", "design sketch", "-b", "work", "-m", "it's a lexer")
	f.mustRun("task", "fix the lexer", "-b", "work", "-t", "bug", "-p", "high", "-d", "2026-08-21")
	f.mustRun("link", "fix the lexer", "design sketch")
	f.mustRun("untag", "fix the lexer", "bug")
	return f
}

func TestExportWritesSQLToStdout(t *testing.T) {
	f := populated(t)

	out := f.mustRun("export")
	for _, want := range []string{
		"CREATE TABLE nodes",
		"CREATE TABLE events",
		"CREATE TABLE node_history",
		"CREATE VIRTUAL TABLE nodes_fts",
		"INSERT INTO nodes",
		"BEGIN;",
		"COMMIT;",
		"design sketch",
		"it''s a lexer", // the apostrophe is escaped, not dropped
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the script is missing %q", want)
		}
	}
}

// Nothing but the script may reach standard output, or piping into sqlite3
// would feed it a status line.
func TestExportKeepsStdoutClean(t *testing.T) {
	f := populated(t)

	out, errOut, code := f.run("export")
	if code != 0 {
		t.Fatalf("export failed: %s", errOut)
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" || strings.HasPrefix(line, "--") {
			continue
		}
		// Everything else must be SQL: a statement, a continuation of one, or
		// a pragma. Anything conversational would be a bug.
		if strings.HasPrefix(line, "wrote ") || strings.HasPrefix(line, "note: ") {
			t.Errorf("a message reached standard output: %q", line)
		}
	}
}

func TestExportToFileReportsOnStderr(t *testing.T) {
	f := populated(t)

	path := filepath.Join(t.TempDir(), "notes.sql")
	out, errOut, code := f.run("export", "-o", path)
	if code != 0 {
		t.Fatalf("export failed: %s", errOut)
	}
	if out != "" {
		t.Errorf("writing to a file still wrote to standard output: %q", out)
	}
	if !strings.Contains(errOut, "wrote "+path) {
		t.Errorf("no confirmation on standard error: %q", errOut)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the exported file: %v", err)
	}
	if !strings.Contains(string(body), "CREATE TABLE nodes") {
		t.Error("the exported file holds no schema")
	}
}

func TestExportFlagsOmitTables(t *testing.T) {
	f := populated(t)

	out := f.mustRun("export", "--no-history", "--no-fts")
	for _, unwanted := range []string{"node_history", "nodes_fts", "nodes_text"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("a slim export still mentions %q", unwanted)
		}
	}
	if !strings.Contains(out, "CREATE TABLE nodes") {
		t.Error("the slim export dropped the tree as well")
	}
}

func TestExportRejectsPositionalArguments(t *testing.T) {
	f := populated(t)

	// A stray argument almost certainly means the user expected a filename to
	// be taken positionally, which would otherwise be silently ignored.
	_, errOut, code := f.run("export", "notes.db")
	if code != 2 {
		t.Fatalf("a stray argument was accepted (exit %d)", code)
	}
	if !strings.Contains(errOut, "usage: gnotes export") {
		t.Errorf("no usage line: %q", errOut)
	}
}

func TestExportOfAnEmptyProject(t *testing.T) {
	f := newFixture(t)

	out := f.mustRun("export")
	if !strings.Contains(out, "CREATE TABLE nodes") {
		t.Error("an empty project produced no schema")
	}
	loadExport(t, out)
}

// The end of the chain the help text promises: the script the command line
// produces is accepted by sqlite3 and answers a question gnotes cannot.
func TestExportedScriptLoadsAndAnswersQueries(t *testing.T) {
	f := populated(t)
	db := loadExport(t, f.mustRun("export"))

	if got := sqliteQuery(t, db, "SELECT title FROM nodes WHERE kind = 'task';"); got != "fix the lexer" {
		t.Errorf("task title: got %q", got)
	}
	if got := sqliteQuery(t, db, "SELECT priority_name FROM nodes WHERE kind = 'task';"); got != "high" {
		t.Errorf("priority: got %q", got)
	}
	if got := sqliteQuery(t, db, "SELECT count(*) FROM links;"); got != "1" {
		t.Errorf("links: got %q, want 1", got)
	}
	if got := sqliteQuery(t, db, "SELECT count(*) FROM tags;"); got != "0" {
		t.Errorf("the removed tag is still present (%s rows)", got)
	}

	// The tag was present earlier in the log, so the history has to remember
	// it even though the tree no longer does.
	if got := sqliteQuery(t, db, "SELECT count(*) FROM node_history WHERE field = 'tag' AND value = 'bug';"); got != "1" {
		t.Errorf("history lost the removed tag (%s rows)", got)
	}

	if got := sqliteQuery(t, db, `SELECT n.title FROM nodes_fts f
	    JOIN nodes n ON n.rowid_ = f.rowid WHERE nodes_fts MATCH 'lexer' AND n.kind = 'note';`); got != "design sketch" {
		t.Errorf("full text search over the body returned %q", got)
	}
}

// loadExport pipes a script into sqlite3 and returns the database path,
// skipping where sqlite3 is not installed.
func loadExport(t *testing.T, sql string) string {
	t.Helper()

	bin, err := exec.LookPath("sqlite3")
	if err != nil {
		t.Skip("sqlite3 is not installed; skipping the checks that need a database")
	}

	db := filepath.Join(t.TempDir(), "notes.db")
	cmd := exec.Command(bin, "-bail", db)
	cmd.Stdin = strings.NewReader(sql)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sqlite3 refused the script: %v\n%s", err, out)
	}
	return db
}

func sqliteQuery(t *testing.T, db, sql string) string {
	t.Helper()

	out, err := exec.Command("sqlite3", "-bail", db, sql).CombinedOutput()
	if err != nil {
		t.Fatalf("query %q: %v\n%s", sql, err, out)
	}
	return strings.TrimRight(string(out), "\n")
}
