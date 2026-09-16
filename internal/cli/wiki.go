package cli

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"strconv"
	"strings"

	"github.com/shakfu/gwiki/internal/display"
	"github.com/shakfu/gwiki/internal/lsp"
	"github.com/shakfu/gwiki/internal/mcp"
	"github.com/shakfu/gwiki/internal/tui"
	"github.com/shakfu/gwiki/internal/wiki"
)

// The wiki commands, at the top level. The notes database has its own under
// "gwiki notes"; see app.go.

func wikiHelp(a *App) {
	a.printf("gwiki keeps a wiki of markdown pages in your project, under .gwiki/wiki.\n\n")
	a.printf("usage: gwiki <command> [arguments]\n")
	a.printf("       gwiki               open the wiki interface\n\n")
	a.printf("A page is named by its path, its title, its file name, or a fragment. A task\n")
	a.printf("is a task page, \"page:line\" for a checklist item, or a fragment of its text.\n")
	a.printf("Every write checks that the page has not changed since it was read.\n\n")
}

var (
	cmdWikiInit = &command{name: "init", summary: "create .gwiki/wiki here", run: wikiInit,
		help: `Creates .gwiki/wiki for pages, .gwiki/config.json, and a .gwiki/.gitignore
that keeps the cache and drafts out of commits. Pages are committed with the
project; .gwiki/cache.db is derived from them and rebuilt whenever it is
missing or stale.`}
	cmdWikiList      = &command{name: "ls", args: "[dir] [-t tag] [--json]", summary: "list pages", run: wikiList}
	cmdWikiShow      = &command{name: "show", args: "<page> [--json]", summary: "a page with its links and backlinks", run: wikiShow}
	cmdWikiSearch    = &command{name: "search", args: "<query> [-n N] [--json]", summary: "ranked full-text search; the last word matches as a prefix", run: wikiSearch}
	cmdWikiLinks     = &command{name: "links", args: "<page> [--json]", summary: "a page's outgoing links and their status", run: wikiLinks}
	cmdWikiBacklinks = &command{name: "backlinks", args: "<page> [--json]", summary: "the links that reach a page", run: wikiBacklinks}
	cmdWikiCheck     = &command{name: "check", args: "[--fix[=first]] [--json]", summary: "broken links; exit status 1 while any remain", run: wikiCheck,
		help: `Re-examines every link, file and line links included, and lists those whose
target is missing, ambiguous or out of range. --fix offers repairs for each
one to choose from; --fix=first applies a repair when it is the only one.`}
	cmdWikiOrphans = &command{name: "orphans", args: "[--json]", summary: "pages no other page links to", run: wikiOrphans}
	cmdWikiTasks   = &command{name: "tasks", args: "[-s status] [--json]", summary: "checklist items and task pages", run: wikiTasks}
	cmdWikiNew     = &command{name: "new", args: "<title> [--in dir] [--task] [-t tag]... [-m body | --stdin]", summary: "create a page, or a task page", run: wikiNew}
	cmdWikiEdit    = &command{name: "edit", args: "<page> [-m body | --stdin]", summary: "replace a page's body, or open the page in $EDITOR", run: wikiEdit}
	cmdWikiMove    = &command{name: "mv", args: "<page> <path> [--dry-run] [--json]", summary: "rename a page, rewriting links to and from it", run: wikiMove,
		help: `Moves a page and rewrites every link to it, and its own relative links, in
each link's own form. A path ending in / keeps the file name. --dry-run lists
the changes without making them. The move is refused, and nothing written, if
a page it edits changed since it was read.`}
	cmdWikiRemove  = &command{name: "rm", args: "<page> [--force]", summary: "delete a page; refused while pages link to it", run: wikiRemove}
	cmdWikiTag     = &command{name: "tag", args: "<page> <tag>...", summary: "add tags to a page's front matter", run: func(a *App, args []string) error { return wikiTag(a, args, true) }}
	cmdWikiUntag   = &command{name: "untag", args: "<page> <tag>...", summary: "remove tags from a page's front matter", run: func(a *App, args []string) error { return wikiTag(a, args, false) }}
	cmdWikiDone    = &command{name: "done", args: "<task>", summary: "tick a checklist item, or mark a task page done", run: func(a *App, args []string) error { return wikiStatus(a, args, "done") }}
	cmdWikiDoing   = &command{name: "doing", args: "<task>", summary: "mark a task page in progress", run: func(a *App, args []string) error { return wikiStatus(a, args, "doing") }}
	cmdWikiReopen  = &command{name: "reopen", args: "<task>", summary: "clear a checklist item, or reopen a task page", run: func(a *App, args []string) error { return wikiStatus(a, args, "open") }}
	cmdWikiPromote = &command{name: "promote", args: "<item> [--in dir]", summary: "turn a checklist item into a task page linked from where it was", run: wikiPromote}
	cmdWikiUI      = &command{name: "ui", summary: "the wiki interface (also what a bare 'gwiki' does)", run: wikiUI}
	cmdWikiMCP     = &command{name: "mcp", summary: "serve the wiki to an agent over the Model Context Protocol", run: wikiMCP,
		help: `Speaks MCP on standard input and output. Register it with Claude Code from
inside the project:

    claude mcp add gwiki -- gwiki mcp`}
	cmdWikiLSP = &command{name: "lsp", summary: "serve the wiki to an editor over the Language Server Protocol", run: wikiLSP,
		help: `Speaks LSP on standard input and output, started by an editor. See the
README for Neovim, Helix and Vim configuration.`}
	cmdWikiCache = &command{name: "cache", args: "--rebuild", summary: "delete the cache and index every page again", run: wikiCache}
)

// wikiLSP serves the wiki to an editor on standard input and output. The
// editor starts it; see the README for configuration.
func wikiLSP(a *App, args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("%w: lsp takes no arguments", errUsage)
	}
	s := lsp.New(func(root string) (*wiki.Wiki, error) {
		if root == "" {
			root = a.Dir
		}
		p, err := wiki.Discover(root)
		if err != nil {
			return nil, err
		}
		return wiki.Open(p)
	}, "gwiki", Version)
	defer s.Close()
	return s.Serve(a.Stdin, a.Stdout, a.Stderr)
}

func wikiUI(a *App, args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("%w: ui takes no arguments", errUsage)
	}
	return a.withWiki(tui.RunWiki)
}

// wikiMCP serves the wiki on standard input and output. Register it with
// Claude Code from inside the project:
//
//	claude mcp add gwiki -- gwiki mcp
func wikiMCP(a *App, args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("%w: mcp takes no arguments", errUsage)
	}
	// Standard output carries only protocol frames from here on.
	return a.withWiki(func(w *wiki.Wiki) error {
		return mcp.NewWiki(w, "gwiki", Version).Serve(a.Stdin, a.Stdout, a.Stderr)
	})
}

// openWiki finds and opens the wiki, bringing its cache up to date.
func (a *App) openWiki() (*wiki.Wiki, error) {
	p, err := wiki.Discover(a.Dir)
	if err != nil {
		return nil, err
	}
	return wiki.Open(p)
}

// withWiki opens the wiki, runs fn and closes the wiki.
func (a *App) withWiki(fn func(*wiki.Wiki) error) error {
	w, err := a.openWiki()
	if err != nil {
		return err
	}
	defer w.Close()
	return fn(w)
}

func wikiInit(a *App, args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("%w: init takes no arguments", errUsage)
	}
	p, err := wiki.Init(a.Dir)
	if err != nil {
		return err
	}
	w, err := wiki.Open(p)
	if err != nil {
		return err
	}
	defer w.Close()
	pages, err := w.Pages("", "")
	if err != nil {
		return err
	}
	a.printf("wiki at %s (%s)\n", p.PagesPath(), plural(len(pages), "page"))
	return nil
}

func wikiList(a *App, args []string) error {
	fs := a.flags("ls")
	tag := fs.String("t", "", "only pages with this tag")
	asJSON := fs.Bool("json", false, "machine-readable output")
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return fmt.Errorf("%w: ls takes one directory", errUsage)
	}
	return a.withWiki(func(w *wiki.Wiki) error {
		pages, err := w.Pages(fs.Arg(0), *tag)
		if err != nil {
			return err
		}
		if *asJSON {
			return a.writeJSON(pages)
		}
		a.pageTable(pages)
		return nil
	})
}

func (a *App) pageTable(pages []wiki.PageInfo) {
	var t table
	for _, p := range pages {
		status := ""
		if p.Type == "task" {
			status = "[" + orDefault(p.Status, "open") + "]"
		}
		tags := ""
		if len(p.Tags) > 0 {
			tags = "#" + strings.Join(p.Tags, " #")
		}
		t.addStyled([]string{p.Path, status, p.Title, tags},
			[]string{a.style(ansiDim, p.Path), a.style(ansiYellow, status), a.style(ansiBold, p.Title), a.style(ansiCyan, tags)})
	}
	t.write(a.Stdout)
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// pageArg parses flags and a page reference, the shape of show, links and
// backlinks.
func (a *App) pageArg(name string, args []string) (string, bool, error) {
	fs := a.flags(name)
	asJSON := fs.Bool("json", false, "machine-readable output")
	if err := parse(fs, args); err != nil {
		return "", false, err
	}
	ref := strings.Join(fs.Args(), " ")
	if ref == "" {
		return "", false, fmt.Errorf("%w: name a page", errUsage)
	}
	return ref, *asJSON, nil
}

func wikiShow(a *App, args []string) error {
	ref, asJSON, err := a.pageArg("wiki show", args)
	if err != nil {
		return err
	}
	w, err := a.openWiki()
	if err != nil {
		return err
	}
	defer w.Close()
	info, err := w.Find(ref)
	if err != nil {
		return err
	}
	page, err := w.Page(info.Path)
	if err != nil {
		return err
	}
	if asJSON {
		return a.writeJSON(page)
	}

	a.printf("%s  %s\n", a.style(ansiBold, page.Title), a.style(ansiDim, page.File()))
	if len(page.Tags) > 0 {
		a.printf("%s\n", a.style(ansiCyan, "#"+strings.Join(page.Tags, " #")))
	}
	a.printf("\n%s\n", strings.TrimRight(display.Block(page.Body), "\n"))

	if len(page.Links) > 0 {
		a.printf("\n%s\n", a.style(ansiBold, "links"))
		a.linkTable(page.Links, false)
	}
	if len(page.Backlinks) > 0 {
		a.printf("\n%s\n", a.style(ansiBold, "backlinks"))
		a.linkTable(page.Backlinks, true)
	}
	return nil
}

// linkTable prints links: where they are when from is set, what they point at,
// and their status when it is not ok.
func (a *App) linkTable(links []wiki.Link, from bool) {
	var t table
	for _, l := range links {
		where := fmt.Sprintf("%d:%d", l.Line, l.Col)
		if from {
			where = fmt.Sprintf("%s:%d", l.Page, l.Line)
		}
		status := ""
		if l.Status != wiki.StatusOK {
			status = l.Status
		}
		target := l.Resolved
		if l.Kind == wiki.KindExternal {
			target = ""
		}
		t.addStyled([]string{where, l.Written(), status, target},
			[]string{a.style(ansiDim, where), l.Written(), a.style(ansiRed, status), a.style(ansiDim, target)})
	}
	t.write(a.Stdout)
}

func wikiSearch(a *App, args []string) error {
	fs := a.flags("search")
	limit := fs.Int("n", 20, "maximum results")
	asJSON := fs.Bool("json", false, "machine-readable output")
	if err := parse(fs, args); err != nil {
		return err
	}
	query := strings.Join(fs.Args(), " ")
	if strings.TrimSpace(query) == "" {
		return fmt.Errorf("%w: give something to search for", errUsage)
	}
	return a.withWiki(func(w *wiki.Wiki) error {
		hits, err := w.Search(query, *limit)
		if err != nil {
			return err
		}
		if *asJSON {
			for i := range hits {
				hits[i].Snippet = strings.NewReplacer("\x02", "", "\x03", "").Replace(hits[i].Snippet)
			}
			return a.writeJSON(hits)
		}
		if len(hits) == 0 {
			a.printf("nothing matches %q\n", query)
			return nil
		}
		for _, h := range hits {
			a.printf("%s  %s\n", a.style(ansiDim, h.Path), a.style(ansiBold, h.Title))
			if s := a.snippet(h.Snippet); s != "" {
				a.printf("    %s\n", s)
			}
		}
		return nil
	})
}

// snippet flattens a search snippet to one line and styles its match markers.
func (a *App) snippet(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	var b strings.Builder
	for i, part := range strings.Split(s, "\x02") {
		if i == 0 {
			b.WriteString(display.Line(part))
			continue
		}
		match, rest, _ := strings.Cut(part, "\x03")
		b.WriteString(a.style(ansiYellow, match))
		b.WriteString(display.Line(rest))
	}
	return b.String()
}

func wikiLinks(a *App, args []string) error {
	return a.pageLinks("wiki links", args, func(w *wiki.Wiki, p string) ([]wiki.Link, error) { return w.Links(p) }, false)
}

func wikiBacklinks(a *App, args []string) error {
	return a.pageLinks("wiki backlinks", args, func(w *wiki.Wiki, p string) ([]wiki.Link, error) { return w.Backlinks(p) }, true)
}

func (a *App) pageLinks(name string, args []string, get func(*wiki.Wiki, string) ([]wiki.Link, error), from bool) error {
	ref, asJSON, err := a.pageArg(name, args)
	if err != nil {
		return err
	}
	w, err := a.openWiki()
	if err != nil {
		return err
	}
	defer w.Close()
	info, err := w.Find(ref)
	if err != nil {
		return err
	}
	links, err := get(w, info.Path)
	if err != nil {
		return err
	}
	if asJSON {
		return a.writeJSON(links)
	}
	a.linkTable(links, from)
	return nil
}

// fixFlag is --fix, --fix=first or absent.
type fixFlag string

func (f *fixFlag) String() string   { return string(*f) }
func (f *fixFlag) IsBoolFlag() bool { return true }
func (f *fixFlag) Set(v string) error {
	switch v {
	case "true":
		*f = "ask"
	case "first":
		*f = "first"
	case "false":
		*f = ""
	default:
		return fmt.Errorf("--fix takes no value or first, not %q", v)
	}
	return nil
}

func wikiCheck(a *App, args []string) error {
	fs := a.flags("check")
	asJSON := fs.Bool("json", false, "machine-readable output")
	var fix fixFlag
	fs.Var(&fix, "fix", "offer repairs for each broken link; =first applies an only offer")
	if err := parse(fs, args); err != nil {
		return err
	}
	if fix != "" && *asJSON {
		return fmt.Errorf("%w: --fix and --json do not combine", errUsage)
	}
	return a.withWiki(func(w *wiki.Wiki) error {
		if fix != "" {
			if err := a.fixLinks(w, fix == "first"); err != nil {
				return err
			}
		}
		broken, err := w.Check()
		if err != nil {
			return err
		}
		if *asJSON {
			if err := a.writeJSON(broken); err != nil {
				return err
			}
		} else {
			var t table
			for _, l := range broken {
				where := fmt.Sprintf("%s:%d:%d", l.Page, l.Line, l.Col)
				t.addStyled([]string{where, l.Status, l.Written()},
					[]string{a.style(ansiDim, where), a.style(ansiRed, l.Status), l.Written()})
			}
			t.write(a.Stdout)
		}
		if len(broken) > 0 {
			// An error, so the exit status is 1 for scripts and CI.
			return errors.New(plural(len(broken), "broken link"))
		}
		if !*asJSON {
			a.printf("no broken links\n")
		}
		return nil
	})
}

// fixLinks walks the broken links, one at a time, offering repairs. It reads
// the list again after each fix, since a fix moves the offsets of later links
// in the same page. With first set it applies an only offer and asks nothing.
func (a *App) fixLinks(w *wiki.Wiki, first bool) error {
	in := bufio.NewReader(a.Stdin)
	passed := map[string]bool{}
	fixed := 0
	for {
		broken, err := w.Check()
		if err != nil {
			return err
		}
		var l *wiki.Link
		for i := range broken {
			if !passed[linkKey(broken[i])] {
				l = &broken[i]
				break
			}
		}
		if l == nil {
			break
		}
		passed[linkKey(*l)] = true
		offers, err := w.Offers(*l)
		if err != nil {
			return err
		}
		if len(offers) == 0 || (first && len(offers) != 1) {
			continue
		}

		choice := 0
		if !first {
			a.printf("%s  %s  %s\n", a.style(ansiDim, fmt.Sprintf("%s:%d", l.Page, l.Line)), a.style(ansiRed, l.Status), l.Written())
			for i, o := range offers {
				a.printf("  %d  %s  %s\n", i+1, display.Line(o.New), a.style(ansiDim, o.Label))
			}
			a.printf("  s  skip    q  stop\nchoice: ")
			line, err := in.ReadString('\n')
			answer := strings.TrimSpace(line)
			if answer == "q" || (err != nil && answer == "") {
				a.printf("\n")
				break
			}
			n, convErr := strconv.Atoi(answer)
			if convErr != nil || n < 1 || n > len(offers) {
				continue
			}
			choice = n - 1
		}
		if err := w.Fix(*l, offers[choice]); err != nil {
			return err
		}
		fixed++
		a.printf("%s  %s -> %s\n", a.style(ansiGreen, "fixed"), l.Written(), display.Line(offers[choice].New))
	}
	if fixed > 0 {
		a.printf("%s\n", plural(fixed, "link")+" fixed")
	}
	return nil
}

// linkKey identifies a broken link across re-reads, which can shift columns.
func linkKey(l wiki.Link) string {
	return fmt.Sprintf("%s\x00%d\x00%s\x00%s", l.Page, l.Line, l.Written(), l.Status)
}

func wikiOrphans(a *App, args []string) error {
	fs := a.flags("orphans")
	asJSON := fs.Bool("json", false, "machine-readable output")
	if err := parse(fs, args); err != nil {
		return err
	}
	return a.withWiki(func(w *wiki.Wiki) error {
		pages, err := w.Orphans()
		if err != nil {
			return err
		}
		if *asJSON {
			return a.writeJSON(pages)
		}
		a.pageTable(pages)
		return nil
	})
}

func wikiTasks(a *App, args []string) error {
	fs := a.flags("tasks")
	status := fs.String("s", "", "open, doing or done")
	asJSON := fs.Bool("json", false, "machine-readable output")
	if err := parse(fs, args); err != nil {
		return err
	}
	switch *status {
	case "", "open", "doing", "done":
	default:
		return fmt.Errorf("%w: status is open, doing or done, not %q", errUsage, *status)
	}
	return a.withWiki(func(w *wiki.Wiki) error {
		tasks, err := w.Tasks(wiki.TaskFilter{Status: *status})
		if err != nil {
			return err
		}
		if *asJSON {
			return a.writeJSON(tasks)
		}
		var t table
		for _, tk := range tasks {
			where := tk.Page
			if tk.Line > 0 {
				where = fmt.Sprintf("%s:%d", tk.Page, tk.Line)
			}
			box := "[ ]"
			switch tk.Status {
			case "done":
				box = "[x]"
			case "doing":
				box = "[~]"
			}
			t.addStyled([]string{where, box, tk.Due, tk.Priority, tk.Text},
				[]string{a.style(ansiDim, where), box, a.style(ansiYellow, tk.Due), a.style(ansiRed, tk.Priority), tk.Text})
		}
		t.write(a.Stdout)
		return nil
	})
}

func wikiCache(a *App, args []string) error {
	fs := a.flags("cache")
	rebuild := fs.Bool("rebuild", false, "delete the cache and index every page again")
	if err := parse(fs, args); err != nil {
		return err
	}
	if !*rebuild || fs.NArg() > 0 {
		return fmt.Errorf("%w: cache --rebuild", errUsage)
	}
	return a.withWiki(func(w *wiki.Wiki) error {
		if err := w.Rebuild(); err != nil {
			return err
		}
		pages, err := w.Pages("", "")
		if err != nil {
			return err
		}
		a.printf("rebuilt %s from %s\n", w.Cache(), plural(len(pages), "page"))
		return nil
	})
}

func wikiNew(a *App, args []string) error {
	fs := a.flags("new")
	dir := fs.String("in", "", "directory under .gwiki/wiki")
	task := fs.Bool("task", false, "a task page")
	body := fs.String("m", "", "body text")
	stdin := fs.Bool("stdin", false, "read the body from standard input")
	var tags multiFlag
	fs.Var(&tags, "t", "tag (repeatable)")
	if err := parse(fs, args); err != nil {
		return err
	}
	title := strings.Join(fs.Args(), " ")
	if strings.TrimSpace(title) == "" {
		return fmt.Errorf("%w: give the page a title", errUsage)
	}
	text := *body
	if *stdin {
		piped, err := a.readStdin()
		if err != nil {
			return err
		}
		text = piped
	}
	return a.withWiki(func(w *wiki.Wiki) error {
		info, err := w.Create(wiki.NewPage{Title: title, Dir: *dir, Task: *task, Tags: tags, Body: text})
		if err != nil {
			return err
		}
		a.printf("%s  %s\n", a.style(ansiDim, info.File()), a.style(ansiBold, info.Title))
		return nil
	})
}

func wikiEdit(a *App, args []string) error {
	fs := a.flags("edit")
	body := fs.String("m", "", "new body")
	stdin := fs.Bool("stdin", false, "read the body from standard input")
	if err := parse(fs, args); err != nil {
		return err
	}
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	ref := strings.Join(fs.Args(), " ")
	if ref == "" {
		return fmt.Errorf("%w: name a page", errUsage)
	}
	return a.withWiki(func(w *wiki.Wiki) error {
		info, err := w.Find(ref)
		if err != nil {
			return err
		}
		switch {
		case *stdin:
			piped, err := a.readStdin()
			if err != nil {
				return err
			}
			if err := w.SetBody(info.Path, piped); err != nil {
				return err
			}
		case given["m"]:
			if err := w.SetBody(info.Path, *body); err != nil {
				return err
			}
		default:
			// The whole page in the editor, front matter included, and a
			// conflict rather than a lost edit if it changed meanwhile.
			src, hash, err := w.Read(info.Path)
			if err != nil {
				return err
			}
			edited, err := a.editInEditor(string(src))
			if err != nil {
				return err
			}
			if edited == string(src) {
				a.printf("no change\n")
				return nil
			}
			if err := w.Write(info.Path, []byte(strings.TrimRight(edited, "\n")+"\n"), hash); err != nil {
				return err
			}
		}
		a.printf("%s  updated\n", a.style(ansiDim, info.File()))
		return nil
	})
}

func wikiMove(a *App, args []string) error {
	fs := a.flags("mv")
	dry := fs.Bool("dry-run", false, "print the changes without writing")
	asJSON := fs.Bool("json", false, "machine-readable output")
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return fmt.Errorf("%w: mv takes a page and its new path; quote a title with spaces", errUsage)
	}
	return a.withWiki(func(w *wiki.Wiki) error {
		info, err := w.Find(fs.Arg(0))
		if err != nil {
			return err
		}
		to := fs.Arg(1)
		if strings.HasSuffix(to, "/") {
			to += info.Path[strings.LastIndex(info.Path, "/")+1:]
		}
		if *dry {
			plan, err := w.PlanMove(info.Path, to)
			if err != nil {
				return err
			}
			if *asJSON {
				return a.writeJSON(plan)
			}
			a.printf("would move %s to %s\n", plan.From, plan.To)
			a.editTable(plan.Edits)
			a.unrewritten(plan.Unrewritten)
			return nil
		}
		res, err := w.Move(info.Path, to)
		if err != nil {
			return err
		}
		if *asJSON {
			return a.writeJSON(res)
		}
		a.printf("moved %s to %s\n", res.From, res.To)
		a.editTable(res.Edits)
		a.unrewritten(res.Unrewritten)
		if len(res.Broken) > 0 {
			a.printf("\n%s\n", a.style(ansiRed, "now broken, not rewritten:"))
			var t table
			for _, l := range res.Broken {
				where := fmt.Sprintf("%s:%d", l.Page, l.Line)
				t.add(where, l.Status, l.Written())
			}
			t.write(a.Stdout)
		}
		return nil
	})
}

func (a *App) editTable(edits []wiki.Edit) {
	var t table
	for _, e := range edits {
		where := fmt.Sprintf("%s:%d", e.Page, e.Line)
		t.addStyled([]string{where, e.Old, "->", e.New}, []string{a.style(ansiDim, where), e.Old, "->", a.style(ansiGreen, e.New)})
	}
	t.write(a.Stdout)
}

func (a *App) unrewritten(links []wiki.Link) {
	if len(links) == 0 {
		return
	}
	a.printf("\n%s\n", a.style(ansiYellow, "left as written, without a source position:"))
	for _, l := range links {
		a.printf("  %s:%d  %s\n", l.Page, l.Line, display.Line(l.Written()))
	}
}

func wikiRemove(a *App, args []string) error {
	fs := a.flags("rm")
	force := fs.Bool("force", false, "delete even while pages link to it")
	if err := parse(fs, args); err != nil {
		return err
	}
	ref := strings.Join(fs.Args(), " ")
	if ref == "" {
		return fmt.Errorf("%w: name a page", errUsage)
	}
	return a.withWiki(func(w *wiki.Wiki) error {
		info, err := w.Find(ref)
		if err != nil {
			return err
		}
		back, err := w.Remove(info.Path, *force)
		if len(back) > 0 {
			a.printf("%s\n", a.style(ansiYellow, "linked from:"))
			a.linkTable(back, true)
		}
		if err != nil {
			return err
		}
		a.printf("removed %s\n", info.File())
		return nil
	})
}

func wikiTag(a *App, args []string, add bool) error {
	if len(args) < 2 {
		return fmt.Errorf("%w: name a page and at least one tag; quote a title with spaces", errUsage)
	}
	return a.withWiki(func(w *wiki.Wiki) error {
		info, err := w.Find(args[0])
		if err != nil {
			return err
		}
		if add {
			err = w.Tag(info.Path, args[1:], nil)
		} else {
			err = w.Tag(info.Path, nil, args[1:])
		}
		if err != nil {
			return err
		}
		updated, err := w.Page(info.Path)
		if err != nil {
			return err
		}
		a.printf("%s  %s\n", a.style(ansiDim, info.File()), a.style(ansiCyan, "#"+strings.Join(updated.Tags, " #")))
		return nil
	})
}

func wikiStatus(a *App, args []string, status string) error {
	ref := strings.Join(args, " ")
	if strings.TrimSpace(ref) == "" {
		return fmt.Errorf("%w: name a task", errUsage)
	}
	return a.withWiki(func(w *wiki.Wiki) error {
		t, err := w.FindTask(ref)
		if err != nil {
			return err
		}
		if err := w.SetTaskStatus(t, status); err != nil {
			return err
		}
		where := t.Page
		if t.Line > 0 {
			where = fmt.Sprintf("%s:%d", t.Page, t.Line)
		}
		a.printf("%s  %s  %s\n", a.style(ansiDim, where), status, display.Line(t.Text))
		return nil
	})
}

func wikiPromote(a *App, args []string) error {
	fs := a.flags("promote")
	dir := fs.String("in", "tasks", "directory for the task page")
	if err := parse(fs, args); err != nil {
		return err
	}
	ref := strings.Join(fs.Args(), " ")
	if ref == "" {
		return fmt.Errorf("%w: name a checklist item", errUsage)
	}
	return a.withWiki(func(w *wiki.Wiki) error {
		t, err := w.FindTask(ref)
		if err != nil {
			return err
		}
		info, err := w.Promote(t, *dir)
		if err != nil {
			return err
		}
		a.printf("%s  %s  from %s:%d\n", a.style(ansiDim, info.File()), a.style(ansiBold, info.Title), t.Page, t.Line)
		return nil
	})
}
