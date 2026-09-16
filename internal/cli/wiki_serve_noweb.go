//go:build noweb

package cli

import "errors"

// cmdWikiServe stands in for the browser view in a build made with -tags
// noweb, so that help still lists it and an invocation explains itself.
var cmdWikiServe = &command{
	name:    "serve",
	aliases: []string{"web"},
	args:    "",
	summary: "open the wiki in a browser (not in this build)",
	help: `This gwiki was built with -tags noweb, which leaves out the browser
view and the HTTP server it needs. Build without that tag to include it.`,
	run: func(a *App, args []string) error {
		return errors.New("this build has no browser view; rebuild without -tags noweb")
	},
}
