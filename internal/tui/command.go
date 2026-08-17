package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/shakfu/gnotes/internal/gitsync"
	"github.com/shakfu/gnotes/internal/rank"
	"github.com/shakfu/gnotes/internal/state"
)

// tuiCommand is one ':' command.
type tuiCommand struct {
	name    string
	aliases []string
	args    string
	summary string
	run     func(*Model, []string) error
}

// tuiCommands is the command table, in the order the help pane lists them.
var tuiCommands = []*tuiCommand{
	{
		name: "new", aliases: []string{"note"}, args: "<title>",
		summary: "add a note to the selected notebook",
		run: func(m *Model, args []string) error {
			return m.createFromCommand(false, strings.Join(args, " "))
		},
	},
	{
		name: "task", args: "<title>",
		summary: "add a task to the selected notebook",
		run: func(m *Model, args []string) error {
			return m.createFromCommand(true, strings.Join(args, " "))
		},
	},
	{
		name: "notebook", aliases: []string{"nb"}, args: "<name>",
		summary: "create a notebook",
		run: func(m *Model, args []string) error {
			nb, err := m.sess.NewNotebook(strings.Join(args, " "))
			if err != nil {
				return err
			}
			m.commit("created notebook " + nb.Title)
			return nil
		},
	},
	{
		name: "tag", args: "<tag>...",
		summary: "tag the selection",
		run: func(m *Model, args []string) error {
			return m.eachTag(args, m.sess.AddTag, "tagged")
		},
	},
	{
		name: "untag", args: "<tag>...",
		summary: "remove tags from the selection",
		run: func(m *Model, args []string) error {
			return m.eachTag(args, m.sess.RemoveTag, "untagged")
		},
	},
	{
		name: "due", args: "<date|none>",
		summary: "set the selected task's due date",
		run: func(m *Model, args []string) error {
			n, err := m.requireEntry()
			if err != nil {
				return err
			}
			if err := m.sess.SetDue(n, strings.Join(args, " ")); err != nil {
				return err
			}
			m.commit("due set")
			return nil
		},
	},
	{
		name: "priority", aliases: []string{"prio"}, args: "<low|normal|high|none>",
		summary: "set the selected task's priority",
		run: func(m *Model, args []string) error {
			n, err := m.requireEntry()
			if err != nil {
				return err
			}
			if len(args) == 0 {
				return fmt.Errorf("which priority? low, normal, high or none")
			}
			p, ok := state.ParsePriority(args[0])
			if !ok {
				return fmt.Errorf("unknown priority %q", args[0])
			}
			if err := m.sess.SetPriority(n, p); err != nil {
				return err
			}
			m.commit("priority set")
			return nil
		},
	},
	{
		name: "assign", args: "<who>",
		summary: "assign the selected task",
		run: func(m *Model, args []string) error {
			n, err := m.requireEntry()
			if err != nil {
				return err
			}
			if len(args) == 0 {
				return fmt.Errorf("assign to whom?")
			}
			if err := m.sess.Assign(n, args[0]); err != nil {
				return err
			}
			m.commit("assigned")
			return nil
		},
	},
	{
		name: "move", aliases: []string{"mv"}, args: "<notebook>",
		summary: "move the selection to another notebook",
		run: func(m *Model, args []string) error {
			n, err := m.requireEntry()
			if err != nil {
				return err
			}
			if len(args) == 0 {
				return fmt.Errorf("move to which notebook?")
			}
			// Appended rather than placed: ordering within a notebook is done
			// with J and K, which act on what is on screen.
			if err := m.sess.Move(n, strings.Join(args, " "), rank.End()); err != nil {
				return err
			}
			m.commit("moved")
			return nil
		},
	},
	{
		name: "delete", aliases: []string{"rm"}, args: "",
		summary: "delete the selection",
		run: func(m *Model, args []string) error {
			m.promptDelete()
			return nil
		},
	},
	{
		name: "filter", args: "<field> <value> | clear",
		summary: "narrow the list by kind, tag, status, priority, assignee or overdue",
		run:     (*Model).runFilter,
	},
	{
		name: "sort", args: "<rank|created|updated|title|due|priority>",
		summary: "change the list order",
		run: func(m *Model, args []string) error {
			o, ok := state.ParseOrder(strings.Join(args, " "))
			if !ok {
				return fmt.Errorf("unknown sort %q", strings.Join(args, " "))
			}
			m.order = o
			m.refresh()
			m.setStatus("sorted")
			return nil
		},
	},
	{
		name: "sync", args: "[push]",
		summary: "commit the logs to git, or exchange with origin",
		run: func(m *Model, args []string) error {
			withRemote := len(args) > 0 && (args[0] == "push" || args[0] == "--push")

			message := fmt.Sprintf("gnotes: %d events", len(m.sess.Log()))
			res, err := gitsync.Sync(m.sess.Project, message, withRemote)
			if err != nil {
				return err
			}

			// Another machine's work may have arrived in the pull.
			m.reload()

			switch {
			case res.Pushed:
				m.setStatus("synced with origin on %s", res.Branch)
			case res.Committed:
				m.setStatus("committed on %s", res.Branch)
			default:
				m.setStatus("nothing to commit")
			}
			return nil
		},
	},
	{
		name: "reload", aliases: []string{"r"}, args: "",
		summary: "re-read the log from disk",
		run: func(m *Model, args []string) error {
			m.reload()
			m.setStatus("reloaded")
			return nil
		},
	},
	{
		name: "help", aliases: []string{"h"}, args: "",
		summary: "show the key reference",
		run: func(m *Model, args []string) error {
			m.mode = modeHelp
			return nil
		},
	},
	{
		name: "quit", aliases: []string{"q"}, args: "",
		summary: "leave gnotes",
		run:     func(m *Model, args []string) error { return errQuit },
	},
}

// errQuit is a sentinel, not a failure: it lets ':quit' return through the
// same path as every other command.
var errQuit = fmt.Errorf("quit")

// tuiByName indexes commands and aliases.
var tuiByName = map[string]*tuiCommand{}

func init() {
	for _, c := range tuiCommands {
		tuiByName[c.name] = c
		for _, a := range c.aliases {
			tuiByName[a] = c
		}
	}
}

// runCommand parses and executes a ':' line.
func (m *Model) runCommand(line string) (tea.Model, tea.Cmd) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return m, nil
	}

	c, ok := tuiByName[fields[0]]
	if !ok {
		m.setStatus("unknown command %q, press ? for help", fields[0])
		m.statusErr = true
		return m, nil
	}

	if err := c.run(m, fields[1:]); err != nil {
		if err == errQuit {
			m.quitting = true
			return m, tea.Quit
		}
		m.setError(err)
	}
	return m, nil
}

// requireEntry returns the selected note or task, or explains that there is
// none.
func (m *Model) requireEntry() (*state.Node, error) {
	n := m.currentEntry()
	if n == nil {
		return nil, fmt.Errorf("select a note or task first")
	}
	return n, nil
}

// createFromCommand adds a note or task from the command line.
func (m *Model) createFromCommand(isTask bool, title string) error {
	if strings.TrimSpace(title) == "" {
		return fmt.Errorf("give it a title")
	}

	nb := m.currentNotebook()
	if nb == nil {
		created, err := m.sess.DefaultNotebook()
		if err != nil {
			return err
		}
		nb = created
	}

	var err error
	if isTask {
		_, err = m.sess.NewTask(nb.ID, title, "")
	} else {
		_, err = m.sess.NewNote(nb.ID, title, "")
	}
	if err != nil {
		return err
	}

	m.commit("created " + title)
	m.focus = paneEntries
	m.selectTitle(title)
	return nil
}

// eachTag applies a tag operation to every argument.
func (m *Model) eachTag(args []string, apply func(*state.Node, string) error, verb string) error {
	n := m.selected()
	if n == nil {
		return fmt.Errorf("select something first")
	}
	if len(args) == 0 {
		return fmt.Errorf("which tag?")
	}
	for _, tag := range args {
		if err := apply(n, tag); err != nil {
			return err
		}
	}
	m.commit(verb)
	return nil
}

// runFilter implements ':filter', which narrows the list persistently, unlike
// '/' which searches.
func (m *Model) runFilter(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("filter what? try kind, tag, status, priority, assignee, overdue or clear")
	}

	field := strings.ToLower(args[0])
	value := strings.Join(args[1:], " ")

	switch field {
	case "clear", "none", "off":
		m.filter = state.Filter{}
	case "kind":
		k, ok := state.ParseKind(value)
		if !ok {
			return fmt.Errorf("unknown kind %q; use note or task", value)
		}
		m.filter.Kinds = []state.Kind{k}
	case "tag":
		if value == "" {
			m.filter.Tags = nil
			break
		}
		m.filter.Tags = append(m.filter.Tags, state.NormalizeTag(value))
	case "status":
		s, ok := state.ParseStatus(value)
		if !ok {
			return fmt.Errorf("unknown status %q; use open, doing or done", value)
		}
		m.filter.Status = &s
	case "priority", "prio":
		p, ok := state.ParsePriority(value)
		if !ok {
			return fmt.Errorf("unknown priority %q", value)
		}
		m.filter.Priority = &p
	case "assignee", "who":
		if strings.EqualFold(value, "me") {
			m.filter.Assignee = m.sess.Actor.ID
			break
		}
		id, ok := m.sess.State.FindContributor(value)
		if !ok {
			return fmt.Errorf("nobody named %q in this project", value)
		}
		m.filter.Assignee = id
	case "overdue":
		m.filter.Overdue = !m.filter.Overdue
	case "deleted":
		m.filter.IncludeDeleted = !m.filter.IncludeDeleted
	default:
		return fmt.Errorf("cannot filter on %q", field)
	}

	m.entry, m.scroll = 0, 0
	m.refresh()
	m.setStatus("%d shown", len(m.entries))
	return nil
}

// complete fills in the rest of a partially typed command, or the common
// prefix when several match.
//
// Completing to the shared prefix rather than cycling through candidates means
// tab never puts something on the line the user did not ask for.
func (m *Model) complete() {
	line := m.input.String()
	fields := strings.Fields(line)

	// Completing the command word itself, which is the case whenever nothing
	// has been typed after it.
	if len(fields) <= 1 && !strings.HasSuffix(line, " ") {
		prefix := ""
		if len(fields) == 1 {
			prefix = fields[0]
		}
		if done := commonPrefix(matchingCommands(prefix)); done != "" {
			m.input.set(done)
			if len(matchingCommands(prefix)) == 1 {
				m.input.insert(' ')
			}
		}
		return
	}

	// Completing a tag argument, the only argument with a known vocabulary.
	if fields[0] == "tag" || fields[0] == "untag" || (fields[0] == "filter" && len(fields) > 1 && fields[1] == "tag") {
		partial := ""
		if !strings.HasSuffix(line, " ") {
			partial = fields[len(fields)-1]
		}

		var candidates []string
		for _, tc := range m.sess.State.Tags() {
			if strings.HasPrefix(tc.Tag, partial) {
				candidates = append(candidates, tc.Tag)
			}
		}
		sort.Strings(candidates)

		if done := commonPrefix(candidates); done != "" && done != partial {
			keep := strings.TrimSuffix(line, partial)
			m.input.set(keep + done)
		}
	}
}

// matchingCommands lists the command names starting with a prefix.
func matchingCommands(prefix string) []string {
	var out []string
	for _, c := range tuiCommands {
		if strings.HasPrefix(c.name, prefix) {
			out = append(out, c.name)
		}
	}
	sort.Strings(out)
	return out
}

// commonPrefix returns the longest prefix shared by every candidate.
func commonPrefix(candidates []string) string {
	if len(candidates) == 0 {
		return ""
	}
	prefix := candidates[0]
	for _, c := range candidates[1:] {
		for !strings.HasPrefix(c, prefix) {
			prefix = prefix[:len(prefix)-1]
			if prefix == "" {
				return ""
			}
		}
	}
	return prefix
}
