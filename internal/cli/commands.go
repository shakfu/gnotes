package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"unicode/utf8"

	"github.com/shakfu/gwiki/internal/editor"
)

// flags builds a FlagSet that reports errors through the command's usage line
// rather than printing its own and calling os.Exit.
func (a *App) flags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	return fs
}

// parse reads arguments, allowing flags to appear after positional ones.
//
// The standard parser stops at the first non-flag argument, which is unusable
// here: almost every command takes its subject first, so "gwiki new 'a title'
// -t bug" would silently treat the flag as part of the title. Sorting
// the flags ahead of the positionals before parsing gives the behaviour people
// expect from every other tool.
//
// A literal "--" ends flag parsing, which is how to pass a positional that
// begins with a dash.
func parse(fs *flag.FlagSet, args []string) error {
	var flags, positional []string

	for i := 0; i < len(args); i++ {
		arg := args[i]

		if arg == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if len(arg) < 2 || arg[0] != '-' {
			positional = append(positional, arg)
			continue
		}

		flags = append(flags, arg)

		// A flag written as -x=value already carries its argument.
		name := strings.TrimLeft(arg, "-")
		if strings.ContainsRune(name, '=') {
			continue
		}
		// A non-boolean flag consumes the next argument, which must travel
		// with it rather than being left among the positionals.
		if f := fs.Lookup(name); f != nil && !isBoolFlag(f) && i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}

	// The "--" is passed on so that a positional beginning with a dash is not
	// read as a flag after all.
	if err := fs.Parse(append(append(flags, "--"), positional...)); err != nil {
		return fmt.Errorf("%w: %v", errUsage, err)
	}
	return nil
}

// isBoolFlag reports whether a flag stands alone, in the way flag.FlagSet
// itself determines it.
func isBoolFlag(f *flag.Flag) bool {
	b, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && b.IsBoolFlag()
}

// multiFlag collects a repeatable string flag.
type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

// maxStdin bounds a body read from standard input.
const maxStdin = 16 << 20

// readStdin reads a body from standard input.
//
// Text that is not UTF-8 is refused rather than written into a page. A
// terminal gets a hint, since otherwise the command appears to hang.
func (a *App) readStdin() (string, error) {
	if f, ok := a.Stdin.(*os.File); ok {
		if info, err := f.Stat(); err == nil && info.Mode()&os.ModeCharDevice != 0 {
			fmt.Fprintln(a.Stderr, "reading the body from the terminal; finish with ctrl-d")
		}
	}
	raw, err := io.ReadAll(io.LimitReader(a.Stdin, maxStdin+1))
	if err != nil {
		return "", fmt.Errorf("read standard input: %w", err)
	}
	switch {
	case len(raw) > maxStdin:
		return "", fmt.Errorf("standard input is larger than %d MiB", maxStdin>>20)
	case !utf8.Valid(raw):
		return "", errors.New("standard input is not UTF-8 text")
	}
	return string(raw), nil
}

// editInEditor opens the current body in the user's editor and returns what
// came back.
//
// Interrupts are ignored while the editor runs. The editor shares the
// terminal and receives ctrl-c itself; gwiki exiting on it would leave the
// temporary file, which holds the body, behind.
func (a *App) editInEditor(current string) (string, error) {
	edit, err := editor.Start(current, a.Env)
	if errors.Is(err, editor.ErrNoEditor) {
		return "", errors.New("no editor configured; set $EDITOR, or pass -m or --stdin")
	}
	if err != nil {
		return "", err
	}
	defer edit.Cleanup()

	edit.Cmd.Stdin, edit.Cmd.Stdout, edit.Cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	signal.Ignore(os.Interrupt)
	err = edit.Cmd.Run()
	signal.Reset(os.Interrupt)
	if err != nil {
		return "", fmt.Errorf("the editor exited with an error: %w", err)
	}
	return edit.Finish()
}

// helpCommand lists a table's commands, or shows one command's detail. It
// takes the table variable's address, since the table is built after the
// command.
func helpCommand(intro func(a *App), tableVar **commandTable) *command {
	return &command{
		name:    "help",
		args:    "[command]",
		summary: "show this help, or the detail for one command",
		run: func(a *App, args []string) error {
			t := *tableVar
			if len(args) > 0 {
				c, ok := t.byName[args[0]]
				if !ok {
					return fmt.Errorf("%w: unknown command %q", errUsage, args[0])
				}
				a.printf("usage: %s %s %s\n\n%s\n", t.prefix, c.name, c.args, c.summary)
				if c.help != "" {
					a.printf("\n%s\n", c.help)
				}
				if len(c.aliases) > 0 {
					a.printf("\naliases: %s\n", strings.Join(c.aliases, ", "))
				}
				return nil
			}

			intro(a)
			var out table
			for _, c := range t.list {
				name := c.name
				if len(c.aliases) > 0 {
					name += ", " + strings.Join(c.aliases, ", ")
				}
				out.addStyled([]string{name, c.summary}, []string{a.style(ansiBold, name), ""})
			}
			out.write(a.Stdout)
			a.printf("\nrun '%s help <command>' for detail.\n", t.prefix)
			return nil
		},
	}
}
