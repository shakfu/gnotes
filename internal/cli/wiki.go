package cli

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"strconv"
	"strings"

	"github.com/shakfu/gnotes/internal/display"
	"github.com/shakfu/gnotes/internal/mcp"
	"github.com/shakfu/gnotes/internal/wiki"
)

// cmdWiki groups the commands for the markdown wiki under .gnotes/wiki. They
// sit beside the notes commands until the wiki replaces them; see
// docs/dev/wiki-design.md.
var cmdWiki = &command{
	name:    "wiki",
	args:    "<command> [arguments]",
	summary: "the markdown wiki in .gnotes/wiki",
	help: `Pages are markdown files under .gnotes/wiki, committed with the project.
.gnotes/cache.db indexes their titles, headings, links, tags and tasks, and is
rebuilt from the pages whenever it is missing or stale.

  init                      create .gnotes/wiki here
  ls [dir] [-t tag]         list pages
  show <page>               a page with its links and backlinks
  search <query> [-n N]     ranked full-text search
  links <page>              a page's outgoing links and their status
  backlinks <page>          the links that reach a page
  check [--fix[=first]]     every broken link, and repairs to choose from;
                            exit status 1 while any remain
  orphans                   pages no other page links to
  tasks [-s status]         checklist items and task pages
  cache --rebuild           delete the cache and index every page again
  mcp                       serve the wiki to an agent over MCP

  new <title> [--in dir] [--task] [-t tag]... [-m body | --stdin]
  edit <page> [-m body | --stdin]   replace the body, or open $EDITOR
  mv <page> <path> [--dry-run]      rename, rewriting links to and from it
  rm <page> [--force]               delete; refused while pages link to it
  tag <page> <tag>...    untag <page> <tag>...
  done <task>    doing <task>    reopen <task>
  promote <item> [--in dir]         a checklist item becomes a task page

A page is named by its path, its title, its file name, or a fragment. A task is
a task page, "page:line" for a checklist item, or a fragment of its text. Every
write checks that the page has not changed since it was read, and refuses
rather than overwrite. Read commands and mv --dry-run take --json.`,
	run: func(a *App, args []string) error {
		if len(args) == 0 {
			return byName["help"].run(a, []string{"wiki"})
		}
		sub, ok := wikiCommands[args[0]]
		if !ok {
			return fmt.Errorf("%w: unknown wiki command %q", errUsage, args[0])
		}
		if a.Global {
			return errors.New("the wiki does not support -g yet")
		}
		return sub(a, args[1:])
	},
}

var wikiCommands = map[string]func(*App, []string) error{
	"init":      wikiInit,
	"ls":        wikiList,
	"show":      wikiShow,
	"search":    wikiSearch,
	"links":     wikiLinks,
	"backlinks": wikiBacklinks,
	"check":     wikiCheck,
	"orphans":   wikiOrphans,
	"tasks":     wikiTasks,
	"cache":     wikiCache,
	"new":       wikiNew,
	"edit":      wikiEdit,
	"mv":        wikiMove,
	"rm":        wikiRemove,
	"tag":       func(a *App, args []string) error { return wikiTag(a, args, true) },
	"untag":     func(a *App, args []string) error { return wikiTag(a, args, false) },
	"done":      func(a *App, args []string) error { return wikiStatus(a, args, "done") },
	"doing":     func(a *App, args []string) error { return wikiStatus(a, args, "doing") },
	"reopen":    func(a *App, args []string) error { return wikiStatus(a, args, "open") },
	"promote":   wikiPromote,
	"mcp":       wikiMCP,
}

// wikiMCP serves the wiki on standard input and output. Register it with
// Claude Code from inside the project:
//
//	claude mcp add gnotes-wiki -- gnotes wiki mcp
func wikiMCP(a *App, args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("%w: mcp takes no arguments", errUsage)
	}
	// Standard output carries only protocol frames from here on.
	return a.withWiki(func(w *wiki.Wiki) error {
		return mcp.NewWiki(w, "gnotes-wiki", Version).Serve(a.Stdin, a.Stdout, a.Stderr)
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
	fs := a.flags("wiki ls")
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
	fs := a.flags("wiki search")
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
	fs := a.flags("wiki check")
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
	fs := a.flags("wiki orphans")
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
	fs := a.flags("wiki tasks")
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
	fs := a.flags("wiki cache")
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
	fs := a.flags("wiki new")
	dir := fs.String("in", "", "directory under .gnotes/wiki")
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
	fs := a.flags("wiki edit")
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
	fs := a.flags("wiki mv")
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
	fs := a.flags("wiki rm")
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
	fs := a.flags("wiki promote")
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
