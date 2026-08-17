// Package cli implements the gnotes command line.
//
// Every command is a thin shell over the session package: parse arguments,
// call one method, print the result. Nothing here decides domain rules, so the
// command line and the interactive interface cannot disagree about what an
// operation means.
package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/shakfu/gnotes/internal/session"
	"github.com/shakfu/gnotes/internal/store"
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

	// help is the long description, printed by "gnotes help <name>".
	help string

	run func(*App, []string) error

	// runNamed replaces run for commands whose alias carries meaning, such as
	// "done" and "doing" both being the status command. It receives the name
	// the user actually typed.
	runNamed func(*App, string, []string) error
}

// commands is the dispatch table, in the order help prints them.
var commands []*command

// byName indexes commands and their aliases.
var byName = map[string]*command{}

func init() {
	commands = []*command{
		cmdInit, cmdNotebook, cmdNote, cmdTask,
		cmdList, cmdShow, cmdSearch, cmdTags,
		cmdEdit, cmdStatus, cmdDue, cmdPriority,
		cmdTag, cmdUntag, cmdAssign, cmdUnassign,
		cmdLink, cmdUnlink, cmdMove, cmdRemove, cmdRestore,
		cmdLog, cmdSync, cmdInfo, cmdWho, cmdUI, cmdHelp,
	}
	for _, c := range commands {
		byName[c.name] = c
		for _, a := range c.aliases {
			byName[a] = c
		}
	}
}

// Run dispatches one invocation and returns the process exit status.
func (a *App) Run(args []string) int {
	if len(args) == 0 {
		// A bare invocation opens the interactive interface, which is the
		// usual way to reach for a notes tool.
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

	c, ok := byName[name]
	if !ok {
		fmt.Fprintf(a.Stderr, "gnotes: unknown command %q\n", name)
		if suggestion := closest(name); suggestion != "" {
			fmt.Fprintf(a.Stderr, "did you mean %q?\n", suggestion)
		}
		fmt.Fprintln(a.Stderr, "run 'gnotes help' for the list")
		return 2
	}

	run := c.run
	if c.runNamed != nil {
		run = func(a *App, rest []string) error { return c.runNamed(a, name, rest) }
	}

	if err := run(a, args[1:]); err != nil {
		if errors.Is(err, errUsage) {
			fmt.Fprintf(a.Stderr, "usage: gnotes %s %s\n", c.name, c.args)
			return 2
		}
		fmt.Fprintf(a.Stderr, "gnotes: %v\n", err)
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
func closest(typed string) string {
	best, bestDist := "", len(typed)/3+1

	for name := range byName {
		if d := distance(typed, name); d <= bestDist {
			// Ties go to the shorter name, which is the canonical one more
			// often than not.
			if d < bestDist || len(name) < len(best) {
				best, bestDist = name, d
			}
		}
	}
	return best
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

// open loads the project containing the working directory, along with the
// configured identity.
func (a *App) open() (*session.Session, error) {
	actor, err := store.LoadUser()
	if err != nil {
		return nil, err
	}

	s, err := session.Open(a.Dir, actor)
	if err != nil {
		return nil, err
	}
	s.SetClock(a.Now)

	// Events written by a newer build are stepped over rather than applied.
	// Saying so once is the difference between a confusing absence and a
	// known one.
	if len(s.Skipped) > 0 {
		var parts []string
		for action, n := range s.Skipped {
			parts = append(parts, fmt.Sprintf("%s x%d", action, n))
		}
		sort.Strings(parts)
		fmt.Fprintf(a.Stderr, "note: skipped events this version does not understand (%s); upgrade gnotes to apply them\n",
			strings.Join(parts, ", "))
	}
	return s, nil
}

// commit writes the events staged by a command.
func (a *App) commit(s *session.Session) error { return s.Commit() }

// printf writes to standard output.
func (a *App) printf(format string, args ...any) {
	fmt.Fprintf(a.Stdout, format, args...)
}

// warnProblems reports events that could not be applied. They are not fatal,
// but silently dropping them would leave the user wondering where their note
// went.
func (a *App) warnProblems(s *session.Session) {
	const show = 3
	for i, p := range s.Problems {
		if i == show {
			fmt.Fprintf(a.Stderr, "note: and %d more unapplied events\n", len(s.Problems)-show)
			break
		}
		fmt.Fprintf(a.Stderr, "note: %s\n", p)
	}
}
