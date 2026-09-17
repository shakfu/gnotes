// Package cli implements the gwiki command line.
//
// Every command is a thin shell over the wiki package: parse
// arguments, call one method, print the result. Nothing here decides domain
// rules, so the command line and the interactive interfaces cannot disagree
// about what an operation means.
package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// App holds everything a command needs from its environment. Passing it in
// rather than reaching for globals is what lets the tests drive the whole
// command line with no process and no terminal.
type App struct {
	Stdout io.Writer
	Stderr io.Writer
	Stdin  io.Reader

	// Dir is the working directory the project is discovered from.
	Dir string

	// Now is the clock, so relative dates are reproducible under test.
	Now func() time.Time

	// Env reads an environment variable. It is a field rather than a direct
	// call to os.Getenv so that a test can describe a machine it is not
	// running on, such as one reached over SSH.
	Env func(string) string

	// Color enables ANSI styling.
	Color bool
}

// New returns an App wired to the real process environment.
func New() *App {
	dir, err := os.Getwd()
	if err != nil {
		dir = "."
	}
	return &App{
		Stdout: os.Stdout,
		Stderr: os.Stderr,
		Stdin:  os.Stdin,
		Dir:    dir,
		Now:    time.Now,
		Env:    os.Getenv,
		Color:  useColor(),
	}
}

// useColor reports whether to style output: only on a terminal, and never when
// NO_COLOR is set.
func useColor() bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	info, err := os.Stdout.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// command is one verb.
type command struct {
	name    string
	aliases []string
	args    string
	summary string

	// help is the long description, printed by "gwiki help <name>".
	help string

	run func(*App, []string) error

	// runNamed replaces run for commands whose alias carries meaning, such as
	// "done" and "doing" both being the status command. It receives the name
	// the user actually typed.
	runNamed func(*App, string, []string) error
}

// A commandTable is a set of commands and the prefix of their usage lines.
type commandTable struct {
	// prefix is what precedes a command name in usage lines.
	prefix string
	list   []*command
	byName map[string]*command
}

func newTable(prefix string, list []*command) *commandTable {
	t := &commandTable{prefix: prefix, list: list, byName: map[string]*command{}}
	for _, c := range list {
		t.byName[c.name] = c
		for _, a := range c.aliases {
			t.byName[a] = c
		}
	}
	return t
}

// wikiTable is filled in init, since its help command refers back to it.
var wikiTable *commandTable

func init() {
	wikiTable = newTable("gwiki", []*command{
		cmdWikiInit, cmdWikiList, cmdWikiShow, cmdWikiSearch, cmdWikiLinks, cmdWikiBacklinks,
		cmdWikiCheck, cmdWikiOrphans, cmdWikiTasks,
		cmdWikiNew, cmdWikiEdit, cmdWikiMove, cmdWikiRemove, cmdWikiExport, cmdWikiTag, cmdWikiUntag,
		cmdWikiDone, cmdWikiDoing, cmdWikiReopen, cmdWikiPromote,
		cmdWikiUI, cmdWikiServe, cmdWikiMCP, cmdWikiLSP, cmdWikiCache,
		helpCommand(wikiHelp, &wikiTable),
	})
}

// Run dispatches one invocation and returns the process exit status.
func (a *App) Run(args []string) int {
	if a.Env == nil {
		a.Env = os.Getenv
	}
	return a.dispatch(wikiTable, args)
}

func (a *App) dispatch(t *commandTable, args []string) int {
	if len(args) == 0 {
		// A bare gwiki opens the interface.
		args = []string{"ui"}
	}

	name := args[0]
	if name == "-h" || name == "--help" {
		name = "help"
	}
	if name == "--version" || name == "-v" {
		fmt.Fprintln(a.Stdout, Version)
		return 0
	}

	c, ok := t.byName[name]
	if !ok {
		fmt.Fprintf(a.Stderr, "gwiki: unknown command %q\n", name)
		if suggestion := t.closest(name); suggestion != "" {
			fmt.Fprintf(a.Stderr, "did you mean %q?\n", suggestion)
		}
		fmt.Fprintf(a.Stderr, "run '%s help' for the list\n", t.prefix)
		return 2
	}

	// Help for a command, wherever the flag appears before "--". Commands that
	// take free words would otherwise treat it as one, tagging an entry
	// "--help".
	for _, arg := range args[1:] {
		if arg == "--" {
			break
		}
		if arg == "-h" || arg == "--help" {
			return a.dispatch(t, []string{"help", c.name})
		}
	}

	run := c.run
	if c.runNamed != nil {
		run = func(a *App, rest []string) error { return c.runNamed(a, name, rest) }
	}

	if err := run(a, args[1:]); err != nil {
		if errors.Is(err, errUsage) {
			// A detail beyond the bare usage error, such as the flag package's
			// "flag provided but not defined", is shown first.
			if detail := strings.TrimPrefix(err.Error(), errUsage.Error()+": "); err != errUsage && detail != "" {
				fmt.Fprintf(a.Stderr, "gwiki: %s\n", detail)
			}
			fmt.Fprintf(a.Stderr, "usage: %s %s %s\n", t.prefix, c.name, c.args)
			return 2
		}
		fmt.Fprintf(a.Stderr, "gwiki: %v\n", err)
		return 1
	}
	return 0
}

// Version is the build version, overridden at link time.
var Version = "dev"

// errUsage signals a malformed invocation, which prints the usage line rather
// than an error message.
var errUsage = errors.New("usage")

// closest suggests a command for a near miss, using edit distance bounded by a
// third of the typed length so that unrelated words are not "corrected".
func (t *commandTable) closest(typed string) string {
	best, bestDist := "", len(typed)/3+1

	for name := range t.byName {
		d := distance(typed, name)
		if d > bestDist {
			continue
		}
		// Ties go to the name nearest the typed length, since a typo is
		// usually a swapped or doubled letter rather than a lost word, then
		// alphabetically, so map order cannot decide.
		gap, bestGap := abs(len(name)-len(typed)), abs(len(best)-len(typed))
		if d < bestDist || best == "" || gap < bestGap || (gap == bestGap && name < best) {
			best, bestDist = name, d
		}
	}
	return best
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// distance is Levenshtein edit distance over two short strings.
func distance(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}

	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(min(cur[j-1]+1, prev[j]+1), prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// printf writes to standard output.
func (a *App) printf(format string, args ...any) {
	fmt.Fprintf(a.Stdout, format, args...)
}
