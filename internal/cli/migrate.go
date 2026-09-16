package cli

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/shakfu/gwiki/internal/session"
	"github.com/shakfu/gwiki/internal/state"
	"github.com/shakfu/gwiki/internal/store"
	"github.com/shakfu/gwiki/internal/wiki"
)

var cmdMigrate = &command{
	name:    "migrate",
	args:    "[--dry-run] [--in <dir>] [--include-deleted] [--global]",
	summary: "copy the notes database into wiki pages",
	help: `Converts this project's notes and tasks, in .gwiki/notes.db, into pages.

    notebook   a directory, named after it
    note       a page, named after its title
    task       a task page, with its status, priority, due date and assignees
    tags       front matter
    reference  a [[wiki]] link by title, under a "Links" heading

The database is left as it is; nothing is deleted. A page that already exists
where an entry would go is left alone and reported, so the command can be run
again after adding notes. --dry-run lists what it would write.

Deleted entries are skipped. --include-deleted writes them under
.gwiki/wiki/.deleted/, which the wiki does not index, so they are kept in the
repository without appearing in the wiki.`,
	run: func(a *App, args []string) error {
		fs := a.flags("migrate")
		dryRun := fs.Bool("dry-run", false, "list what would be written")
		dir := fs.String("in", "", "a directory under .gwiki/wiki to put the notebooks in")
		includeDeleted := fs.Bool("include-deleted", false, "also write deleted entries, unindexed")
		global := fs.Bool("global", false, "migrate the global notes instead of this project's")
		if err := parse(fs, args); err != nil {
			return err
		}
		if fs.NArg() > 0 {
			return errUsage
		}

		a.Global = *global
		s, err := a.open()
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("%w\n\nthere is nothing to migrate", err)
			}
			return err
		}

		// The wiki is created when the project has none, since migrating into
		// one is the point.
		p, err := wiki.Discover(a.Dir)
		if errors.Is(err, wiki.ErrNotFound) {
			if *dryRun {
				a.printf("%s\n", a.style(ansiDim, "no wiki here yet; it would be created"))
			}
			if p, err = wiki.Init(a.Dir); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		w, err := wiki.Open(p)
		if err != nil {
			return err
		}
		defer w.Close()

		return a.migrate(s, w, migrateOptions{dryRun: *dryRun, dir: *dir, includeDeleted: *includeDeleted})
	},
}

type migrateOptions struct {
	dryRun         bool
	dir            string
	includeDeleted bool
}

// migrate converts a notes project into pages.
func (a *App) migrate(s *session.Session, w *wiki.Wiki, opts migrateOptions) error {
	st := s.State
	var pages []wiki.NewPage
	var skipped, deleted int
	var lines []string

	for _, nb := range st.Notebooks() {
		dir := path.Join(opts.dir, wiki.Slugify(nb.Title))
		for _, n := range st.Children(nb.ID) {
			if n.Kind != state.KindNote && n.Kind != state.KindTask {
				continue
			}
			page := pageFor(n, dir, st)
			// A page already there is left alone, so a second run adds only
			// what is new.
			if _, err := os.Stat(w.PageFile(path.Join(dir, wiki.Slugify(n.Title)))); err == nil {
				skipped++
				continue
			}
			pages = append(pages, page)
			lines = append(lines, fmt.Sprintf("%s/%s  %s", dir, wiki.Slugify(n.Title), n.Title))
		}
	}

	if opts.includeDeleted {
		var err error
		deleted, err = a.writeDeleted(st, w, opts)
		if err != nil {
			return err
		}
	}

	sort.Strings(lines)
	if opts.dryRun {
		for _, l := range lines {
			a.printf("%s\n", l)
		}
		a.printf("%s would be written", plural(len(pages), "page"))
		if skipped > 0 {
			a.printf(", %d already there", skipped)
		}
		a.printf("\n")
		return nil
	}

	if len(pages) > 0 {
		if _, err := w.CreateAll(pages); err != nil {
			return err
		}
	}
	a.printf("%s written to %s\n", plural(len(pages), "page"), w.PagesPath())
	if skipped > 0 {
		a.printf("%s\n", a.style(ansiDim, fmt.Sprintf("%d entries already had a page, and were left alone", skipped)))
	}
	if deleted > 0 {
		a.printf("%s\n", a.style(ansiDim, fmt.Sprintf("%s written under .deleted/, which the wiki does not index", plural(deleted, "deleted entry"))))
	}
	a.printf("%s\n", a.style(ansiDim, "the notes database is unchanged; 'gwiki notes' still reads it"))
	return nil
}

// pageFor converts one entry.
func pageFor(n *state.Node, dir string, st *state.State) wiki.NewPage {
	page := wiki.NewPage{Title: n.Title, Dir: dir, Tags: n.Tags, Body: body(n, st)}
	if n.Kind == state.KindTask {
		page.Task = true
		page.Status = n.Status.String()
		page.Priority = n.Priority.String()
		if !n.Due.IsZero() {
			page.Due = n.Due.Format("2006-01-02")
		}
		for _, id := range n.Assignees {
			page.Assignees = append(page.Assignees, st.Contributor(id))
		}
	}
	return page
}

// body is the entry's text, with its references as wiki links. A reference is
// written by title, which survives a later move of the page it names.
func body(n *state.Node, st *state.State) string {
	text := strings.TrimRight(n.Body, "\n")
	var links []string
	for _, id := range n.Links {
		if target := st.Get(id); target != nil && target.Title != "" {
			links = append(links, "- [["+target.Title+"]]")
		}
	}
	if len(links) == 0 {
		return text
	}
	if text != "" {
		text += "\n\n"
	}
	return text + "## Links\n\n" + strings.Join(links, "\n") + "\n"
}

// writeDeleted writes deleted entries under a hidden directory. They are
// written as files rather than through the wiki, which does not index hidden
// directories and so would refuse the path.
func (a *App) writeDeleted(st *state.State, w *wiki.Wiki, opts migrateOptions) (int, error) {
	dir := filepath.Join(w.PagesPath(), ".deleted")
	written := 0
	for _, n := range st.Nodes {
		if !n.Deleted || (n.Kind != state.KindNote && n.Kind != state.KindTask) {
			continue
		}
		page := pageFor(n, "", st)
		name := wiki.Slugify(n.Title)
		if name == "" {
			name = "entry"
		}
		file := filepath.Join(dir, name+"-"+lastSix(n.ID)+".md")
		if opts.dryRun {
			a.printf("%s  %s\n", filepath.Join(".deleted", filepath.Base(file)), n.Title)
			written++
			continue
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return written, err
		}
		if err := os.WriteFile(file, []byte(deletedSource(page)), 0o644); err != nil {
			return written, err
		}
		written++
	}
	return written, nil
}

// deletedSource is the page text for a deleted entry, front matter included.
func deletedSource(p wiki.NewPage) string {
	var b strings.Builder
	b.WriteString("---\ntitle: " + p.Title + "\n")
	if p.Task {
		b.WriteString("type: task\nstatus: " + p.Status + "\n")
	}
	if len(p.Tags) > 0 {
		b.WriteString("tags: [" + strings.Join(p.Tags, ", ") + "]\n")
	}
	b.WriteString("deleted: true\n---\n\n")
	if body := strings.TrimRight(p.Body, "\n"); body != "" {
		b.WriteString(body + "\n")
	}
	return b.String()
}

func lastSix(id string) string {
	if len(id) <= 6 {
		return id
	}
	return id[len(id)-6:]
}
