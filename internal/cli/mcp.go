package cli

import (
	"errors"
	"fmt"

	"github.com/shakfu/gwiki/internal/mcp"
	"github.com/shakfu/gwiki/internal/store"
)

var cmdMCP = &command{
	name:    "mcp",
	args:    "",
	summary: "serve the project to an agent over the Model Context Protocol",
	help: `Speaks MCP on standard input and output, so an agent can read and write
this project's notes and tasks. It is not meant to be run by hand — a client
starts it, talks to it, and stops it by closing the connection.

Register it with Claude Code from inside the project:

    claude mcp add gwiki-notes -- gwiki notes mcp

The agent gets the same rules as every other view: task fields are refused on
notes, deletion is recoverable, and writes go to the same database the command
line reads.

Everything on standard output is protocol. Diagnostics go to standard error,
where the client will surface them if it shows anything at all.`,
	run: func(a *App, args []string) error {
		fs := a.flags("mcp")
		if err := parse(fs, args); err != nil {
			return err
		}
		if fs.NArg() > 0 {
			return errUsage
		}

		s, err := a.open()
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("%w\n\nrun 'gwiki notes init' in the project you want the agent to see", err)
			}
			return err
		}
		if s.State.Workspace == "" {
			return errors.New("this project has no workspace yet; run 'gwiki notes init'")
		}
		a.warnProblems(s)

		// Standard output is the protocol channel and carries nothing else, so
		// nothing in this command prints to it before Serve.
		return mcp.New(s, "gwiki", Version).Serve(a.Stdin, a.Stdout, a.Stderr)
	},
}
