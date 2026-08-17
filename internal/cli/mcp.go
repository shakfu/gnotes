package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/shakfu/gnotes/internal/mcp"
	"github.com/shakfu/gnotes/internal/store"
)

var cmdMCP = &command{
	name:    "mcp",
	args:    "",
	summary: "serve the project to an agent over the Model Context Protocol",
	help: `Speaks MCP on standard input and output, so an agent can read and write
this project's notes and tasks. It is not meant to be run by hand — a client
starts it, talks to it, and stops it by closing the connection.

Register it with Claude Code from inside the project:

    claude mcp add gnotes -- gnotes mcp

The agent gets the same rules as every other view: task fields are refused on
notes, deletion is recoverable, and writes append to the same event log the
command line reads.

Everything on standard output is protocol. Diagnostics go to standard error,
where the client will surface them if it shows anything at all.`,
	run: func(a *App, args []string) error {
		fs := a.flags("mcp")
		if err := parse(fs, args); err != nil {
			return errUsage
		}

		s, err := a.open()
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("%w\n\nrun 'gnotes init' in the project you want the agent to see", err)
			}
			return err
		}
		if s.State.Workspace == "" {
			return errors.New("this project has no workspace yet; run 'gnotes init'")
		}
		for _, p := range s.Problems {
			fmt.Fprintf(a.Stderr, "gnotes mcp: %s\n", p)
		}

		// Standard output is the protocol channel and carries nothing else;
		// a.Stdout is deliberately not used anywhere in this command.
		return mcp.New(s, "gnotes", Version).Serve(os.Stdin, os.Stdout, a.Stderr)
	},
}
