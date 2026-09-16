// Command gwiki is a wiki of markdown pages kept with a project's code.
//
// Pages live under .gwiki/wiki and are committed with the repository; a
// derived cache, .gwiki/cache.db, indexes their links, headings, tags and
// tasks. The older notes and tasks database is under "gwiki notes".
package main

import (
	"os"

	"github.com/shakfu/gwiki/internal/cli"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	cli.Version = version
	os.Exit(cli.New().Run(os.Args[1:]))
}
