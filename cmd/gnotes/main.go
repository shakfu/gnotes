// Command gnotes is a notes and tasks tool for a single user.
//
// Notes and tasks live in a SQLite database, .gnotes/gnotes.db, committed with
// the repository they belong to. Every change is recorded in the database, so
// the project can be viewed as it stood at any past moment.
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
