package cli

import (
	"bufio"
	"fmt"
	"os"

	"github.com/shakfu/gnotes/internal/sqlexport"
)

var cmdExport = &command{
	name:    "export",
	aliases: []string{"sql"},
	args:    "[-o <file>] [--no-history] [--no-fts]",
	summary: "render the project as a SQL script",
	help: `Writes a SQL script that builds a SQLite database holding the whole
project: the tree, the tags, the links, the raw event log, and a history table
recording every value each field has ever held.

    gnotes export | sqlite3 notes.db
    gnotes export -o notes.sql

The script is emitted rather than the database itself, so gnotes needs no
database driver and the binary is not a megabyte larger for a command most
people will never run. Anything that reads SQLite can then read the result:

    sqlite3 notes.db "SELECT title, due FROM nodes WHERE status = 'open'"
    duckdb -c "ATTACH 'notes.db' AS n (TYPE sqlite); SELECT * FROM n.events"

The database is derived and disposable. The event logs remain the only source
of truth, so a stale copy is fixed by exporting again, never by editing it.
Exporting the same log twice produces the same bytes.

The script ends with worked queries for the questions the command line cannot
answer: what was open on a given date, how long tasks take, which tags occur
together.

--no-history drops the point-in-time table, which is about one row per event.
--no-fts drops the full text index, for a SQLite built without FTS5.`,
	run: func(a *App, args []string) error {
		fs := a.flags("export")
		out := fs.String("o", "", "write to a file instead of standard output")
		noHistory := fs.Bool("no-history", false, "omit the point-in-time history table")
		noFTS := fs.Bool("no-fts", false, "omit the full text index")
		if err := parse(fs, args); err != nil {
			return errUsage
		}
		if fs.NArg() > 0 {
			return errUsage
		}

		s, err := a.open()
		if err != nil {
			return err
		}
		a.warnProblems(s)

		w := a.Stdout
		if *out != "" {
			f, err := os.Create(*out)
			if err != nil {
				return fmt.Errorf("create %s: %w", *out, err)
			}
			defer f.Close()
			w = f
		}

		// The encoder buffers internally, but a file handle written a line at
		// a time would still cost a syscall per statement on a long log.
		buf := bufio.NewWriterSize(w, 64<<10)
		if err := sqlexport.Write(buf, s.State, s.Log(), sqlexport.Options{
			Project: s.Project.Config.Name,
			Version: Version,
			Now:     a.Now(),
			History: !*noHistory,
			FTS:     !*noFTS,
		}); err != nil {
			return err
		}
		if err := buf.Flush(); err != nil {
			return err
		}

		// A pipe into sqlite3 says nothing on success, so the count goes to
		// standard error where it cannot corrupt the script.
		if *out != "" {
			fmt.Fprintf(a.Stderr, "wrote %s (%d events)\n", *out, len(s.Log()))
		}
		return nil
	},
}
