package cli

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/shakfu/gnotes/internal/event"
	"github.com/shakfu/gnotes/internal/gitsync"
	"github.com/shakfu/gnotes/internal/rank"
	"github.com/shakfu/gnotes/internal/search"
	"github.com/shakfu/gnotes/internal/session"
	"github.com/shakfu/gnotes/internal/state"
	"github.com/shakfu/gnotes/internal/store"
	"github.com/shakfu/gnotes/internal/ulid"
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
// here: almost every command takes its subject first, so "gnotes note 'a
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

	return fs.Parse(append(flags, positional...))
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
	help: `Creates a .gnotes directory in the current directory and starts its
event log. The name defaults to the directory's own.

The first time you run it anywhere, it also records your name and mints the
identity that every event you write is attributed to. That identity is stored
outside the project, so it follows you across all of them.`,
	run: func(a *App, args []string) error {
		fs := a.flags("init")
		user := fs.String("user", "", "your display name")
		if err := parse(fs, args); err != nil {
			return errUsage
		}

		actor, err := store.LoadUser()
		if err != nil {
			return err
		}
		if *user != "" || !actor.Valid() {
			name := *user
			if name == "" {
				if name, err = a.prompt("Your name: "); err != nil {
					return err
				}
			}
			actor.Name = name
			if actor, err = store.SaveUser(actor); err != nil {
				return err
			}
			a.printf("identity: %s\n", actor.Name)
		}

		name := strings.Join(fs.Args(), " ")

		// An existing project just gets its workspace started, so that a fresh
		// clone of a repository someone else initialised is usable.
		p, err := store.Init(a.Dir, name, a.Now())
		if errors.Is(err, store.ErrExists) {
			s, err := a.open()
			if err != nil {
				return err
			}
			if s.State.Workspace != "" {
				return errors.New("this project is already initialised")
			}
			p = s.Project
		} else if err != nil {
			return err
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
		a.printf("created %s\n", p.Dir)
		a.printf("\nnext: gnotes note \"a first note\"  or  gnotes task \"a first task\"\n")
		return nil
	},
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

    git log --oneline -20 | gnotes note "release notes" --stdin`,
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

    gnotes task "ship the parser" -d friday -p high -a me`,
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
		return errUsage
	}
	title := strings.Join(fs.Args(), " ")
	if title == "" {
		return errUsage
	}

	text := *body
	if *stdin {
		piped, err := io.ReadAll(a.Stdin)
		if err != nil {
			return fmt.Errorf("read standard input: %w", err)
		}
		if text != "" {
			text += "\n\n"
		}
		text += strings.TrimRight(string(piped), "\n")
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

    gnotes ls -k task -s open -p high -t bug

--at replays the log as it stood at a past moment and lists that instead. It
reads a date, a timestamp, or a duration ago:

    gnotes ls --at 2026-08-01
    gnotes ls --at 3d`,
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
			return errUsage
		}

		s, err := a.open()
		if err != nil {
			return err
		}
		a.warnProblems(s)

		view := s.State
		if *at != "" {
			cutoff, err := parseWhen(*at, a.Now())
			if err != nil {
				return err
			}
			view, _ = s.At(cutoff.UnixMilli())
			if !*asJSON {
				a.printf("%s\n", a.style(ansiDim, "as of "+cutoff.Local().Format("2006-01-02 15:04")))
			}
		}

		f := state.Filter{
			Tags:           tags,
			Text:           strings.Join(fs.Args(), " "),
			Overdue:        *overdue,
			IncludeDeleted: *all,
			Now:            a.Now(),
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
		a.listNodes(view, nodes, f.Notebook == "")
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
			return errUsage
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
			a.listNodes(s.State, back, true)
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
			return errUsage
		}
		query := strings.Join(fs.Args(), " ")
		if strings.TrimSpace(query) == "" {
			return errUsage
		}

		s, err := a.open()
		if err != nil {
			return err
		}

		nodes := s.State.List(state.Filter{}, state.OrderRank)
		results := search.Build(nodes).Search(query, *limit)

		if *asJSON {
			out := make([]jsonNode, len(results))
			for i, r := range results {
				out[i] = toJSON(s.State, r.Node)
			}
			return a.writeJSON(out)
		}
		if len(results) == 0 {
			a.printf("nothing matches %q\n", query)
			return nil
		}

		for _, r := range results {
			a.listNodes(s.State, []*state.Node{r.Node}, true)
			if snippet := search.Snippet(r.Node, query, 100); snippet != "" {
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

var cmdEdit = &command{
	name:    "edit",
	args:    "<ref> [--title <text>] [-m <body>] [--stdin]",
	summary: "change a title or body",
	help: `With no flags, opens the body in $EDITOR and saves what you write.

    gnotes edit lexer --title "fix the lexer properly"
    gnotes edit lexer -m "a short body"
    cat notes.md | gnotes edit lexer --stdin`,
	run: func(a *App, args []string) error {
		fs := a.flags("edit")
		title := fs.String("title", "", "new title")
		body := fs.String("m", "", "new body")
		stdin := fs.Bool("stdin", false, "read the body from standard input")
		if err := parse(fs, args); err != nil {
			return errUsage
		}
		if fs.NArg() == 0 {
			return errUsage
		}

		return a.withNode(fs.Arg(0), func(s *session.Session, n *state.Node) error {
			if *title != "" {
				if err := s.SetTitle(n, *title); err != nil {
					return err
				}
			}
			switch {
			case *stdin:
				piped, err := io.ReadAll(a.Stdin)
				if err != nil {
					return fmt.Errorf("read standard input: %w", err)
				}
				return s.SetBody(n, strings.TrimRight(string(piped), "\n"))
			case *body != "":
				return s.SetBody(n, *body)
			case *title == "":
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

// editInEditor writes the current body to a temporary file, opens it in the
// user's editor and returns what came back.
func (a *App) editInEditor(current string) (string, error) {
	editor := firstNonEmpty(os.Getenv("VISUAL"), os.Getenv("EDITOR"))
	if editor == "" {
		return "", errors.New("no editor configured; set $EDITOR, or pass -m or --stdin")
	}

	// A .md suffix so the editor turns on the right syntax highlighting.
	f, err := os.CreateTemp("", "gnotes-*.md")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())

	if _, err := f.WriteString(current); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}

	if err := runEditor(editor, f.Name()); err != nil {
		return "", err
	}
	out, err := os.ReadFile(f.Name())
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(out), "\n"), nil
}

// runEditor launches the user's editor on a file, with the terminal handed
// straight through so a full-screen editor works normally.
//
// The command is split on spaces rather than run through a shell, so that a
// setting like "code -w" works while a path containing a semicolon cannot turn
// into a second command.
func runEditor(editor, path string) error {
	fields := strings.Fields(editor)
	if len(fields) == 0 {
		return errors.New("no editor configured")
	}

	cmd := exec.Command(fields[0], append(fields[1:], path)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("editor %q exited with an error: %w", editor, err)
	}
	return nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

var cmdStatus = &command{
	name:    "status",
	aliases: []string{"done", "doing", "reopen"},
	args:    "<ref> [status]",
	summary: "move a task to open, doing or done",
	help: `Sets a task's status. The command name doubles as the status, so
these are the same:

    gnotes done lexer
    gnotes status lexer done

"reopen" sets it back to open.`,
	runNamed: statusRun,
}

// statusRun handles the status command and its aliases. The alias carries the
// intent, so "gnotes done x" needs no further argument.
func statusRun(a *App, invoked string, args []string) error {
	want := ""
	switch invoked {
	case "done", "doing":
		want = invoked
	case "reopen":
		want = "open"
	}

	if len(args) == 0 {
		return errUsage
	}
	refStr := args[0]
	if want == "" {
		if len(args) < 2 {
			return errUsage
		}
		want = args[1]
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

    gnotes due lexer 2026-09-01
    gnotes due lexer friday
    gnotes due lexer none

Relative words are resolved now, so the stored date never shifts.`,
	run: func(a *App, args []string) error {
		if len(args) < 2 {
			return errUsage
		}
		when := strings.Join(args[1:], " ")
		return a.withNode(args[0], func(s *session.Session, n *state.Node) error {
			return s.SetDue(n, when)
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
		p, ok := state.ParsePriority(args[1])
		if !ok {
			return fmt.Errorf("unknown priority %q; use low, normal, high or none", args[1])
		}
		return a.withNode(args[0], func(s *session.Session, n *state.Node) error {
			return s.SetPriority(n, p)
		}, "%s  priority set on %q")
	},
}

var cmdTag = &command{
	name:    "tag",
	args:    "<ref> <tag>...",
	summary: "add tags",
	run: func(a *App, args []string) error {
		if len(args) < 2 {
			return errUsage
		}
		return a.withNode(args[0], func(s *session.Session, n *state.Node) error {
			for _, tag := range args[1:] {
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
		if len(args) < 2 {
			return errUsage
		}
		return a.withNode(args[0], func(s *session.Session, n *state.Node) error {
			for _, tag := range args[1:] {
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
		if len(args) < 2 {
			return errUsage
		}
		return a.withNode(args[0], func(s *session.Session, n *state.Node) error {
			return s.Assign(n, args[1])
		}, "%s  assigned %q")
	},
}

var cmdUnassign = &command{
	name:    "unassign",
	args:    "<ref> <who>",
	summary: "take someone off a task",
	run: func(a *App, args []string) error {
		if len(args) < 2 {
			return errUsage
		}
		return a.withNode(args[0], func(s *session.Session, n *state.Node) error {
			return s.Unassign(n, args[1])
		}, "%s  unassigned %q")
	},
}

var cmdLink = &command{
	name:    "link",
	args:    "<from> <to>",
	summary: "point one entry at another",
	help:    `Records a reference, which is how a task points at the note it came out of. "gnotes show" lists both directions.`,
	run: func(a *App, args []string) error {
		if len(args) < 2 {
			return errUsage
		}
		s, err := a.open()
		if err != nil {
			return err
		}
		from, err := s.State.Resolve(args[0])
		if err != nil {
			return err
		}
		to, err := s.State.Resolve(args[1])
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
		from, err := s.State.Resolve(args[0])
		if err != nil {
			return err
		}
		to, err := s.State.Resolve(args[1])
		if err != nil {
			return err
		}
		if err := s.Unlink(from, to.ID); err != nil {
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
			return errUsage
		}
		if fs.NArg() == 0 {
			return errUsage
		}

		s, err := a.open()
		if err != nil {
			return err
		}
		n, err := s.State.Resolve(fs.Arg(0))
		if err != nil {
			return err
		}

		pos := rank.End()
		switch {
		case *top:
			pos = rank.Start()
		case *bottom:
			pos = rank.End()
		case *before != "":
			sib, err := s.State.Resolve(*before)
			if err != nil {
				return err
			}
			pos = rank.Before(sib.ID)
		case *after != "":
			sib, err := s.State.Resolve(*after)
			if err != nil {
				return err
			}
			pos = rank.After(sib.ID)
		}

		notebook := strings.Join(fs.Args()[1:], " ")
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
"gnotes restore" can undo it and the original is always recoverable from the
log. Deleting a notebook deletes what is in it.`,
	run: func(a *App, args []string) error {
		if len(args) == 0 {
			return errUsage
		}
		return a.withNode(args[0], func(s *session.Session, n *state.Node) error {
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

		// The default resolver skips deleted nodes, which is right everywhere
		// except here.
		n := findDeleted(s, args[0])
		if n == nil {
			return fmt.Errorf("no deleted entry matches %q", args[0])
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

// findDeleted looks for a tombstoned node by handle or title.
func findDeleted(s *session.Session, refStr string) *state.Node {
	upper := strings.ToUpper(refStr)
	lower := strings.ToLower(refStr)

	var found *state.Node
	for _, n := range s.State.List(state.Filter{IncludeDeleted: true, Kinds: allKinds}, state.OrderCreated) {
		if !n.Deleted {
			continue
		}
		if strings.HasSuffix(n.ID, upper) || strings.Contains(strings.ToLower(n.Title), lower) {
			// The most recently created match, which is almost always the one
			// just deleted by mistake.
			found = n
		}
	}
	return found
}

var allKinds = []state.Kind{state.KindNotebook, state.KindNote, state.KindTask}

// ---------------------------------------------------------------- project

var cmdLog = &command{
	name:    "log",
	aliases: []string{"history"},
	args:    "[-n <count>] [<ref>]",
	summary: "show the event history",
	help: `Prints the raw events, newest last. With a reference, only the events
touching that entry.

This is the actual stored data; everything else in gnotes is derived from it.`,
	run: func(a *App, args []string) error {
		fs := a.flags("log")
		count := fs.Int("n", 20, "how many events to show")
		if err := parse(fs, args); err != nil {
			return errUsage
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

		log := s.Log()
		var rows []int
		for i, e := range log {
			if only != "" && e.Payload.ID != only && e.Payload.Parent != only && e.Payload.Target != only {
				continue
			}
			rows = append(rows, i)
		}
		if *count > 0 && len(rows) > *count {
			rows = rows[len(rows)-*count:]
		}

		var t table
		for _, i := range rows {
			e := log[i]
			when, _ := ulid.Timestamp(e.ID)
			t.addStyled(
				[]string{ulid.Short(e.ID, refLen), when.Local().Format("2006-01-02 15:04"), string(e.Action), describe(s, e.Payload), s.State.Contributor(e.UserID)},
				[]string{a.style(ansiDim, ulid.Short(e.ID, refLen)), a.style(ansiDim, when.Local().Format("2006-01-02 15:04")), a.style(ansiBold, string(e.Action)), "", a.style(ansiDim, s.State.Contributor(e.UserID))},
			)
		}
		t.write(a.Stdout)
		return nil
	},
}

// describe renders the part of an event payload worth seeing in a log line:
// the entry it touched, and the value it set.
func describe(s *session.Session, p event.Payload) string {
	var parts []string

	// The entry's current title, which is more use than its id for reading
	// back what happened.
	subject := ""
	if p.ID != "" {
		if n := s.State.Get(p.ID); n != nil {
			subject = n.Title
			parts = append(parts, strconv.Quote(subject))
		} else {
			parts = append(parts, ulid.Short(p.ID, refLen))
		}
	}
	for _, v := range []string{p.Name, p.Title, p.Status, p.Priority, p.Due, p.Tag} {
		// A creation event carries the same title it is already labelled
		// with; printing it twice says nothing. A later rename does differ,
		// and that is exactly the case worth seeing.
		if v != "" && v != subject {
			parts = append(parts, v)
		}
	}
	if p.Body != "" {
		parts = append(parts, fmt.Sprintf("%d bytes", len(p.Body)))
	}
	if p.Assignee != "" {
		parts = append(parts, s.State.Contributor(p.Assignee))
	}
	if p.Target != "" {
		parts = append(parts, "-> "+ulid.Short(p.Target, refLen))
	}
	if len(p.Ranks) > 0 {
		parts = append(parts, plural(len(p.Ranks), "sibling"))
	}
	return strings.Join(parts, " ")
}

var cmdSync = &command{
	name:    "sync",
	args:    "[--push]",
	summary: "commit the logs to git, and optionally exchange with origin",
	help: `Commits the event logs. Only the .gnotes paths are staged and
committed, so whatever else you have staged or edited is untouched.

--push also pulls from origin and pushes back. That is opt-in because the logs
live on your working branch: pulling would move your branch and pushing would
publish everything else on it.`,
	run: func(a *App, args []string) error {
		fs := a.flags("sync")
		push := fs.Bool("push", false, "also pull from and push to origin")
		if err := parse(fs, args); err != nil {
			return errUsage
		}

		s, err := a.open()
		if err != nil {
			return err
		}

		message := fmt.Sprintf("gnotes: %s", plural(len(s.Log()), "event"))
		res, err := gitsync.Sync(s.Project, message, *push)
		if err != nil {
			return err
		}

		switch {
		case res.Committed:
			a.printf("committed the event logs on %s\n", res.Branch)
		default:
			a.printf("nothing to commit\n")
		}
		if res.Pulled {
			a.printf("pulled from origin\n")
		}
		if res.Pushed {
			a.printf("pushed to origin\n")
		}
		if !*push && res.Committed {
			a.printf("%s\n", a.style(ansiDim, "run 'gnotes sync --push' to exchange with origin"))
		}
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
		t.add("location", s.Project.Dir)
		t.add("notebooks", fmt.Sprint(c.Notebooks))
		t.add("notes", fmt.Sprint(c.Notes))
		t.add("tasks", fmt.Sprintf("%d  (%d open, %d doing, %d done)", c.Tasks, c.Open, c.Doing, c.Done))
		if c.Overdue > 0 {
			t.add("overdue", a.style(ansiRed, fmt.Sprint(c.Overdue)))
		}
		t.add("events", fmt.Sprint(len(s.Log())))
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
	summary: "show or change the name your events are attributed to",
	run: func(a *App, args []string) error {
		fs := a.flags("whoami")
		set := fs.String("set", "", "change your display name")
		if err := parse(fs, args); err != nil {
			return errUsage
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
			return errors.New("no identity configured; run 'gnotes init'")
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

var cmdHelp = &command{
	name:    "help",
	args:    "[command]",
	summary: "show this help, or the detail for one command",
	run: func(a *App, args []string) error {
		if len(args) > 0 {
			c, ok := byName[args[0]]
			if !ok {
				return fmt.Errorf("unknown command %q", args[0])
			}
			a.printf("usage: gnotes %s %s\n\n%s\n", c.name, c.args, c.summary)
			if c.help != "" {
				a.printf("\n%s\n", c.help)
			}
			if len(c.aliases) > 0 {
				a.printf("\naliases: %s\n", strings.Join(c.aliases, ", "))
			}
			return nil
		}

		a.printf("gnotes is a git-backed notes and tasks tool.\n\n")
		a.printf("usage: gnotes <command> [arguments]\n")
		a.printf("       gnotes            open the interactive interface\n\n")

		var t table
		for _, c := range commands {
			name := c.name
			if len(c.aliases) > 0 {
				name += ", " + strings.Join(c.aliases, ", ")
			}
			t.addStyled([]string{name, c.summary}, []string{a.style(ansiBold, name), ""})
		}
		t.write(a.Stdout)

		a.printf("\nrun 'gnotes help <command>' for detail.\n")
		return nil
	},
}

// parseWhen reads a point in the past, for time travel. It accepts a date, a
// timestamp, or a duration ago such as "3d" or "2h".
func parseWhen(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)

	if d, err := parseDurationAgo(s); err == nil {
		return now.Add(-d), nil
	}
	if t, ok := state.ParseDue(s, now); ok && !t.IsZero() {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("could not read %q as a time; try 2026-08-01, or 3d for three days ago", s)
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

	var n int
	if _, err := fmt.Sscanf(s[:len(s)-1], "%d", &n); err != nil || n < 0 {
		return 0, errors.New("not a duration")
	}
	return time.Duration(n) * scale, nil
}
