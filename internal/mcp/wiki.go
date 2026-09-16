package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"

	"github.com/shakfu/gwiki/internal/wiki"
)

// wikiInstructions tell the model what the wiki server is for.
const wikiInstructions = `This project keeps a wiki of markdown pages under .gwiki/wiki, committed with
the code. Pages link with [[Page title]] or [[path/page#Heading|label]], and
with markdown links to pages, files and line ranges such as ../../src/lexer.go#L42.

A page is named by its path under .gwiki/wiki, without .md, such as
lexer/design-sketch. Reading tools also accept a title or a fragment; writing
tools take the exact path.

Every write takes the hash returned when the page was read. If the page changed
since, nothing is written and the error carries the current hash and content;
read it, redo the change against it, and retry. Prefer gwiki_edit for changing
part of a page, so text someone else added is kept.

gwiki does not stage or commit. The developer reviews changes with git.`

var wikiRegistry = []registered{
	// ------------------------------------------------------------ reading

	{
		tool: tool{
			Name:  "gwiki_list",
			Title: "List wiki pages",
			Description: `List the wiki's pages with their paths, titles, tags and, for task pages,
status and due date.

Call this to see what exists before creating a page, or to find the path of a
page to read. Use gwiki_search to find pages by what they say.`,
			Annotations: &annotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: ptr(false)},
			InputSchema: props(nil, object{
				"dir":   str("Restrict to pages under this directory, such as \"lexer\"."),
				"tag":   str("Restrict to pages with this tag."),
				"limit": object{"type": "integer", "description": "Maximum pages to return. Defaults to 200."},
			}),
		},
		run: (*Server).wikiList,
	},

	{
		tool: tool{
			Name:  "gwiki_search",
			Title: "Search the wiki",
			Description: `Search the full text of every page: titles, headings, tags and bodies.

Call this when looking for something by what it says rather than where it
lives. All words must match; the last word also matches as a prefix. Results
are ranked, title matches first, each with the fragment that matched.`,
			Annotations: &annotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: ptr(false)},
			InputSchema: props([]string{"query"}, object{
				"query": str("Words to search for."),
				"limit": object{"type": "integer", "description": "Maximum results to return. Defaults to 20."},
			}),
		},
		run: (*Server).wikiSearch,
	},

	{
		tool: tool{
			Name:  "gwiki_read",
			Title: "Read a page",
			Description: `Read one page: its source exactly as stored, its hash, headings, outgoing
links with their status, and the links from other pages that reach it.

Call this before changing a page. Every write needs the hash returned here, and
gwiki_edit needs text copied exactly from the source.`,
			Annotations: &annotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: ptr(false)},
			InputSchema: props([]string{"page"}, object{
				"page": str("The page's path, its title, or a distinctive fragment of either."),
			}),
		},
		run: (*Server).wikiRead,
	},

	{
		tool: tool{
			Name:  "gwiki_check",
			Title: "Find broken links",
			Description: `List broken links: links to missing pages, headings or files, ambiguous wiki
links, and line ranges past the end of a file. Each comes with the repairs
gwiki can offer.

Call this after renaming or deleting things, or before finishing work on the
wiki. Apply a repair with gwiki_fix_link.`,
			Annotations: &annotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: ptr(false)},
			InputSchema: props(nil, object{
				"page":  str("Restrict to links on this page, by path."),
				"limit": object{"type": "integer", "description": "Maximum broken links to return with their repairs. Defaults to 20."},
			}),
		},
		run: (*Server).wikiCheck,
	},

	{
		tool: tool{
			Name:  "gwiki_tasks",
			Title: "List tasks",
			Description: `List tasks: checklist items ("- [ ] text") in any page, and task pages, which
have type: task in their front matter.

Call this to see what is outstanding. A checklist item is named page:line; a
task page by its path. Pass either to gwiki_set_task.`,
			Annotations: &annotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: ptr(false)},
			InputSchema: props(nil, object{
				"status": enum("Restrict to tasks with this status. Checklist items are open or done.", "open", "doing", "done"),
				"page":   str("Restrict to tasks on this page, by path or title."),
			}),
		},
		run: (*Server).wikiTasks,
	},

	// ------------------------------------------------------------ writing

	{
		tool: tool{
			Name:  "gwiki_create",
			Title: "Create a page",
			Description: `Create a page, or a task page. Its file name is made from the title; a name
already taken gets a numeric suffix. The page starts with the title as a
heading, followed by the body.

Search first, so the wiki does not get two pages on one subject. Returns the
new page's path and hash.`,
			Annotations: &annotations{DestructiveHint: ptr(false), OpenWorldHint: ptr(false)},
			InputSchema: props([]string{"title"}, object{
				"title": str("The page's title, on one line."),
				"dir":   str("Directory under .gwiki/wiki to create it in, such as \"lexer\". Omit for the top."),
				"task":  boolean("Create a task page, with type: task and status: open."),
				"tags":  strList("Tags for the front matter."),
				"body":  str("Markdown after the title heading."),
			}),
		},
		run: (*Server).wikiCreate,
	},

	{
		tool: tool{
			Name:  "gwiki_edit",
			Title: "Replace text in a page",
			Description: `Replace one exact piece of a page's source with new text.

This is the way to change part of a page. The old text must occur exactly once;
include enough of the surrounding text to make it unique. The base hash must be
the page's current hash, from gwiki_read or the last write. Returns the new
hash.`,
			Annotations: &annotations{DestructiveHint: ptr(true), OpenWorldHint: ptr(false)},
			InputSchema: props([]string{"page", "base", "old", "new"}, object{
				"page": str("The page's path."),
				"base": str("The hash of the source the old text was copied from."),
				"old":  str("Text to replace, copied exactly, including whitespace."),
				"new":  str("Replacement text. Empty deletes the old text."),
			}),
		},
		run: (*Server).wikiEdit,
	},

	{
		tool: tool{
			Name:  "gwiki_write",
			Title: "Replace a whole page",
			Description: `Replace a page's whole source, front matter included, with new content.

Use gwiki_edit instead unless most of the page changes. The base hash must be
the page's current hash; a page changed since is refused, so another writer's
text is never lost silently. Returns the new hash.`,
			Annotations: &annotations{DestructiveHint: ptr(true), OpenWorldHint: ptr(false)},
			InputSchema: props([]string{"page", "base", "content"}, object{
				"page":    str("The page's path."),
				"base":    str("The hash of the source this content replaces."),
				"content": str("The page's complete new source."),
			}),
		},
		run: (*Server).wikiWrite,
	},

	{
		tool: tool{
			Name:  "gwiki_rename",
			Title: "Rename a page",
			Description: `Move a page to a new path and rewrite the links to it and its own relative
links, each in its own form.

Call it with dry_run first to see every change. Pages it edits get new hashes,
so read them again before writing to them. Links that become ambiguous are
reported, not rewritten.`,
			Annotations: &annotations{DestructiveHint: ptr(false), OpenWorldHint: ptr(false)},
			InputSchema: props([]string{"page", "to"}, object{
				"page":    str("The page's current path."),
				"to":      str("The new path. Ending it with / keeps the file name."),
				"dry_run": boolean("Report the changes without making them."),
			}),
		},
		run: (*Server).wikiRename,
	},

	{
		tool: tool{
			Name:  "gwiki_fix_link",
			Title: "Repair a broken link",
			Description: `Apply one of the repairs gwiki_check offered for a broken link, rewriting
only the link's destination.

Pass the page, line and link as gwiki_check listed them, and the new
destination of the chosen repair. A wiki link that showed its target keeps
showing the old text as its label.`,
			Annotations: &annotations{DestructiveHint: ptr(false), OpenWorldHint: ptr(false)},
			InputSchema: props([]string{"page", "line", "link", "new"}, object{
				"page": str("The path of the page holding the link."),
				"line": object{"type": "integer", "description": "The link's line."},
				"link": str("The link as gwiki_check wrote it, such as [[Old title]] or ../src/x.go."),
				"new":  str("The new destination, one of those gwiki_check offered."),
			}),
		},
		run: (*Server).wikiFixLink,
	},

	{
		tool: tool{
			Name:  "gwiki_set_task",
			Title: "Change a task's status",
			Description: `Set a task page's status, or tick or clear a checklist item.

A checklist item is open or done; a task page can also be doing. Pass the
item's text as gwiki_tasks listed it, so a line that now holds a different
item is refused.`,
			Annotations: &annotations{DestructiveHint: ptr(false), IdempotentHint: true, OpenWorldHint: ptr(false)},
			InputSchema: props([]string{"task", "status"}, object{
				"task":   str("page:line for a checklist item, or a task page's path."),
				"status": enum("The new status.", "open", "doing", "done"),
				"text":   str("The task's text as listed. Required for a checklist item."),
			}),
		},
		run: (*Server).wikiSetTask,
	},
}

// pagePath names an existing page by its exact path, as writes require.
func (s *Server) pagePath(ref string) (string, error) {
	p, err := wiki.CleanPath(ref)
	if err != nil {
		return "", err
	}
	if _, _, err := s.wiki.Read(p); err != nil {
		return "", fmt.Errorf("no page at %q; gwiki_list and gwiki_search give paths", p)
	}
	return p, nil
}

// writeErr adds what a writer needs to retry to a conflict.
func writeErr(err error) error {
	var conflict *wiki.ErrConflict
	if !errors.As(err, &conflict) || conflict.CurrentHash == "" {
		return err
	}
	return fmt.Errorf("%w\n\nhash: %s\n\n%s", err, conflict.CurrentHash, sourceBlock(conflict.Current))
}

// sourceBlock is a page's source, marked off so that trailing whitespace and
// the final newline are visible.
func sourceBlock(src []byte) string {
	return "source:\n" + string(src) + "\n(end of source)"
}

func infoLine(p wiki.PageInfo) string {
	var b strings.Builder
	b.WriteString(p.Path)
	if p.Title != path.Base(p.Path) {
		fmt.Fprintf(&b, "  %q", p.Title)
	}
	if p.Type == "task" {
		status := p.Status
		if status == "" {
			status = "open"
		}
		fmt.Fprintf(&b, "  task:%s", status)
	}
	if p.Priority != "" {
		b.WriteString("  priority:" + p.Priority)
	}
	if p.Due != "" {
		b.WriteString("  due:" + p.Due)
	}
	for _, t := range p.Tags {
		b.WriteString("  #" + t)
	}
	return b.String()
}

func linkLine(l wiki.Link) string {
	s := fmt.Sprintf("%s:%d  %s", l.Page, l.Line, l.Written())
	switch {
	case l.Status != wiki.StatusOK:
		s += "  " + l.Status
	case l.Resolved != "" && l.Kind != wiki.KindExternal:
		s += "  -> " + l.Resolved
	}
	return s
}

func limited(n, limit, def int) int {
	if limit <= 0 {
		limit = def
	}
	return min(n, limit)
}

func more(total, shown int, what string) string {
	if total > shown {
		return fmt.Sprintf("\n\n%d more %s not shown; narrow the request or raise the limit.", total-shown, what)
	}
	return ""
}

func (s *Server) wikiList(raw json.RawMessage) (string, error) {
	var a struct {
		Dir   string `json:"dir"`
		Tag   string `json:"tag"`
		Limit int    `json:"limit"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}
	pages, err := s.wiki.Pages(a.Dir, a.Tag)
	if err != nil {
		return "", err
	}
	if len(pages) == 0 {
		return "No pages match.", nil
	}
	n := limited(len(pages), a.Limit, 200)
	lines := make([]string, n)
	for i, p := range pages[:n] {
		lines[i] = infoLine(p)
	}
	return strings.Join(lines, "\n") + more(len(pages), n, "pages"), nil
}

func (s *Server) wikiSearch(raw json.RawMessage) (string, error) {
	var a struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}
	limit := a.Limit
	if limit <= 0 {
		limit = 20
	}
	hits, err := s.wiki.Search(a.Query, limit)
	if err != nil {
		return "", err
	}
	if len(hits) == 0 {
		return "No pages match.", nil
	}
	strip := strings.NewReplacer("\x02", "", "\x03", "")
	var b strings.Builder
	for i, h := range hits {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(infoLine(h.PageInfo))
		if snip := strings.Join(strings.Fields(strip.Replace(h.Snippet)), " "); snip != "" {
			b.WriteString("\n  " + snip)
		}
	}
	return b.String(), nil
}

func (s *Server) wikiRead(raw json.RawMessage) (string, error) {
	var a struct {
		Page string `json:"page"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}
	info, err := s.wiki.Find(a.Page)
	if err != nil {
		return "", err
	}
	src, hash, err := s.wiki.Read(info.Path)
	if err != nil {
		return "", err
	}
	page, err := s.wiki.Page(info.Path)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s\nfile: %s\nhash: %s\n", infoLine(info), info.File(), hash)
	if len(page.Headings) > 0 {
		b.WriteString("\nheadings:\n")
		for _, h := range page.Headings {
			fmt.Fprintf(&b, "  %d  %s %s  #%s\n", h.Line, strings.Repeat("#", h.Level), h.Text, h.Slug)
		}
	}
	for _, group := range []struct {
		name  string
		links []wiki.Link
	}{{"links", page.Links}, {"backlinks", page.Backlinks}} {
		if len(group.links) == 0 {
			continue
		}
		b.WriteString("\n" + group.name + ":\n")
		for _, l := range group.links {
			b.WriteString("  " + linkLine(l) + "\n")
		}
	}
	b.WriteString("\n" + sourceBlock(src))
	return b.String(), nil
}

func (s *Server) wikiCheck(raw json.RawMessage) (string, error) {
	var a struct {
		Page  string `json:"page"`
		Limit int    `json:"limit"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}
	broken, err := s.wiki.Check()
	if err != nil {
		return "", err
	}
	if a.Page != "" {
		p, err := wiki.CleanPath(a.Page)
		if err != nil {
			return "", err
		}
		var on []wiki.Link
		for _, l := range broken {
			if l.Page == p {
				on = append(on, l)
			}
		}
		broken = on
	}
	if len(broken) == 0 {
		return "No broken links.", nil
	}

	n := limited(len(broken), a.Limit, 20)
	var b strings.Builder
	fmt.Fprintf(&b, "%d broken links.\n", len(broken))
	all, err := s.wiki.OffersFor(broken[:n])
	if err != nil {
		return "", err
	}
	for i, l := range broken[:n] {
		b.WriteString("\n" + linkLine(l) + "\n")
		offers := all[i]
		if len(offers) == 0 {
			b.WriteString("  no repair offered\n")
		}
		for _, o := range offers {
			fmt.Fprintf(&b, "  new: %s  (%s)\n", o.New, o.Label)
		}
	}
	return strings.TrimRight(b.String(), "\n") + more(len(broken), n, "broken links"), nil
}

func taskLine(t wiki.Task) string {
	if t.Line == 0 {
		s := fmt.Sprintf("%s  [%s]  %s", t.Page, t.Status, t.Text)
		if t.Priority != "" {
			s += "  priority:" + t.Priority
		}
		if t.Due != "" {
			s += "  due:" + t.Due
		}
		return s
	}
	box := "[ ]"
	if t.Status == "done" {
		box = "[x]"
	}
	return fmt.Sprintf("%s:%d  %s  %s", t.Page, t.Line, box, t.Text)
}

func (s *Server) wikiTasks(raw json.RawMessage) (string, error) {
	var a struct {
		Status string `json:"status"`
		Page   string `json:"page"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}
	f := wiki.TaskFilter{Status: a.Status}
	if a.Page != "" {
		info, err := s.wiki.Find(a.Page)
		if err != nil {
			return "", err
		}
		f.Page = info.Path
	}
	tasks, err := s.wiki.Tasks(f)
	if err != nil {
		return "", err
	}
	if len(tasks) == 0 {
		return "No tasks match.", nil
	}
	lines := make([]string, len(tasks))
	for i, t := range tasks {
		lines[i] = taskLine(t)
	}
	return strings.Join(lines, "\n"), nil
}

func (s *Server) wikiCreate(raw json.RawMessage) (string, error) {
	var a struct {
		Title string   `json:"title"`
		Dir   string   `json:"dir"`
		Task  bool     `json:"task"`
		Tags  []string `json:"tags"`
		Body  string   `json:"body"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}
	info, err := s.wiki.Create(wiki.NewPage{Title: a.Title, Dir: a.Dir, Task: a.Task, Tags: a.Tags, Body: a.Body})
	if err != nil {
		return "", err
	}
	_, hash, err := s.wiki.Read(info.Path)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("created %s\nfile: %s\nhash: %s", info.Path, info.File(), hash), nil
}

func (s *Server) wikiEdit(raw json.RawMessage) (string, error) {
	var a struct {
		Page string `json:"page"`
		Base string `json:"base"`
		Old  string `json:"old"`
		New  string `json:"new"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}
	p, err := s.pagePath(a.Page)
	if err != nil {
		return "", err
	}
	hash, err := s.wiki.Replace(p, a.Old, a.New, a.Base)
	if err != nil {
		return "", writeErr(err)
	}
	return fmt.Sprintf("edited %s\nhash: %s", p, hash), nil
}

func (s *Server) wikiWrite(raw json.RawMessage) (string, error) {
	var a struct {
		Page    string `json:"page"`
		Base    string `json:"base"`
		Content string `json:"content"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}
	p, err := s.pagePath(a.Page)
	if err != nil {
		return "", err
	}
	if a.Base == "" {
		return "", errors.New("base is the hash from gwiki_read; use gwiki_create for a new page")
	}
	if err := s.wiki.Write(p, []byte(a.Content), a.Base); err != nil {
		return "", writeErr(err)
	}
	return fmt.Sprintf("wrote %s\nhash: %s", p, wiki.Hash([]byte(a.Content))), nil
}

func (s *Server) wikiRename(raw json.RawMessage) (string, error) {
	var a struct {
		Page   string `json:"page"`
		To     string `json:"to"`
		DryRun bool   `json:"dry_run"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}
	from, err := s.pagePath(a.Page)
	if err != nil {
		return "", err
	}
	to := a.To
	if strings.HasSuffix(to, "/") {
		to += path.Base(from)
	}

	var plan *wiki.MovePlan
	var broken []wiki.Link
	if a.DryRun {
		plan, err = s.wiki.PlanMove(from, to)
	} else {
		var res *wiki.MoveResult
		if res, err = s.wiki.Move(from, to); err == nil {
			plan, broken = &res.MovePlan, res.Broken
		}
	}
	if err != nil {
		return "", writeErr(err)
	}

	var b strings.Builder
	verb := "moved"
	if a.DryRun {
		verb = "would move"
	}
	fmt.Fprintf(&b, "%s %s to %s\n", verb, plan.From, plan.To)
	if len(plan.Edits) > 0 {
		b.WriteString("\nlinks rewritten:\n")
		for _, e := range plan.Edits {
			fmt.Fprintf(&b, "  %s:%d  %s -> %s\n", e.Page, e.Line, e.Old, e.New)
		}
	}
	if len(plan.Unrewritten) > 0 {
		b.WriteString("\nnot rewritten, with no source position to edit:\n")
		for _, l := range plan.Unrewritten {
			b.WriteString("  " + linkLine(l) + "\n")
		}
	}
	if len(broken) > 0 {
		b.WriteString("\nbroken by the move:\n")
		for _, l := range broken {
			b.WriteString("  " + linkLine(l) + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func (s *Server) wikiFixLink(raw json.RawMessage) (string, error) {
	var a struct {
		Page string `json:"page"`
		Line int    `json:"line"`
		Link string `json:"link"`
		New  string `json:"new"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}
	p, err := s.pagePath(a.Page)
	if err != nil {
		return "", err
	}
	broken, err := s.wiki.Check()
	if err != nil {
		return "", err
	}
	for _, l := range broken {
		if l.Page != p || l.Line != a.Line || l.Written() != a.Link {
			continue
		}
		offers, err := s.wiki.Offers(l)
		if err != nil {
			return "", err
		}
		var news []string
		for _, o := range offers {
			if o.New == a.New {
				if err := s.wiki.Fix(l, o); err != nil {
					return "", writeErr(err)
				}
				return fmt.Sprintf("fixed %s:%d  %s -> %s", p, a.Line, a.Link, o.New), nil
			}
			news = append(news, o.New)
		}
		if len(news) == 0 {
			return "", fmt.Errorf("gwiki offers no repair for %s; change it with gwiki_edit", a.Link)
		}
		return "", fmt.Errorf("%q is not an offered repair; the offers are: %s", a.New, strings.Join(news, ", "))
	}
	return "", fmt.Errorf("no broken link %s on line %d of %s; run gwiki_check again", a.Link, a.Line, p)
}

func (s *Server) wikiSetTask(raw json.RawMessage) (string, error) {
	var a struct {
		Task   string `json:"task"`
		Status string `json:"status"`
		Text   string `json:"text"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}
	var t wiki.Task
	i := strings.LastIndexByte(a.Task, ':')
	if line, err := strconv.Atoi(a.Task[i+1:]); i > 0 && err == nil {
		p, err := s.pagePath(a.Task[:i])
		if err != nil {
			return "", err
		}
		if t, err = s.wiki.FindTask(fmt.Sprintf("%s:%d", p, line)); err != nil {
			return "", err
		}
		if a.Text == "" {
			return "", errors.New("pass the item's text as gwiki_tasks listed it")
		}
	} else {
		p, err := s.pagePath(a.Task)
		if err != nil {
			return "", err
		}
		tasks, err := s.wiki.Tasks(wiki.TaskFilter{Page: p})
		if err != nil {
			return "", err
		}
		found := false
		for _, candidate := range tasks {
			if candidate.Line == 0 {
				t, found = candidate, true
			}
		}
		if !found {
			return "", fmt.Errorf("%s is not a task page; name a checklist item as page:line", p)
		}
	}
	if a.Text != "" && strings.TrimSpace(a.Text) != t.Text {
		return "", fmt.Errorf("%s now holds %q; list tasks again", a.Task, t.Text)
	}
	if err := s.wiki.SetTaskStatus(t, a.Status); err != nil {
		return "", writeErr(err)
	}
	return taskLine(wiki.Task{Page: t.Page, Line: t.Line, Text: t.Text, Status: a.Status, Priority: t.Priority, Due: t.Due}), nil
}
