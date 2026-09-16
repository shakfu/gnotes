package tui

import (
	"errors"
	"fmt"
	"strings"

	"github.com/shakfu/gwiki/internal/wiki"
)

// wikiCommand is a ':' command: typed in the page's buffer, where vim's own
// commands come first, or on the ':' line of the tree, the overview and the
// lists. The help lists the same table.
type wikiCommand struct {
	names []string // the first is the one the help shows
	args  string
	help  string
	page  bool // needs an open page
	run   func(m *WikiModel, arg string) error
}

// wikiCommands is filled in init: its commands reach the buffer, whose hooks
// look commands up here.
var wikiCommands []wikiCommand

func init() {
	wikiCommands = []wikiCommand{
		{names: []string{"new"}, args: "[title]", help: "new page beside the one open or selected", run: func(m *WikiModel, arg string) error {
			m.promptNew(arg)
			return nil
		}},
		{names: []string{"mv", "move"}, args: "[path]", help: "move the page, rewriting links", page: true, run: func(m *WikiModel, arg string) error {
			m.promptMove(arg)
			return nil
		}},
		{names: []string{"search"}, args: "text", help: "search every page", run: func(m *WikiModel, arg string) error {
			m.startSearch()
			m.input.set(arg)
			m.search()
			return nil
		}},
		{names: []string{"broken"}, help: "broken links in every page", run: func(m *WikiModel, _ string) error {
			m.showList(screenBroken)
			return nil
		}},
		{names: []string{"tasks"}, help: "open tasks", run: func(m *WikiModel, _ string) error {
			m.showList(screenTasks)
			return nil
		}},
		{names: []string{"overview"}, help: "the overview", run: func(m *WikiModel, _ string) error {
			m.showHome()
			return nil
		}},
		{names: []string{"backlinks"}, help: "to the backlinks panel", page: true, run: func(m *WikiModel, _ string) error {
			m.screen, m.base = screenRead, screenRead
			return m.toPanel()
		}},
		{names: []string{"fix"}, help: "repairs for the broken link under the cursor", page: true, run: func(m *WikiModel, _ string) error {
			return m.fixLink()
		}},
		{names: []string{"check"}, help: "the broken links in the page", page: true, run: func(m *WikiModel, _ string) error {
			broken := 0
			for _, l := range m.bufferLinks() {
				if l.Status != wiki.StatusOK {
					broken++
				}
			}
			if broken > 0 {
				return fmt.Errorf("%s in this page", plural(broken, "broken link"))
			}
			return nil
		}},
		{names: []string{"preview"}, help: "the page as markdown draws it, until esc", page: true, run: func(m *WikiModel, _ string) error {
			m.screen, m.base, m.focus = screenRead, screenRead, focusContent
			m.edit.preview = !m.edit.preview
			return nil
		}},
		{names: []string{"external"}, help: "edit the page in $EDITOR", page: true, run: func(m *WikiModel, _ string) error {
			return m.editExternal()
		}},
		{names: []string{"reload"}, help: "reload pages and git details", run: func(m *WikiModel, _ string) error {
			m.reloadAll()
			return nil
		}},
		{names: []string{"help"}, help: "every key and command", run: func(m *WikiModel, _ string) error {
			m.openHelp()
			return nil
		}},
	}
}

func findCommand(name string) *wikiCommand {
	for i := range wikiCommands {
		for _, n := range wikiCommands[i].names {
			if n == name {
				return &wikiCommands[i]
			}
		}
	}
	return nil
}

// runCommand runs a wiki command, reporting whether the name is one.
func (m *WikiModel) runCommand(name, arg string) (bool, error) {
	c := findCommand(name)
	if c == nil {
		return false, nil
	}
	if c.page && m.cur == nil {
		return true, errors.New(":" + name + " needs an open page")
	}
	return true, c.run(m, arg)
}

// editCommand handles the ':' commands vim does not know.
func (m *WikiModel) editCommand(name, arg string) (bool, error) { return m.runCommand(name, arg) }

// startCommand opens the ':' line outside the buffer.
func (m *WikiModel) startCommand() {
	m.askWiki(":", "", func(line string) error {
		m.status = ""
		return m.commandLine(line)
	})
}

// commandLine runs a ':' line typed outside the buffer: a wiki command, or
// vim's :w, :q, :wq and :x on the open page.
func (m *WikiModel) commandLine(line string) error {
	name, arg, _ := strings.Cut(strings.TrimSpace(line), " ")
	arg = strings.TrimSpace(arg)
	force := strings.HasSuffix(name, "!")
	name = strings.TrimSuffix(name, "!")
	switch name {
	case "":
		return nil
	case "q", "quit":
		if m.edit == nil {
			m.quitting = true
			return nil
		}
		return m.editQuit(force)
	case "w", "write", "wq", "x", "xit":
		if m.edit == nil {
			return errors.New(":" + name + " needs an open page")
		}
		if m.edit.ed.Dirty || force {
			if err := m.editSave(force); err != nil {
				return err
			}
			m.edit.ed.Dirty = false
		}
		if name == "w" || name == "write" {
			m.setStatus("written " + m.edit.page)
			return nil
		}
		return m.editQuit(false)
	}
	if ok, err := m.runCommand(name, arg); ok {
		return err
	}
	return errors.New("not a command: " + name)
}
