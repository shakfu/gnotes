//go:build !noweb

package cli

import (
	"errors"
	"net"

	"github.com/shakfu/gwiki/internal/webwiki"
	"github.com/shakfu/gwiki/internal/wiki"
)

var cmdWikiServe = &command{
	name:    "serve",
	aliases: []string{"web"},
	args:    "[--addr <host:port>] [--no-open] [--token <secret>]",
	summary: "open the wiki in a browser",
	help: `Starts a local server and opens the wiki in your browser: the overview, the
page tree, pages with their links and backlinks, search, broken links, tasks,
and an editor. It updates by itself when the command line, the terminal
interface or an agent writes.

The whole page is compiled into the gwiki binary, so there is nothing to
install and it works with no network at all.

The address carries an access token. That token, not the loopback binding, is
what protects the wiki: any page open in your browser can make requests to
127.0.0.1, so without a secret one of them could read and rewrite your pages.
Open the printed address rather than typing the bare host and port.

    gwiki serve
    gwiki serve --no-open
    gwiki serve --addr 127.0.0.1:7777`,
	run: func(a *App, args []string) error {
		fs := a.flags("serve")
		addr := fs.String("addr", "127.0.0.1:0", "address to listen on")
		token := fs.String("token", a.Env("GWIKI_TOKEN"), "use this access token instead of a generated one; $GWIKI_TOKEN keeps it out of the shell history")
		noOpen := fs.Bool("no-open", false, "print the address without opening a browser")
		forceOpen := fs.Bool("open", false, "open a browser even where one is not expected")
		if err := parse(fs, args); err != nil {
			return err
		}
		if *token != "" && len(*token) < 16 {
			return errors.New("a token must be at least 16 characters")
		}

		return a.withWiki(func(w *wiki.Wiki) error {
			srv, err := webwiki.New(w, webwiki.Options{Token: *token})
			if err != nil {
				return err
			}
			// The listener opens before anything is printed, so a port already
			// in use is an error rather than an address that does not work.
			ln, err := srv.Serve(*addr)
			if err != nil {
				return err
			}
			url := srv.URL(ln.Addr().String())
			a.printf("%s  %s\n", a.style(ansiBold, "gwiki"), w.Config.Name)
			a.printf("%s\n", url)
			if tcp, ok := ln.Addr().(*net.TCPAddr); ok && !tcp.IP.IsLoopback() {
				a.printf("%s\n", a.style(ansiRed, "warning: listening beyond this machine over plain HTTP; anyone who sees the address can read and change these pages"))
			}
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
		})
	},
}
