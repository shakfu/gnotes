// Command gwiki is a wiki of markdown pages kept with a project's code.
//
// Pages live under .gwiki/wiki and are committed with the repository; a
// derived cache, .gwiki/cache.db, indexes their links, headings, tags and
// tasks.
package main

import (
	"os"

	"github.com/shakfu/gwiki/internal/cli"
)

// version is this release. `make` overrides it with -ldflags "-X
// main.version=..." when the commit is tagged.
var version = "0.3.0"

func main() {
	cli.Version = version
	os.Exit(cli.New().Run(os.Args[1:]))
}
