// Command gnotes is a git-backed notes and tasks tool.
//
// Notes and tasks live in an append-only event log inside the repository they
// belong to. Every view is replayed from that log, so history is complete,
// concurrent edits from several machines merge without conflict, and the state
// of the project at any past moment is a matter of replaying a prefix.
package main

import (
	"os"

	"github.com/shakfu/gnotes/internal/cli"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	cli.Version = version
	os.Exit(cli.New().Run(os.Args[1:]))
}
