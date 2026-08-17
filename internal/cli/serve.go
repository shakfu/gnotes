//go:build !noweb

// The browser view roughly doubles the binary, almost all of it net/http and
// its transitive crypto. Building with -tags noweb drops this file, the web
// package and that weight, leaving the command line and the interactive
// interface untouched.

package cli

import (
	"errors"
	"fmt"
	"os/exec"
	"runtime"

	"github.com/shakfu/gnotes/internal/store"
	"github.com/shakfu/gnotes/internal/web"
)

var cmdServe = &command{
	name:    "serve",
	aliases: []string{"web"},
	args:    "[--addr <host:port>] [--no-open] [--token <secret>]",
	summary: "open the project in a browser",
	help: `Starts a local server and opens the page in your browser. It is a
third view of the same project, alongside the command line and the interactive
interface, and it updates by itself when any of them writes.

The whole page is compiled into the gnotes binary, so there is nothing to
install and it works with no network at all.

The address carries an access token. That token, not the loopback binding, is
what protects the project: any page open in your browser can make requests to
127.0.0.1, so without a secret one of them could read and rewrite your notes.
Open the printed address rather than typing the bare host and port.

No browser is opened when there is evidently no desktop to open it on: over
SSH, under a continuous integration runner, or on a Unix session with no
display. The address is printed either way, and --open forces the attempt.

    gnotes serve
    gnotes serve --no-open
    gnotes serve --addr 127.0.0.1:7777`,
	run: func(a *App, args []string) error {
		fs := a.flags("serve")
		addr := fs.String("addr", "127.0.0.1:0", "address to listen on")
		token := fs.String("token", "", "use this access token instead of a generated one")
		noOpen := fs.Bool("no-open", false, "print the address without opening a browser")
		forceOpen := fs.Bool("open", false, "open a browser even where one is not expected")
		if err := parse(fs, args); err != nil {
			return errUsage
		}

		s, err := a.open()
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("%w\n\nrun 'gnotes init' here to start one", err)
			}
			return err
		}
		if s.State.Workspace == "" {
			return errors.New("this project has no workspace yet; run 'gnotes init'")
		}
		a.warnProblems(s)

		srv, err := web.New(s, web.Options{Token: *token})
		if err != nil {
			return err
		}

		// The listener is opened before anything is printed, so that a port
		// already in use is reported as an error rather than as an address
		// that does not work.
		ln, err := srv.Serve(*addr)
		if err != nil {
			return err
		}

		url := srv.URL(ln.Addr().String())
		a.printf("%s  %s\n", a.style(ansiBold, "gnotes"), s.Project.Config.Name)
		a.printf("%s\n", url)

		// The browser is launched before Run, which is safe: the listener is
		// already accepting, so a request that arrives first waits in the
		// backlog rather than being refused.
		switch {
		case *noOpen:
		case *forceOpen || hasDesktop(a.Env):
			if err := openBrowser(url); err != nil {
				a.printf("%s\n", a.style(ansiDim, "could not open a browser: "+err.Error()))
			} else {
				a.printf("%s\n", a.style(ansiDim, "opening it in your browser"))
			}
		default:
			a.printf("%s\n", a.style(ansiDim, "no desktop session detected, so no browser was opened; --open forces it"))
		}

		a.printf("%s\n", a.style(ansiDim, "the token in that address is what grants access; press ctrl-c to stop"))
		return srv.Run(ln)
	},
}

// hasDesktop reports whether there is evidently a desktop session to open a
// browser on.
//
// Getting this wrong is worse than not trying. A "gnotes serve" run over SSH
// would otherwise launch a browser on the far machine, where nobody can see
// it, and on a headless box the opener can hang holding the terminal.
func hasDesktop(env func(string) string) bool {
	// An SSH session means the terminal is here and the machine is elsewhere.
	for _, name := range []string{"SSH_CONNECTION", "SSH_CLIENT", "SSH_TTY"} {
		if env(name) != "" {
			return false
		}
	}
	// Most runners set CI; none of them want a browser.
	if env("CI") != "" {
		return false
	}

	switch runtime.GOOS {
	case "darwin", "windows":
		// A session on these is a desktop session, and "open" is always there.
		return true
	default:
		// Unix without a display server has nothing to open onto.
		return env("DISPLAY") != "" || env("WAYLAND_DISPLAY") != ""
	}
}

// openBrowser asks the desktop to open a URL.
//
// Each platform has one canonical opener, so this is a lookup rather than a
// search. A failure is reported to the caller and never fatal: the address is
// already on screen and can be opened by hand.
func openBrowser(url string) error {
	var cmd string
	var args []string

	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
	case "windows":
		cmd, args = "rundll32", []string{"url.dll,FileProtocolHandler"}
	default:
		cmd = "xdg-open"
	}

	return exec.Command(cmd, append(args, url)...).Start()
}
