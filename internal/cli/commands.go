package cli

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/shakfu/gwiki/internal/editor"
	"github.com/shakfu/gwiki/internal/rank"
	"github.com/shakfu/gwiki/internal/search"
	"github.com/shakfu/gwiki/internal/session"
	"github.com/shakfu/gwiki/internal/state"
	"github.com/shakfu/gwiki/internal/store"
	"github.com/shakfu/gwiki/internal/ulid"
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
// here: almost every command takes its subject first, so "gwiki notes note 'a
// title' -t bug" would silently treat the flag as part of the title. Sorting
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

// ---------------------------------------------------------------- init

var cmdInit = &command{
	name:    "init",
	args:    "[name] [--user <you>]",
	summary: "create a project here, and set your name the first time",
	help: `Creates .gwiki/notes.db in the current directory, a SQLite database
for you to commit. The name defaults to the directory's own.

The first time you run it anywhere, it also records your name and mints the
identity that every change you make is attributed to. That identity is stored
outside the project, so it follows you across all of them.

'gwiki notes -g init [dir]' sets up the global notes instead: a project that
belongs to you rather than to a repository, reached from anywhere with -g. It
is created in dir, or ~/notes, and its location is recorded. A project already
in dir, such as a clone from another machine, is used as it is.`,
	run: func(a *App, args []string) error {
		fs := a.flags("init")
		user := fs.String("user", "", "your display name")
		if err := parse(fs, args); err != nil {
			return err
		}

		actor, err := store.LoadUser()
		if err != nil {
			return err
		}

		name := strings.Join(fs.Args(), " ")
		if a.Global {
			dir, p, err := loadGlobal()
			if err != nil {
				return err
			}
			if p != nil {
				a.printf("global notes location already exists: %s\n", dir)
				return nil
			}
			if fs.NArg() > 1 {
				return fmt.Errorf("%w: give one directory for the global notes", errUsage)
			}
			// A recorded location whose project has gone is set up again in
			// place, unless another directory is given.
			switch {
			case fs.NArg() == 1:
				dir = fs.Arg(0)
				if !filepath.IsAbs(dir) {
					dir = filepath.Join(a.Dir, dir)
				}
			case dir == "":
				home, err := os.UserHomeDir()
				if err != nil {
					return err
				}
				dir = filepath.Join(home, "notes")
			}
			a.Dir, name = filepath.Clean(dir), "global"
		}
		here := resolvedPath(a.Dir)

		// Everything that can refuse is checked before the identity is saved,
		// so a refused init changes nothing.
		existing, err := store.Discover(a.Dir)
		switch {
		case errors.Is(err, store.ErrNotFound):
			existing = nil
		case err != nil:
			return err
		}

		if existing != nil && resolvedPath(existing.Root) != here {
			// A project above, in the same repository, would be shadowed for
			// every command run below the new one.
			if top, inGit := store.GitRoot(a.Dir); !inGit || within(resolvedPath(existing.Root), resolvedPath(top)) {
				return fmt.Errorf("a notes database already covers this directory, at %s", existing.Root)
			}
			existing = nil
		}

		if existing != nil {
			s, err := session.OpenProject(existing, actor)
			if err != nil {
				return err
			}
			if s.State.Workspace != "" {
				if actor.Valid() {
					if *user != "" {
						return errors.New("this project is already initialised; change your name with 'gwiki notes whoami --set'")
					}
					if !a.Global {
						return errors.New("this project is already initialised")
					}
				} else if _, err := a.saveIdentity(actor, *user); err != nil {
					// A fresh clone of someone else's project: only the
					// identity is missing, and setting it is all there is to do.
					return err
				}
				if a.Global {
					return a.saveGlobal(existing)
				}
				a.printf("this project is already initialised; you can start writing\n")
				return nil
			}
		}

		if *user != "" || !actor.Valid() {
			if actor, err = a.saveIdentity(actor, *user); err != nil {
				return err
			}
		}

		p := existing
		if p == nil {
			if p, err = store.Init(a.Dir, name, a.Now()); err != nil {
				return err
			}
		}

		s, err := session.OpenProject(p, actor)
		if err != nil {
			return err
		}
		s.SetClock(a.Now)

		if s.State.Workspace == "" {
			if err := s.Init(p.Config.Name); err != nil {
				return err
			}
			if err := s.Commit(); err != nil {
				return err
			}
		}
		a.printf("created %s\n", p.Path)
		if a.Global {
			if err := a.saveGlobal(p); err != nil {
				return err
			}
		}
		if top, inGit := store.GitRoot(a.Dir); inGit && resolvedPath(top) != here {
			a.printf("%s\n", a.style(ansiDim, "note: the repository's root is "+top+"; notes created here are only found from this directory down"))
		}

		g := ""
		if a.Global {
			g = "-g "
		}
		a.printf("\nnext: gwiki notes %snote \"a first note\"  or  gwiki notes %stask \"a first task\"\n", g, g)
		return nil
	},
}

// saveGlobal records p as the global notes project.
func (a *App) saveGlobal(p *store.Project) error {
	if err := store.SaveGlobal(p.Root); err != nil {
		return err
	}
	a.printf("global notes: %s\n", p.Root)
	return nil
}

// saveIdentity stores the user's name, asking for it when none was given.
func (a *App) saveIdentity(actor store.Actor, name string) (store.Actor, error) {
	if name == "" {
		var err error
		if name, err = a.prompt("Your name: "); err != nil {
			return actor, err
		}
	}
	actor.Name = name
	actor, err := store.SaveUser(actor)
	if err != nil {
		return actor, err
	}
	a.printf("identity: %s\n", actor.Name)
	return actor, nil
}

// resolvedPath returns an absolute path with symlinks followed where possible,
// so two spellings of one directory compare equal.
func resolvedPath(dir string) string {
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	return dir
}

// within reports whether path is dir or below it.
func within(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// prompt reads one line from standard input.
func (a *App) prompt(question string) (string, error) {
	fmt.Fprint(a.Stdout, question)
	line, err := bufio.NewReader(a.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("could not read a name: %w", err)
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return "", errors.New("a name is required")
	}
	return line, nil
}

// ---------------------------------------------------------------- create

var cmdNotebook = &command{
	name:    "notebook",
	aliases: []string{"nb"},
	args:    "[name]",
	summary: "create a notebook, or list them all",
	run: func(a *App, args []string) error {
		s, err := a.open()
		if err != nil {
			return err
		}

		if len(args) == 0 {
			nbs := s.State.Notebooks()
			if len(nbs) == 0 {
				fmt.Fprintln(a.Stdout, "no notebooks yet")
				return nil
			}
			var t table
			for _, nb := range nbs {
				kids := s.State.Children(nb.ID)
				open := 0
				for _, k := range kids {
					if k.Kind == state.KindTask && k.Status != state.StatusDone {
						open++
					}
				}
				t.addStyled(
					[]string{ref(nb), nb.Title, fmt.Sprintf("%d entries", len(kids)), fmt.Sprintf("%d open", open)},
					[]string{a.style(ansiDim, ref(nb)), a.style(ansiBold, nb.Title), "", a.style(ansiDim, "")},
				)
			}
			t.write(a.Stdout)
			return nil
		}

		nb, err := s.NewNotebook(strings.Join(args, " "))
		if err != nil {
			return err
		}
		if err := a.commit(s); err != nil {
			return err
		}
		a.printf("%s  notebook %q\n", ref(nb), nb.Title)
		return nil
	},
}

var cmdNote = &command{
	name:    "note",
	aliases: []string{"n"},
	args:    "<title> [-b <notebook>] [-t <tag>]... [-m <body>] [--stdin]",
	summary: "write a note",
	help: `Creates a note in a notebook. Without -b it goes to the first
notebook, creating one called "inbox" if the project has none.

The body can be given with -m, or piped in with --stdin, which is the usual way
to capture something longer:

    git log --oneline -20 | gwiki notes note "release notes" --stdin`,
	run: func(a *App, args []string) error { return a.createEntry(args, false) },
}

var cmdTask = &command{
	name:    "task",
	aliases: []string{"t"},
	args:    "<title> [-b <notebook>] [-t <tag>]... [-d <due>] [-p <priority>] [-a <who>]",
	summary: "add a task",
	help: `Creates a task, which is a note that also carries a status, a
priority, a due date and assignees.

The due date accepts a plain date or a relative word:

    gwiki notes task "ship the parser" -d friday -p high -a me`,
	run: func(a *App, args []string) error { return a.createEntry(args, true) },
}

// createEntry backs both "note" and "task", which differ only in the fields a
// task additionally accepts.
func (a *App) createEntry(args []string, isTask bool) error {
	kind := "note"
	if isTask {
		kind = "task"
	}
	fs := a.flags(kind)

	notebook := fs.String("b", "", "notebook")
	body := fs.String("m", "", "body text")
	stdin := fs.Bool("stdin", false, "read the body from standard input")
	var tags multiFlag
	fs.Var(&tags, "t", "tag (repeatable)")

	var due, priority string
	var assignees multiFlag
	if isTask {
		fs.StringVar(&due, "d", "", "due date")
		fs.StringVar(&priority, "p", "", "priority: low, normal or high")
		fs.Var(&assignees, "a", "assignee (repeatable)")
	}

	if err := parse(fs, args); err != nil {
		return err
	}
	title := strings.Join(fs.Args(), " ")
	if title == "" {
		return errUsage
	}

	text := *body
	if *stdin {
		piped, err := a.readStdin()
		if err != nil {
			return err
		}
		if text != "" {
			text += "\n\n"
		}
		text += strings.TrimRight(piped, "\n")
	}

	s, err := a.open()
	if err != nil {
		return err
	}

	target := *notebook
	if target == "" {
		nb, err := s.DefaultNotebook()
		if err != nil {
			return err
		}
		target = nb.ID
	}

	var n *state.Node
	if isTask {
		n, err = s.NewTask(target, title, text)
	} else {
		n, err = s.NewNote(target, title, text)
	}
	if err != nil {
		return err
	}

	for _, tag := range tags {
		if err := s.AddTag(n, tag); err != nil {
			return err
		}
	}
	if isTask {
		if err := a.applyTaskFlags(s, n, due, priority, assignees); err != nil {
			return err
		}
	}
	if err := a.commit(s); err != nil {
		return err
	}

	a.printf("%s  %s %q\n", ref(n), kind, n.Title)
	return nil
}

// applyTaskFlags sets the optional task fields given on a create or edit.
func (a *App) applyTaskFlags(s *session.Session, n *state.Node, due, priority string, assignees []string) error {
	if due != "" {
		if err := s.SetDue(n, due); err != nil {
			return err
		}
	}
	if priority != "" {
		p, ok := state.ParsePriority(priority)
		if !ok {
			return fmt.Errorf("unknown priority %q; use low, normal or high", priority)
		}
		if err := s.SetPriority(n, p); err != nil {
			return err
		}
	}
	for _, who := range assignees {
		if err := s.Assign(n, who); err != nil {
			return err
		}
	}
	return nil
}

// multiFlag collects a repeatable string flag.
type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

// ---------------------------------------------------------------- read

var cmdList = &command{
	name:    "ls",
	aliases: []string{"list"},
	args:    "[-b <notebook>] [-t <tag>]... [-s <status>] [-p <priority>] [-a <who>] [-k <kind>] [--overdue] [--sort <order>] [--at <when>] [--json]",
	summary: "list notes and tasks",
	help: `Lists the project's notes and tasks, newest arrangement first.

Filters combine, so this shows only the open, high-priority tasks tagged bug:

    gwiki notes ls -k task -s open -p high -t bug

--at replays the recorded changes up to a past moment and lists the project as
it stood then. It
reads a date, a timestamp, or a duration ago. A date means the start of that
day in your time zone:

    gwiki notes ls --at 2026-08-01
    gwiki notes ls --at 3d`,
	run: func(a *App, args []string) error {
		fs := a.flags("ls")
		notebook := fs.String("b", "", "notebook")
		statusFlag := fs.String("s", "", "status: open, doing or done")
		priorityFlag := fs.String("p", "", "priority")
		assignee := fs.String("a", "", "assignee")
		kindFlag := fs.String("k", "", "kind: note or task")
		order := fs.String("sort", "", "order: rank, created, updated, title, due or priority")
		at := fs.String("at", "", "list the project as it stood then")
		overdue := fs.Bool("overdue", false, "only tasks past their due date")
		all := fs.Bool("all", false, "include deleted entries")
		asJSON := fs.Bool("json", false, "machine-readable output")
		var tags multiFlag
		fs.Var(&tags, "t", "tag (repeatable)")

		if err := parse(fs, args); err != nil {
			return err
		}

		s, err := a.open()
		if err != nil {
			return err
		}
		a.warnProblems(s)

		view := s.State
		listNow := a.Now()
		if *at != "" {
			cutoff, err := parseWhen(*at, a.Now())
			if err != nil {
				return err
			}
			if view, err = s.At(cutoff); err != nil {
				return err
			}
			// Overdue as of then, not as of now.
			listNow = cutoff
			if !*asJSON {
				a.printf("%s\n", a.style(ansiDim, "as of "+cutoff.Local().Format("2006-01-02 15:04")))
			}
		}

		f := state.Filter{
			Tags:           tags,
			Text:           strings.Join(fs.Args(), " "),
			Overdue:        *overdue,
			IncludeDeleted: *all,
			Now:            listNow,
		}
		if *notebook != "" {
			nb, err := view.Resolve(*notebook, state.KindNotebook)
			if err != nil {
				return err
			}
			f.Notebook = nb.ID
		}
		if *kindFlag != "" {
			k, ok := state.ParseKind(*kindFlag)
			if !ok {
				return fmt.Errorf("unknown kind %q; use note or task", *kindFlag)
			}
			f.Kinds = []state.Kind{k}
		}
		if *statusFlag != "" {
			st, ok := state.ParseStatus(*statusFlag)
			if !ok {
				return fmt.Errorf("unknown status %q; use open, doing or done", *statusFlag)
			}
			f.Status = &st
		}
		if *priorityFlag != "" {
			p, ok := state.ParsePriority(*priorityFlag)
			if !ok {
				return fmt.Errorf("unknown priority %q; use low, normal or high", *priorityFlag)
			}
			f.Priority = &p
		}
		if *assignee != "" {
			id, ok := view.FindContributor(*assignee)
			if !ok && !strings.EqualFold(*assignee, "me") {
				return fmt.Errorf("nobody named %q in this project", *assignee)
			}
			if strings.EqualFold(*assignee, "me") {
				id = s.Actor.ID
			}
			f.Assignee = id
		}

		ord, ok := state.ParseOrder(*order)
		if !ok {
			return fmt.Errorf("unknown sort %q", *order)
		}

		nodes := view.List(f, ord)
		if *asJSON {
			out := make([]jsonNode, len(nodes))
			for i, n := range nodes {
				out[i] = toJSON(view, n)
			}
			return a.writeJSON(out)
		}
		a.listNodes(view, nodes, f.Notebook == "", listNow)
		return nil
	},
}

var cmdShow = &command{
	name:    "show",
	aliases: []string{"cat"},
	args:    "<ref> [--json]",
	summary: "show one note or task in full",
	run: func(a *App, args []string) error {
		fs := a.flags("show")
		asJSON := fs.Bool("json", false, "machine-readable output")
		if err := parse(fs, args); err != nil {
			return err
		}
		if fs.NArg() == 0 {
			return errUsage
		}

		s, err := a.open()
		if err != nil {
			return err
		}
		n, err := s.State.Resolve(strings.Join(fs.Args(), " "))
		if err != nil {
			return err
		}

		if *asJSON {
			return a.writeJSON(toJSON(s.State, n))
		}
		a.showNode(s.State, n, s.State.Path(n))

		if back := s.State.Backlinks(n.ID); len(back) > 0 {
			a.printf("\n%s\n", a.style(ansiBold, "referenced by"))
			a.listNodes(s.State, back, true, a.Now())
		}
		return nil
	},
}

var cmdSearch = &command{
	name:    "search",
	aliases: []string{"s", "find"},
	args:    "<query> [-n <limit>] [--json]",
	summary: "search titles, bodies and tags",
	help: `Searches the full text of every note and task.

All the words must match, and the last one matches by prefix, so a partial word
still finds things. Results are ranked, with title matches well above body
matches.`,
	run: func(a *App, args []string) error {
		fs := a.flags("search")
		limit := fs.Int("n", 20, "maximum results")
		asJSON := fs.Bool("json", false, "machine-readable output")
		if err := parse(fs, args); err != nil {
			return err
		}
		query := strings.Join(fs.Args(), " ")
		if strings.TrimSpace(query) == "" {
			return errUsage
		}

		s, err := a.open()
		if err != nil {
			return err
		}

		results, err := s.Search(query, *limit, false)
		if err != nil {
			return err
		}

		if *asJSON {
			out := make([]jsonNode, len(results))
			for i, n := range results {
				out[i] = toJSON(s.State, n)
			}
			return a.writeJSON(out)
		}
		if len(results) == 0 {
			a.printf("nothing matches %q\n", query)
			return nil
		}

		for _, n := range results {
			a.listNodes(s.State, []*state.Node{n}, true, a.Now())
			if snippet := search.Snippet(n, query, 100); snippet != "" {
				a.printf("    %s\n", a.style(ansiDim, snippet))
			}
		}
		return nil
	},
}

var cmdTags = &command{
	name:    "tags",
	args:    "",
	summary: "list the tags in use, most used first",
	run: func(a *App, args []string) error {
		s, err := a.open()
		if err != nil {
			return err
		}
		tags := s.State.Tags()
		if len(tags) == 0 {
			fmt.Fprintln(a.Stdout, "no tags yet")
			return nil
		}
		var t table
		for _, tc := range tags {
			t.addStyled(
				[]string{"#" + tc.Tag, plural(tc.Count, "entry")},
				[]string{a.style(ansiCyan, "#"+tc.Tag), a.style(ansiDim, "")},
			)
		}
		t.write(a.Stdout)
		return nil
	},
}

// maxStdin bounds a body read from standard input. A body is one line of the
// log, and every load reads the whole log.
const maxStdin = 16 << 20

// readStdin reads a body from standard input.
//
// Text that is not UTF-8 is refused rather than stored: JSON encoding would
// replace each bad byte, so the body would silently differ from the input, and
// feeding the same input again would write another event. A terminal gets a
// hint, since otherwise the command appears to hang.
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

// ---------------------------------------------------------------- modify

// withNode resolves a reference, runs an action against it, commits, and
// prints a one-line confirmation. Nearly every mutating command is this shape.
func (a *App) withNode(refStr string, action func(*session.Session, *state.Node) error, format string) error {
	if strings.TrimSpace(refStr) == "" {
		return errUsage
	}
	s, err := a.open()
	if err != nil {
		return err
	}
	n, err := s.State.Resolve(refStr)
	if err != nil {
		return err
	}
	if err := action(s, n); err != nil {
		return err
	}
	if err := a.commit(s); err != nil {
		return err
	}
	if format != "" {
		a.printf(format+"\n", ref(n), n.Title)
	}
	return nil
}

// splitRef divides arguments into an entry reference and the words after it,
// for commands whose reference may be several unquoted words.
//
// Every split is tried. restOK says whether the words after a split make sense
// for the command, and a split counts only when its reference resolves too.
// Exactly one such split is used; several are refused, since "tag fix the
// lexer bug" could mean tagging "fix the lexer" with bug or "fix" with three
// tags, and guessing wrong changes an entry the user did not name. With no
// valid split the first word is taken as the reference, which reproduces the
// resolver's or the command's own error.
func splitRef(s *session.Session, args []string, restOK func(rest []string) bool, kinds ...state.Kind) (*state.Node, []string, error) {
	var (
		found *state.Node
		rest  []string
		ways  int
	)
	for k := 1; k <= len(args); k++ {
		if !restOK(args[k:]) {
			continue
		}
		n, err := s.State.Resolve(strings.Join(args[:k], " "), kinds...)
		if err != nil {
			continue
		}
		found, rest = n, args[k:]
		ways++
	}

	switch ways {
	case 1:
		return found, rest, nil
	case 0:
		n, err := s.State.Resolve(args[0], kinds...)
		return n, args[1:], err
	default:
		return nil, nil, fmt.Errorf("%q can be read more than one way; quote the entry's title or use its handle", strings.Join(args, " "))
	}
}

// withSplitNode is withNode for commands that take a reference followed by
// further words. See splitRef.
func (a *App) withSplitNode(args []string, restOK func(*session.Session, []string) bool, action func(*session.Session, *state.Node, []string) error, format string) error {
	if len(args) < 2 {
		return errUsage
	}
	s, err := a.open()
	if err != nil {
		return err
	}
	n, rest, err := splitRef(s, args, func(rest []string) bool { return restOK(s, rest) })
	if err != nil {
		return err
	}
	if err := action(s, n, rest); err != nil {
		return err
	}
	if err := a.commit(s); err != nil {
		return err
	}
	a.printf(format+"\n", ref(n), n.Title)
	return nil
}

// someWords accepts any non-empty remainder.
func someWords(_ *session.Session, rest []string) bool { return len(rest) > 0 }

var cmdEdit = &command{
	name:    "edit",
	args:    "<ref> [--title <text>] [-m <body>] [--stdin]",
	summary: "change a title or body",
	help: `With no flags, opens the body in $EDITOR and saves what you write.

    gwiki notes edit lexer --title "fix the lexer properly"
    gwiki notes edit lexer -m "a short body"
    cat notes.md | gwiki notes edit lexer --stdin`,
	run: func(a *App, args []string) error {
		fs := a.flags("edit")
		title := fs.String("title", "", "new title")
		body := fs.String("m", "", "new body")
		stdin := fs.Bool("stdin", false, "read the body from standard input")
		if err := parse(fs, args); err != nil {
			return err
		}
		if fs.NArg() == 0 {
			return errUsage
		}

		// Set rather than non-empty: -m "" clears the body, and --title "" is
		// refused with a reason instead of opening the editor.
		given := map[string]bool{}
		fs.Visit(func(f *flag.Flag) { given[f.Name] = true })

		return a.withNode(strings.Join(fs.Args(), " "), func(s *session.Session, n *state.Node) error {
			if given["title"] {
				if err := s.SetTitle(n, *title); err != nil {
					return err
				}
			}
			switch {
			case *stdin:
				piped, err := a.readStdin()
				if err != nil {
					return err
				}
				return s.SetBody(n, strings.TrimRight(piped, "\n"))
			case given["m"]:
				return s.SetBody(n, *body)
			case !given["title"]:
				// Nothing was specified, so the intent is to edit the body
				// interactively.
				edited, err := a.editInEditor(n.Body)
				if err != nil {
					return err
				}
				return s.SetBody(n, edited)
			}
			return nil
		}, "%s  updated %q")
	},
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

var cmdStatus = &command{
	name:    "status",
	aliases: []string{"done", "doing", "reopen"},
	args:    "<ref> [status]",
	summary: "move a task to open, doing or done",
	help: `Sets a task's status. The command name doubles as the status, so
these are the same:

    gwiki notes done lexer
    gwiki notes status lexer done

"reopen" sets it back to open.`,
	runNamed: statusRun,
}

// statusRun handles the status command and its aliases. The alias carries the
// intent, so "gwiki notes done x" needs no further argument.
func statusRun(a *App, invoked string, args []string) error {
	want := ""
	switch invoked {
	case "done", "doing":
		want = invoked
	case "reopen":
		want = "open"
	}

	// The status is always one word, so the reference is everything else.
	if len(args) == 0 {
		return errUsage
	}
	refStr := strings.Join(args, " ")
	if want == "" {
		if len(args) < 2 {
			return errUsage
		}
		refStr, want = strings.Join(args[:len(args)-1], " "), args[len(args)-1]
	}

	st, ok := state.ParseStatus(want)
	if !ok {
		return fmt.Errorf("unknown status %q; use open, doing or done", want)
	}
	return a.withNode(refStr, func(s *session.Session, n *state.Node) error {
		return s.SetStatus(n, st)
	}, "%s  "+st.String()+" %q")
}

var cmdDue = &command{
	name:    "due",
	args:    "<ref> <date|none>",
	summary: "set or clear a task's due date",
	help: `Accepts a date, a weekday, or a relative word:

    gwiki notes due lexer 2026-09-01
    gwiki notes due lexer friday
    gwiki notes due lexer none

Relative words are resolved now, so the stored date never shifts.`,
	run: func(a *App, args []string) error {
		dateOK := func(s *session.Session, rest []string) bool {
			_, ok := state.ParseDue(strings.Join(rest, " "), s.Now())
			return len(rest) > 0 && ok
		}
		return a.withSplitNode(args, dateOK, func(s *session.Session, n *state.Node, rest []string) error {
			return s.SetDue(n, strings.Join(rest, " "))
		}, "%s  due set on %q")
	},
}

var cmdPriority = &command{
	name:    "priority",
	aliases: []string{"prio"},
	args:    "<ref> <low|normal|high|none>",
	summary: "set a task's priority",
	run: func(a *App, args []string) error {
		if len(args) < 2 {
			return errUsage
		}
		// The priority is always one word, so the reference is everything else.
		word := args[len(args)-1]
		p, ok := state.ParsePriority(word)
		if !ok {
			return fmt.Errorf("unknown priority %q; use low, normal, high or none", word)
		}
		return a.withNode(strings.Join(args[:len(args)-1], " "), func(s *session.Session, n *state.Node) error {
			return s.SetPriority(n, p)
		}, "%s  priority set on %q")
	},
}

var cmdTag = &command{
	name:    "tag",
	args:    "<ref> <tag>...",
	summary: "add tags",
	run: func(a *App, args []string) error {
		return a.withSplitNode(args, someWords, func(s *session.Session, n *state.Node, tags []string) error {
			for _, tag := range tags {
				if err := s.AddTag(n, tag); err != nil {
					return err
				}
			}
			return nil
		}, "%s  tagged %q")
	},
}

var cmdUntag = &command{
	name:    "untag",
	args:    "<ref> <tag>...",
	summary: "remove tags",
	run: func(a *App, args []string) error {
		return a.withSplitNode(args, someWords, func(s *session.Session, n *state.Node, tags []string) error {
			for _, tag := range tags {
				if err := s.RemoveTag(n, tag); err != nil {
					return err
				}
			}
			return nil
		}, "%s  untagged %q")
	},
}

var cmdAssign = &command{
	name:    "assign",
	args:    "<ref> <who>",
	summary: "put someone on a task",
	help:    `Use "me" for yourself. Other names must belong to someone who has already written to this project.`,
	run: func(a *App, args []string) error {
		return a.withSplitNode(args, someWords, func(s *session.Session, n *state.Node, who []string) error {
			return s.Assign(n, strings.Join(who, " "))
		}, "%s  assigned %q")
	},
}

var cmdUnassign = &command{
	name:    "unassign",
	args:    "<ref> <who>",
	summary: "take someone off a task",
	run: func(a *App, args []string) error {
		return a.withSplitNode(args, someWords, func(s *session.Session, n *state.Node, who []string) error {
			return s.Unassign(n, strings.Join(who, " "))
		}, "%s  unassigned %q")
	},
}

var cmdLink = &command{
	name:    "link",
	args:    "<from> <to>",
	summary: "point one entry at another",
	help:    `Records a reference, which is how a task points at the note it came out of. "gwiki notes show" lists both directions.`,
	run: func(a *App, args []string) error {
		if len(args) < 2 {
			return errUsage
		}
		s, err := a.open()
		if err != nil {
			return err
		}
		from, to, err := splitTwoRefs(s, args)
		if err != nil {
			return err
		}
		if err := s.Link(from, to); err != nil {
			return err
		}
		if err := a.commit(s); err != nil {
			return err
		}
		a.printf("%s %q -> %s %q\n", ref(from), from.Title, ref(to), to.Title)
		return nil
	},
}

// splitTwoRefs reads two entry references from unquoted words.
func splitTwoRefs(s *session.Session, args []string) (*state.Node, *state.Node, error) {
	from, rest, err := splitRef(s, args, func(rest []string) bool {
		_, err := s.State.Resolve(strings.Join(rest, " "))
		return len(rest) > 0 && err == nil
	})
	if err != nil {
		return nil, nil, err
	}
	to, err := s.State.Resolve(strings.Join(rest, " "))
	if err != nil {
		return nil, nil, err
	}
	return from, to, nil
}

var cmdUnlink = &command{
	name:    "unlink",
	args:    "<from> <to>",
	summary: "remove a reference",
	run: func(a *App, args []string) error {
		if len(args) < 2 {
			return errUsage
		}
		s, err := a.open()
		if err != nil {
			return err
		}
		var target string
		from, to, err := splitTwoRefs(s, args)
		if err == nil {
			target = to.ID
		} else {
			// The target may be deleted or missing, which the resolver
			// cannot find; the link itself still names it.
			var linkErr error
			if from, linkErr = s.State.Resolve(args[0]); linkErr != nil {
				return err
			}
			if target, linkErr = s.State.LinkTarget(from, strings.Join(args[1:], " ")); linkErr != nil {
				return err
			}
		}
		if err := s.Unlink(from, target); err != nil {
			return err
		}
		if err := a.commit(s); err != nil {
			return err
		}
		a.printf("%s  unlinked %q\n", ref(from), from.Title)
		return nil
	},
}

var cmdMove = &command{
	name:    "mv",
	aliases: []string{"move"},
	args:    "<ref> [notebook] [--top|--bottom|--before <ref>|--after <ref>]",
	summary: "move an entry to another notebook, or reorder it",
	run: func(a *App, args []string) error {
		fs := a.flags("mv")
		top := fs.Bool("top", false, "place first")
		bottom := fs.Bool("bottom", false, "place last")
		before := fs.String("before", "", "place before this entry")
		after := fs.String("after", "", "place after this entry")
		if err := parse(fs, args); err != nil {
			return err
		}
		if fs.NArg() == 0 {
			return errUsage
		}

		s, err := a.open()
		if err != nil {
			return err
		}
		n, rest, err := splitRef(s, fs.Args(), func(rest []string) bool {
			if len(rest) == 0 {
				return true
			}
			_, err := s.State.Resolve(strings.Join(rest, " "), state.KindNotebook)
			return err == nil
		})
		if err != nil {
			return err
		}

		// One placement at most, and a move needs a destination or a placement:
		// "mv x" alone used to move the entry to the bottom without being asked.
		placements := 0
		for _, set := range []bool{*top, *bottom, *before != "", *after != ""} {
			if set {
				placements++
			}
		}
		if placements > 1 || (placements == 0 && len(rest) == 0) {
			return errUsage
		}

		notebook := strings.Join(rest, " ")
		parent := n.Parent
		if notebook != "" {
			nb, err := s.State.Resolve(notebook, state.KindNotebook)
			if err != nil {
				return err
			}
			parent = nb.ID
		}

		// A sibling named for --before or --after must be where the entry is
		// going; otherwise the placement would silently become the end.
		sibling := func(ref string) (string, error) {
			sib, err := s.State.Resolve(ref)
			if err != nil {
				return "", err
			}
			if sib.Parent != parent && n.Kind != state.KindNotebook {
				return "", fmt.Errorf("%q is not in the notebook %q is moving to", sib.Title, n.Title)
			}
			return sib.ID, nil
		}

		pos := rank.End()
		switch {
		case *top:
			pos = rank.Start()
		case *before != "":
			id, err := sibling(*before)
			if err != nil {
				return err
			}
			pos = rank.Before(id)
		case *after != "":
			id, err := sibling(*after)
			if err != nil {
				return err
			}
			pos = rank.After(id)
		}

		if err := s.Move(n, notebook, pos); err != nil {
			return err
		}
		if err := a.commit(s); err != nil {
			return err
		}
		a.printf("%s  moved %q\n", ref(n), n.Title)
		return nil
	},
}

var cmdRemove = &command{
	name:    "rm",
	aliases: []string{"delete"},
	args:    "<ref>",
	summary: "delete an entry",
	help: `Deletion is recorded as an event rather than by rewriting history, so
"gwiki notes restore" can undo it and the original is always recoverable from the
log. Deleting a notebook deletes what is in it.`,
	run: func(a *App, args []string) error {
		if len(args) == 0 {
			return errUsage
		}
		return a.withNode(strings.Join(args, " "), func(s *session.Session, n *state.Node) error {
			return s.Delete(n)
		}, "%s  deleted %q")
	},
}

var cmdRestore = &command{
	name:    "restore",
	args:    "<ref>",
	summary: "undo a deletion",
	run: func(a *App, args []string) error {
		if len(args) == 0 {
			return errUsage
		}
		s, err := a.open()
		if err != nil {
			return err
		}

		n, err := s.State.ResolveDeleted(strings.Join(args, " "))
		if err != nil {
			return fmt.Errorf("no deleted entry: %w", err)
		}
		if err := s.Restore(n.ID); err != nil {
			return err
		}
		if err := a.commit(s); err != nil {
			return err
		}
		a.printf("%s  restored %q\n", ref(n), n.Title)
		return nil
	},
}

// ---------------------------------------------------------------- project

var cmdLog = &command{
	name:    "log",
	aliases: []string{"history"},
	args:    "[-n <count>] [<ref>]",
	summary: "show the change history",
	help: `Prints recorded changes, newest last. With a reference, only the changes
touching that entry.

Every change to the database is recorded by triggers in its changes table,
including edits made with other SQLite clients.`,
	run: func(a *App, args []string) error {
		fs := a.flags("log")
		count := fs.Int("n", 20, "how many changes to show")
		if err := parse(fs, args); err != nil {
			return err
		}

		s, err := a.open()
		if err != nil {
			return err
		}

		var only string
		if fs.NArg() > 0 {
			n, err := s.State.Resolve(strings.Join(fs.Args(), " "))
			if err != nil {
				return err
			}
			only = n.ID
		}

		entries, err := s.History(only, *count)
		if err != nil {
			return err
		}

		var t table
		for _, e := range entries {
			handle, when := ulid.Short(e.Node, refLen), e.At.Local().Format("2006-01-02 15:04")
			// The entry's current title leads, and a value equal to it is not
			// repeated.
			detail := e.Detail
			if n := s.State.Get(e.Node); n != nil && e.Node != s.State.Workspace {
				if detail == n.Title {
					detail = ""
				}
				detail = strings.TrimSpace(strconv.Quote(n.Title) + " " + detail)
			}
			t.addStyled(
				[]string{handle, when, e.Action, detail, e.Author},
				[]string{a.style(ansiDim, handle), a.style(ansiDim, when), a.style(ansiBold, e.Action), "", a.style(ansiDim, e.Author)},
			)
		}
		t.write(a.Stdout)
		return nil
	},
}

var cmdInfo = &command{
	name:    "info",
	aliases: []string{"stat"},
	args:    "",
	summary: "summarise the project",
	run: func(a *App, args []string) error {
		s, err := a.open()
		if err != nil {
			return err
		}
		a.warnProblems(s)

		c := s.State.Summary(a.Now())
		var t table
		t.add("project", s.Project.Config.Name)
		t.add("location", s.Project.Path)
		t.add("notebooks", fmt.Sprint(c.Notebooks))
		t.add("notes", fmt.Sprint(c.Notes))
		t.add("tasks", fmt.Sprintf("%d  (%d open, %d doing, %d done)", c.Tasks, c.Open, c.Doing, c.Done))
		if c.Overdue > 0 {
			t.add("overdue", a.style(ansiRed, fmt.Sprint(c.Overdue)))
		}
		t.add("changes", fmt.Sprint(store.Snap(s.Project)))
		t.add("contributors", fmt.Sprint(len(s.State.Contributors)))
		if len(s.Problems) > 0 {
			t.add("unapplied", fmt.Sprint(len(s.Problems)))
		}
		t.write(a.Stdout)
		return nil
	},
}

var cmdWho = &command{
	name:    "whoami",
	args:    "[--set <name>]",
	summary: "show or change the name your changes are attributed to",
	run: func(a *App, args []string) error {
		fs := a.flags("whoami")
		set := fs.String("set", "", "change your display name")
		if err := parse(fs, args); err != nil {
			return err
		}

		actor, err := store.LoadUser()
		if err != nil {
			return err
		}
		if *set != "" {
			actor.Name = *set
			// The id is deliberately preserved, so renaming keeps every event
			// already attributed to you.
			if actor, err = store.SaveUser(actor); err != nil {
				return err
			}
		}
		if !actor.Valid() {
			return errors.New("no identity configured; run 'gwiki notes init'")
		}

		path, _ := store.UserConfigPath()
		var t table
		t.add("name", actor.Name)
		t.add("id", actor.ID)
		t.add("stored in", path)
		t.write(a.Stdout)
		return nil
	},
}

// ---------------------------------------------------------------- help

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

func notesHelp(a *App) {
	a.printf("gwiki notes keeps notes and tasks in a SQLite database, .gwiki/notes.db.\n\n")
	a.printf("usage: gwiki notes <command> [arguments]\n")
	a.printf("       gwiki notes -g <command>  use the global notes, not this directory's project\n")
	a.printf("       gwiki notes               list the commands\n\n")
}

// parseWhen reads a point in the past, for time travel. It accepts a date, a
// timestamp, a relative word, or a duration ago such as "3d" or "2h".
//
// A date means the start of that day, and a date or time without a zone is in
// now's time zone, which is the one the result is printed in. A point in the
// future is refused: replaying up to it would silently show the present.
func parseWhen(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)

	t, ok := time.Time{}, false
	if d, err := parseDurationAgo(s); err == nil {
		t, ok = now.Add(-d), true
	}
	for _, layout := range []string{"2006-01-02", "2006-01-02 15:04", "2006/01/02"} {
		if ok {
			break
		}
		if parsed, err := time.ParseInLocation(layout, s, now.Location()); err == nil {
			t, ok = parsed, true
		}
	}
	if !ok {
		if parsed, err := time.Parse(time.RFC3339, s); err == nil {
			t, ok = parsed, true
		}
	}
	if !ok {
		if parsed, relative := state.ParseDue(s, now); relative && !parsed.IsZero() {
			t, ok = parsed, true
		}
	}

	switch {
	case !ok:
		return time.Time{}, fmt.Errorf("could not read %q as a time; try 2026-08-01, or 3d for three days ago", s)
	case t.After(now):
		return time.Time{}, fmt.Errorf("%q is in the future; --at looks at the past", s)
	}
	return t, nil
}

// parseDurationAgo reads the compact forms people actually type. Go's own
// parser handles h and m but not d or w, which are the useful units here.
func parseDurationAgo(s string) (time.Duration, error) {
	if s == "" {
		return 0, errors.New("empty")
	}
	unit := s[len(s)-1]
	var scale time.Duration

	switch unit {
	case 'd':
		scale = 24 * time.Hour
	case 'w':
		scale = 7 * 24 * time.Hour
	case 'h':
		scale = time.Hour
	case 'm':
		scale = time.Minute
	default:
		return 0, errors.New("not a duration")
	}

	// Atoi rather than Sscanf, which stops at the first non-digit and so read
	// "1.5d" as one day. The bound keeps the multiplication from overflowing.
	n, err := strconv.Atoi(s[:len(s)-1])
	if err != nil || n < 0 || int64(n) > math.MaxInt64/int64(scale) {
		return 0, errors.New("not a duration")
	}
	return time.Duration(n) * scale, nil
}
