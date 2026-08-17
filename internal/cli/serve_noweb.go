//go:build noweb

package cli

import "errors"

// cmdServe stands in for the browser view in a build made with -tags noweb.
//
// The command still exists so that "gnotes help" lists it and an invocation
// explains itself, rather than the user meeting an unknown-command error and
// wondering whether they typed it wrong.
var cmdServe = &command{
	name:    "serve",
	aliases: []string{"web"},
	args:    "",
	summary: "open the project in a browser (not in this build)",
	help: `This gnotes was built with -tags noweb, which leaves out the browser
view and the HTTP server it needs. Build without that tag to include it.`,
	run: func(a *App, args []string) error {
		return errors.New("this build has no browser view; rebuild without -tags noweb")
	},
}
