package cli

import (
	"errors"
	"fmt"

	"github.com/shakfu/gwiki/internal/store"
	"github.com/shakfu/gwiki/internal/tui"
)

var cmdUI = &command{
	name:    "ui",
	aliases: []string{"tui", "browse"},
	args:    "",
	summary: "open the interactive interface (also what a bare 'gwiki' does)",
	help: `A two-pane browser: notebooks on the left, their notes and tasks on
the right. Movement is vim-style, ':' opens a command line and '/' searches as
you type. Press ? inside it for the full key reference.`,
	run: func(a *App, args []string) error {
		s, err := a.open()
		if err != nil {
			// The most likely reason someone typed a bare "gwiki" is that they
			// have not set the project up yet, so say what to do about it.
			if errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("%w\n\nrun 'gwiki notes init' here to start one", err)
			}
			return err
		}
		if s.State.Workspace == "" {
			return errors.New("this project has no workspace yet; run 'gwiki notes init'")
		}
		a.warnProblems(s)
		return tui.Run(s)
	},
}
