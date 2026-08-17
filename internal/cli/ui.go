package cli

import (
	"errors"
	"fmt"

	"github.com/shakfu/gnotes/internal/store"
	"github.com/shakfu/gnotes/internal/tui"
)

var cmdUI = &command{
	name:    "ui",
	aliases: []string{"tui", "browse"},
	args:    "",
	summary: "open the interactive interface (also what a bare 'gnotes' does)",
	help: `A two-pane browser: notebooks on the left, their notes and tasks on
the right. Movement is vim-style, ':' opens a command line and '/' searches as
you type. Press ? inside it for the full key reference.`,
	run: func(a *App, args []string) error {
		s, err := a.open()
		if err != nil {
			// The most likely reason someone typed a bare "gnotes" is that they
			// have not set the project up yet, so say what to do about it.
			if errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("%w\n\nrun 'gnotes init' here to start one", err)
			}
			return err
		}
		if s.State.Workspace == "" {
			return errors.New("this project has no workspace yet; run 'gnotes init'")
		}
		a.warnProblems(s)
		return tui.Run(s)
	},
}
