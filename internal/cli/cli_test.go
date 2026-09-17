package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// fixture drives the whole command line in-process: no subprocess and no
// terminal.
type fixture struct {
	t   *testing.T
	dir string
	now time.Time
}

// run executes one command and returns its streams and exit status.
func (f *fixture) run(args ...string) (stdout, stderr string, code int) {
	f.t.Helper()
	return f.runIn("", args...)
}

// runIn executes a command with something on standard input.
func (f *fixture) runIn(stdin string, args ...string) (string, string, int) {
	f.t.Helper()

	var out, errBuf bytes.Buffer
	app := &App{
		Stdout: &out,
		Stderr: &errBuf,
		Stdin:  strings.NewReader(stdin),
		Dir:    f.dir,
		Now:    func() time.Time { return f.now },
		Color:  false,
	}
	code := app.Run(args)
	return out.String(), errBuf.String(), code
}

// mustRun fails the test if the command does not succeed.
func (f *fixture) mustRun(args ...string) string {
	f.t.Helper()
	stdout, stderr, code := f.run(args...)
	if code != 0 {
		f.t.Fatalf("gwiki %s: exit %d\nstdout: %s\nstderr: %s",
			strings.Join(args, " "), code, stdout, stderr)
	}
	return stdout
}

func TestUnknownCommandSuggestsAndExitsTwo(t *testing.T) {
	f := wikiFixture(t)

	_, stderr, code := f.run("serach", "x")
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, "search") {
		t.Errorf("no suggestion offered: %s", stderr)
	}
}

func TestUsageErrorsExitTwo(t *testing.T) {
	f := wikiFixture(t)

	for _, args := range [][]string{
		{"new"},
		{"show"},
		{"tag", "index"},
		{"mv", "index"},
	} {
		_, stderr, code := f.run(args...)
		if code != 2 {
			t.Errorf("%v: exit = %d, want 2", args, code)
		}
		if !strings.Contains(stderr, "usage:") {
			t.Errorf("%v: no usage line: %s", args, stderr)
		}
	}
}

func TestHelp(t *testing.T) {
	f := wikiFixture(t)

	// Every command must have its own page, or help is a lie.
	for _, c := range wikiTable.list {
		out, stderr, code := f.run("help", c.name)
		if code != 0 || !strings.Contains(out, c.summary) || !strings.Contains(out, "usage: gwiki "+c.name) {
			t.Errorf("gwiki help %s: exit %d %s\n%s", c.name, code, stderr, out)
		}
	}
}

func TestVersion(t *testing.T) {
	f := wikiFixture(t)
	if out := f.mustRun("--version"); strings.TrimSpace(out) == "" {
		t.Fatal("--version printed nothing")
	}
}

// Output must have no trailing whitespace, so it diffs and copies cleanly.
func TestTableRowsHaveNoTrailingWhitespace(t *testing.T) {
	f := wikiFixture(t)

	for _, args := range [][]string{{"help"}, {"ls"}, {"tasks"}, {"check"}} {
		out, _, _ := f.run(args...)
		for i, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
			if line != strings.TrimRight(line, " \t") {
				t.Errorf("%v line %d has trailing whitespace: %q", args, i, line)
			}
		}
	}
}

func TestHelpFlagsAndUsageErrors(t *testing.T) {
	f := wikiFixture(t)

	stdout, _, code := f.run("tag", "x", "--help")
	if code != 0 || !strings.Contains(stdout, "usage: gwiki tag") {
		t.Fatalf("tag --help: exit %d, stdout %q", code, stdout)
	}
	if _, stderr, code := f.run("ls", "--bogus"); code != 2 || !strings.Contains(stderr, "bogus") {
		t.Fatalf("ls --bogus: exit %d, stderr %q; want 2 and the flag named", code, stderr)
	}
	if _, _, code := f.run("help", "nosuch"); code != 2 {
		t.Fatalf("help nosuch: exit %d, want 2 like an unknown command", code)
	}
}

func TestTyposGetASuggestion(t *testing.T) {
	for typed, want := range map[string]string{"shwo": "show", "sreach": "search", "tga": "tag"} {
		if got := wikiTable.closest(typed); got != want {
			t.Errorf("closest(%q) = %q, want %q", typed, got, want)
		}
	}
}

func TestStdinMustBeUTF8(t *testing.T) {
	f := wikiFixture(t)
	if _, stderr, code := f.runIn("bad \xff byte", "new", "binary", "--stdin"); code == 0 || !strings.Contains(stderr, "UTF-8") {
		t.Fatalf("exit %d, stderr %q; want non-UTF-8 input refused", code, stderr)
	}
}

// "--" ends flag parsing, so a title beginning with a dash is a title.
func TestDoubleDashPassesADashedTitle(t *testing.T) {
	f := wikiFixture(t)
	if out := f.mustRun("new", "--", "-x marks the spot"); !strings.Contains(out, "-x marks the spot") {
		t.Fatalf("the dashed title was not kept: %s", out)
	}
}
