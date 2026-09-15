# gnotes as a project wiki

Status: proposal, 2026-09-15. Supersedes the storage model in
[sqldb-variant.md](sqldb-variant.md) section 12.

Decided:

- **Markdown files in the repository are the source of truth.** SQLite is a
  derived cache, local to each clone and never committed.
- **Writers are the developer and code agents.** No accounts, no permissions.
- **Pages live in `.gnotes/wiki`**, committed; `.gnotes/cache.db` is gitignored.
- **Both link forms**: `[[wiki]]` links and markdown links.
- **gnotes offers to fix broken links.**
- **Tasks are checklist items in pages, and pages of their own.**
- **gnotes has its own editor**, and a page may also be edited outside it.
- **Line anchors are checked for range only.**

Section 18 lists what is still open.

## 1. Goal

A knowledge base of markdown pages, stored and reviewed with the project it
describes.

- Pages link to each other and to files in the repository.
- gnotes knows every link in both directions, reports broken ones, and offers
  fixes.
- Search answers within a keystroke in the terminal interface and quickly on
  the command line.
- Writing and editing markdown happens in gnotes.
- A code agent reads and writes the same pages through the same rules.
- The pages stay useful without gnotes: in another editor, in `git diff`, and
  rendered on GitHub.

Non-goals:

- Real-time collaboration, or any sync other than git.
- Committing, or any git write. Committing is the developer's action.
- Time travel inside gnotes. History is `git log`.
- Checking external URLs, at least in the first version.
- Markdown beyond CommonMark, GitHub tables and task lists, footnotes, front
  matter and wiki links.

## 2. Why files, and what that costs

| problem with `gnotes.db` as truth | with files as truth |
|---|---|
| binary in git, 3.3-3.9x the JSONL size, a full copy per commit | text; git stores deltas |
| `git diff` needs a textconv driver | ordinary diffs and pull requests |
| two writers conflict and one side is lost | git merges line by line |
| unreadable without gnotes or sqlite3 | readable in any editor and on GitHub |

Costs:

- The `changes` table, `log` and `ls --at` go. History comes from git.
- Most of `state`, `session`, `event`, `rank` and the current `store` are
  replaced (section 15).
- Any process can change a page, so the cache detects changes rather than
  recording them (section 7), and every write checks for a concurrent one
  (section 10).

## 3. Layout

```
project/
  .gnotes/
    config.json        committed: project name, options
    .gitignore         committed: cache.db*, drafts/
    wiki/              committed: the pages
      index.md
      lexer/
        design-sketch.md
        grammar-ambiguity.md
      tasks/
        benchmark-the-lexer.md
    cache.db           derived, rebuilt on demand
    drafts/            unsaved editor buffers
```

- Only `.md` files under `.gnotes/wiki` are pages. Directories nest.
- File links may point anywhere in the repository.
- `.gnotes` is found by walking up from the working directory, as now.
- Global notes keep working: `gnotes -g` uses the same layout under `~/notes`.
- **Trade-off of a dot directory:** `rg` and many fuzzy finders skip hidden
  directories by default, so pages are less visible to other tools than
  under `wiki/`. GitHub shows them normally.

## 4. Pages

```markdown
---
title: Design sketch
tags: [design, parser]
---

# Design sketch

The lexer tokenizes input. See [[Grammar ambiguity]] and
[the lexer](../../../src/lexer.go#L42).

- [ ] benchmark the lexer due:2026-08-21
```

- **Identity is the path**, relative to `.gnotes/wiki`, without `.md`:
  `lexer/design-sketch`. No id in the file: a hand-written or agent-written page
  cannot get one wrong.
- **Title:** front matter `title`, else the first `#` heading, else the file
  name. This is gopherwiki's rule (`indexTitleAndBody`).
- **Front matter is optional YAML.** Unknown keys are kept and ignored. A
  leading `---` without a closing delimiter, or whose block is not a mapping,
  is body text, not front matter.
- **Tags** come from front matter `tags`. Inline `#tag` is not parsed: in
  markdown it collides with headings and with issue references such as `#123`.

### Tasks

Two forms, listed together by `gnotes tasks`:

| form | written as | fields |
|---|---|---|
| checklist item | `- [ ] text` in any page | done; optional inline `due:YYYY-MM-DD` |
| task page | a page with `type: task` in front matter | `status` (open, doing, done), `priority`, `due`, `assignees`, plus a body |

A checklist item keeps a small task beside the note it came from. A task page
holds a task with discussion, links and history of its own. Converting one to
the other is a command (section 9).

A checklist item is addressed by page and line. After an outside edit shifts
lines, gnotes finds it again by its text; an ambiguous match is reported, not
guessed.

## 5. Links

### Syntax

| written | kind | resolves to |
|---|---|---|
| `[[Design sketch]]` | wiki | a page by path, title or file name |
| `[[lexer/design-sketch\|the sketch]]` | wiki | the same, with a label |
| `[[Design sketch#Tokens]]` | wiki | a heading in the page |
| `[text](design-sketch.md)` | markdown | a page by relative path |
| `[text](design-sketch.md#tokens)` | markdown | a heading, by GitHub's slug rules |
| `[text](../../../src/lexer.go)` | file | a repository file |
| `[text](../../../src/lexer.go#L42)` or `#L42-L60` | line | a line range in a file |
| `![diagram](img/flow.png)` | file | a repository file |
| `[text](https://...)` | external | recorded, not checked |

Both forms are first-class. They differ in what survives a move:

- A `[[wiki]]` link names a page by title or path, so it survives moving the
  linking page. It does not render as a link on GitHub.
- A markdown link renders everywhere, but it is a relative path, so it breaks
  when either end moves unless gnotes rewrites it.

gnotes writes the form the writer chose. The editor inserts `[[` completions as
wiki links and `[` path completions as markdown links, and a rename rewrites
each link in its own form.

### Parsing

Links come from the goldmark syntax tree, so a link inside a code span or a
fenced or indented code block is not a link. Every link keeps its line, column
and byte span in the source, because a rename or a fix rewrites it in place.

gopherwiki's `WikiLinkExtension` is the starting point (section 16). It needs
two changes: the node must carry its source segment, and `#heading` must be
split from the target. Its `ExtractWikiLinks` is not used: it matches a regular
expression over the whole page, so it counts `[[...]]` inside code.

### Resolution

A wiki link target is tried in order, case-insensitively:

1. a path relative to `.gnotes/wiki`, with or without `.md`;
2. an exact page title;
3. a file name without extension, with spaces read as hyphens.

The first rule that matches exactly one page wins. More than one match is
**ambiguous**, reported like a broken link rather than guessed. Rule 3 matches
gopherwiki, which turns `[[Design Sketch]]` into the path `design-sketch`, so
its pages resolve the same way here.

A markdown link is a relative path from the page's directory, as GitHub reads
it. A path that leaves the repository is broken.

### Broken links and fixes

| status | meaning | fixes offered |
|---|---|---|
| `missing-page` | no page resolves | a page renamed away (same content hash); pages with a similar title or file name; create the page |
| `ambiguous` | several pages resolve | each candidate, rewritten as a path |
| `missing-heading` | the page exists; the heading does not | headings in that page with a similar slug |
| `missing-file` | no file at the path | files with the same name elsewhere in the repository; a rename in `git status` |
| `line-out-of-range` | the file is shorter than the anchor | drop the anchor |
| `outside-repo` | the path leaves the repository | none |

Offers are ranked, never applied without a choice:

- `gnotes check` lists broken links with their offers.
- `gnotes check --fix` asks for each link in turn; `--fix=first` takes the top
  offer where exactly one exists, for scripts.
- The terminal interface shows offers in the broken-links panel.
- Agents get the offers in the `check` result and apply one with `fix_link`.

A fix rewrites the link's byte span in its own form, through the same write
path as any edit (section 10).

A line anchor is checked for range only. A line that moved but still exists is
not detected.

## 6. The cache

`.gnotes/cache.db`, SQLite with WAL. It is derived: a schema-version mismatch or
a corrupt file is deleted and rebuilt, never reported as an error.

```sql
CREATE TABLE files    (path TEXT PRIMARY KEY, size INTEGER, mtime_ns INTEGER, hash TEXT);
CREATE TABLE pages    (path TEXT PRIMARY KEY, title TEXT, type TEXT, front TEXT, body TEXT,
                       status TEXT, priority TEXT, due TEXT);
CREATE TABLE headings (page TEXT, slug TEXT, text TEXT, level INTEGER, line INTEGER);
CREATE TABLE tags     (page TEXT, tag TEXT);
CREATE TABLE assignees(page TEXT, who TEXT);
CREATE TABLE links    (page TEXT, line INTEGER, col INTEGER, start INTEGER, end INTEGER,
                       form TEXT, kind TEXT, raw TEXT, label TEXT,
                       target TEXT, heading TEXT, line_from INTEGER, line_to INTEGER,
                       status TEXT);
CREATE TABLE checklist(page TEXT, line INTEGER, text TEXT, done INTEGER, due TEXT);
CREATE VIRTUAL TABLE pages_fts USING fts5 (title, headings, tags, body,
                       tokenize = 'unicode61 remove_diacritics 2');
```

- Backlinks are `links WHERE target = ?`. Orphans are pages with none.
- Broken links are `links WHERE status != 'ok'`.
- WAL, not the rollback journal the committed database needed: the cache is
  never committed, and WAL lets the terminal interface and browser view read
  while another process refreshes.
- Drafts live in `.gnotes/drafts/`, not in the cache, so rebuilding the cache
  cannot lose unsaved text.

## 7. Freshness

Every command brings the cache up to date before answering:

1. List the directories under `.gnotes/wiki`.
2. Stat every `.md` file on a pool of goroutines.
3. Compare size and modification time with `files`. Re-read and re-parse the
   pages that differ; delete the rows of pages that are gone.
4. Match deleted and added pages with the same content hash as renames, and
   keep them for the fix offers.
5. If the set of pages or headings changed, re-resolve link status with one
   `UPDATE` over `links`.

A write transaction is taken only when step 3 finds a change.

Measured on 5,000 pages in 50 directories, warm cache, 16-thread Linux:

| step | time |
|---|---|
| `filepath.WalkDir` with `Info` per file | 24-29 ms |
| list directories, then stat on 16 goroutines | 7-9 ms |
| read all 5,000 files (9 MB) | 73-89 ms |

An unchanged wiki of 5,000 pages therefore costs about 8 ms per command before
the query. The first build reads every file; its parse and insert time is
measured in phase 0.

File targets outside the wiki can change without any page changing. Refresh
re-checks file targets only for the pages it re-parsed; `gnotes check` stats
every distinct file target and is the command to trust.

The terminal interface and browser view run the same refresh on a one-second
poll, as the current interface does, so an outside edit or an agent's write
appears within a second.

## 8. Search

The crossover measurements show where search time goes today: `search` costs
the same as `ls` at every size, 13.2 against 13.2 ms at 1,000 tasks. The FTS5
query is not the cost; loading every row at startup is.

With the cache:

- The command line runs the freshness check and one FTS5 query. Nothing else
  is loaded.
- The terminal interface and browser view keep one connection and re-run the
  query per keystroke.
- Ranking: BM25 with title above headings above tags above body; a title
  containing the whole query ranks first, as now. Typed words are quoted, as
  `search.Query` does now.
- Snippets use FTS5 `snippet()` with control characters as match markers,
  replaced after escaping, as gopherwiki's `SearchPages` does. The terminal
  interface turns the markers into styling, the browser view into `<mark>`.

Target, checked in phase 1: `gnotes search` under 15 ms median at 5,000 pages,
process start included, against 45 ms for today's build.

## 9. Commands

| command | does |
|---|---|
| `init` | write `.gnotes/config.json` and `.gitignore`; create `.gnotes/wiki` |
| `new <title> [--in dir] [--task]` | create a page, or a task page; open the editor |
| `edit <page>` | open the page in the gnotes editor |
| `show <page>` | print the page, with outgoing links and backlinks |
| `ls [dir] [-t tag]` | list pages |
| `search <query>` | ranked full-text search with snippets |
| `links <page>`, `backlinks <page>` | outgoing and incoming links, with status |
| `check [--files] [--fix[=first]]` | report broken links with fix offers; exit status 1 when any remain |
| `orphans` | pages nothing links to |
| `mv <page> <path> [--dry-run]` | rename a page and rewrite every link to it |
| `rm <page>` | delete a page; refuse while other pages link to it, unless `--force` |
| `tag`, `untag` | edit front matter tags |
| `tasks [-s status] [--due]` | list checklist items and task pages together |
| `done`, `doing`, `reopen` | set a task page's status, or tick a checklist item |
| `promote <task>` | turn a checklist item into a task page linked from where it was |
| `log <page>` | `git log --follow` for the page |
| `cache --rebuild` | delete and rebuild the cache |
| `ui`, `serve`, `mcp` | the other front ends |

A page is named by path, title or a fragment of either. An ambiguous name lists
the candidates, as now.

## 10. Writes

Every write, from the command line, the editor, the browser view or an agent,
goes through one function:

1. The caller passes the content hash it last read (the base).
2. The page is re-read. If its hash differs from the base, the write stops with
   a conflict and the current content; nothing is written.
3. Otherwise the new content goes to a temporary file, which is renamed over
   the page.
4. The page is re-indexed in the same step.

This is gopherwiki's optimistic lock (`SavePage` with a base revision), keyed
on content hash instead of a commit, since gnotes does not commit.

- **gnotes never stages or commits.**
- **`mv` rewrites links from their byte spans,** not by searching text, in the
  link's own form. `--dry-run` prints every file and line it would change. A
  link that becomes ambiguous after the rename is reported, not rewritten.
- **`rm` has no undo in gnotes.** Recovery is `git checkout`. The refusal while
  backlinks exist replaces soft deletion.
- **Front matter edits** go through a YAML node tree, keeping key order and
  comments where the library allows.
- **A multi-file operation** (`mv`, a fix across pages) checks every base first
  and writes only if none has changed. Between the check and the last rename,
  another writer can still interleave; the window is milliseconds and the
  result is reported, not silently merged.

## 11. The editor

gnotes edits pages itself. A page may still be changed by another editor or an
agent while it is open, which the editor must handle.

### Behaviour

- Soft-wrapped markdown with styling for headings, emphasis, code, links and
  task checkboxes; broken links underlined.
- **List continuation on Enter** for bullets, numbered items (renumbering the
  rest of the list), checklists and quotes; Enter on an empty item ends the
  list. gopherwiki's `web/editor/markdown-list.js` is the specification: its
  rules are ported, not its CodeMirror code.
- **`[[` completion** from page titles and paths; `#` after a target completes
  headings. **`[`...`](` completion** from repository paths.
- `space` toggles the checkbox on the current line.
- Undo and redo; search within the page; jump to a link's target.
- A preview toggle, rendered as in the reader (section 12).

### Concurrent edits

- The buffer remembers the hash it was opened from.
- The one-second poll notices when the file changes on disk. A clean buffer
  reloads, keeping the cursor at the nearest line; a dirty buffer shows a
  banner.
- Saving a dirty buffer over a changed file is a conflict (section 10). The
  choices are: see a diff, reload and lose the edits, keep editing, or write
  the buffer to a new page.
- There is no automatic three-way merge in the first version.

### Drafts

The buffer autosaves to `.gnotes/drafts/<page-hash>.md` with its base hash and
cursor. On reopening a page with a draft, the editor offers to restore it; if
the page changed since the draft's base, it offers the diff. This is
gopherwiki's `drafts` table, moved to files so a cache rebuild cannot lose it.

### Building it

The component is the largest and least certain piece of this plan. Phase 0
decides between `bubbles/textarea` and a component of our own. It checks
whether `textarea` supports styled spans, undo and soft wrap with an accurate
cursor, and measures typing latency on a 2,000-line page. The current
dependencies are bubbletea, lipgloss and `x/ansi`; `bubbles` would be new.

## 12. Terminal interface

```
wiki                        | Design sketch                     lexer/
 index                      |
 lexer/                     | The lexer tokenizes input. See
   design sketch          < | [grammar ambiguity] and [the lexer].
   grammar ambiguity        |
 tasks/                     | - [ ] benchmark the lexer  2026-08-21
                            |---------------------------------------------
                            | links 2   backlinks 3   broken 1
                            |  -> grammar ambiguity
                            |  -> src/lexer.go:42        missing-file  f fix
/ search  ^p open  tab link  enter follow  bksp back  e edit  n new  ? help
```

- **Tree** of directories and pages on the left.
- **Reader** on the right. `tab` moves between links, `enter` follows one,
  `backspace` goes back through a history stack. A file link opens the file at
  its line in `$EDITOR`, since source files are not pages.
- **Link panel** under the page: outgoing, backlinks, broken; `f` on a broken
  link lists its fix offers.
- **`/` search as you type**, with snippets. **`ctrl-p`** opens a page by
  title or path.
- **`e`** switches to the editor at the current line. **`n`** creates a page,
  **`r`** renames with the `mv` preview, **`c`** lists broken links across the
  wiki, **`t`** lists tasks.

Rendering: headings, emphasis, lists, task lists, quotes, code and tables from
the goldmark tree, styled with lipgloss. [glamour](https://github.com/charmbracelet/glamour)
does this but brings chroma; phase 0 measures its binary cost before choosing
it over a small renderer. Neither uses goldmark's typographer or hard-wrap
options, which gopherwiki enables: typographer changes quotes and dashes, and
hard wraps render differently from GitHub.

The existing interface's input handling, command line, key reference and
polling are reused; its two-pane notebook model is not.

## 13. Agents and the browser view

### Agents

The MCP server is how code agents contribute. Tools:

| tool | does |
|---|---|
| `list`, `search` | pages, with snippets |
| `read` | a page with its hash, links, backlinks and broken links |
| `create` | a page or task page |
| `write` | replace a page body, given the base hash |
| `edit` | replace one exact text span, given the base hash |
| `rename` | rename with link rewriting, returning the changed files |
| `check` | broken links with fix offers |
| `fix_link` | apply one offer |
| `tasks`, `set_task` | list tasks; change status or tick an item |

- Every write takes a base hash, so an agent cannot overwrite the developer's
  edit or another agent's. A conflict returns the current content for the
  agent to retry against.
- `edit` exists because rewriting a whole page to change a sentence is how an
  agent loses someone else's paragraph.
- gnotes does not attribute writes. Authorship is whatever the developer
  commits; an agent that wants a trail writes it in the page.

gopherwiki's JSON API (`internal/handlers/api_pages.go`) is a reference for
request and response shapes.

### Browser view

The same tree, reader and link panel, rendered to HTML by goldmark. Editing
uses a CodeMirror 6 editor, which gopherwiki already builds in `web/editor`
with the list-continuation rules above. The token and origin protections stay
as they are. This comes after the terminal interface.

## 14. History and migration

**History** is git's. `gnotes log <page>` runs `git log --follow` on the file.
gopherwiki reads history through go-git; gnotes calls the `git` binary instead,
since it only reads history and go-git would add a large dependency. Without
git, a project has no history, and gnotes says so.

**Migration.** `gnotes migrate` converts the current `gnotes.db`, or a JSONL
project through the existing importer:

| from | to |
|---|---|
| notebook | directory, slug of its name |
| note | page, slug of its title; a collision gets a numeric suffix |
| task | task page (`type: task`) with status, priority, due and assignees |
| tags | front matter |
| link | `[[wiki]]` link by title, which survives later moves |
| deleted entry | skipped, or written under `.gnotes/wiki/.deleted/` with `--include-deleted` |

The database and logs are left in place; their history stays in git history.

## 15. What happens to the current code

Non-test lines today:

| package | lines | fate |
|---|---|---|
| `cli` | 2,536 | kept as the command framework; most commands rewritten |
| `tui` | 2,221 | input, command line and polling kept; views rewritten; editor added |
| `web` | 1,190 | server, token and origin checks kept; API and page rewritten |
| `mcp` | 1,270 | protocol kept; tools rewritten |
| `state` | 1,668 | removed; reference resolution and its ambiguity rules move to cache queries |
| `session` | 901 | removed |
| `store` | 1,393 | identity and global location kept; the JSONL reader kept for `migrate`; the rest removed |
| `event` | 408 | kept for `migrate` only |
| `rank` | 305 | removed: order is the file name |
| `ulid` | 252 | removed unless `migrate` needs it |
| `search` | 170 | query builder kept |
| `display`, `editor` | 147 | kept; `editor` now opens source files, not pages |

Roughly half of the non-test code is replaced. That is an estimate from this
table, not a count of a finished change.

## 16. What comes from gopherwiki

gopherwiki (`~/projects/gopherwiki`) is a Go wiki server with the same content
model: markdown pages in a git repository, goldmark, wiki links, front matter and
FTS5. It is MIT-licensed, with a copyright notice from its Otter Wiki origin; code
copied from it keeps that notice.

| gopherwiki | use here | change needed |
|---|---|---|
| `internal/frontmatter` | page front matter | drop the Quarto fields; add `tags`, `type`, `status`, `priority`, `due`, `assignees` |
| `renderer.WikiLinkExtension` | `[[wiki]]` parsing | keep source segments; split `#heading` |
| `renderer.ExtractWikiLinks` | not used | counts links inside code; extract from the tree instead |
| `renderer.IssueRefExtension` | not used | no issue tracker; `[[#123]]` is an ordinary wiki link here |
| `renderer.PrepareExportSource` | reference only | converts wiki links line by line and skips indented code; the rewrite here uses byte spans |
| `db.SearchPages` snippet markers | snippets in both front ends | port from `mattn/go-sqlite3` to `modernc.org/sqlite`; the SQL is unchanged |
| `WikiService.SavePage` optimistic lock | the write path | a content hash instead of a commit revision |
| `drafts` table | editor drafts | files under `.gnotes/drafts/` |
| `web/editor` (CodeMirror 6, `markdown-list.js`) | browser editor; list rules as the terminal editor's spec | none for the browser; a Go port of the rules |
| `util.Slugify`, `GetHeader` | slugs and titles | none |
| `storage` (go-git, commit per save) | not used | gnotes never commits |
| auth, sessions, issues, Quarto, render cache, feeds | not used | |

Keeping the page syntax compatible lets one set of pages be served by
gopherwiki and edited with gnotes. The one difference is resolution: gopherwiki
resolves a wiki link by path only, and gnotes also by title (section 5).

## 17. Phases

Each phase ends with `make test` and `make lint` passing and its exit criteria
recorded here.

0. **Spike.** Port the wiki-link parser with source segments. Measure parse and
   index time for 5,000 pages, and the binary size of goldmark, yaml.v3,
   glamour and `bubbles`. Prototype the editor component (section 11). Exit:
   numbers here; a renderer choice; an editor component choice.
1. **Cache and read commands:** `init`, `ls`, `show`, `search`, `links`,
   `backlinks`, `check` without `--fix`, `orphans`, `tasks`,
   `cache --rebuild`. Exit: a fixture wiki with every broken-link status is
   reported exactly; `search` under 15 ms median at 5,000 pages.
2. **Writes:** the write path with base hashes, `new`, `mv`, `rm`, `tag`,
   `done`, `promote`, `check --fix`. Exit: rename and fix fixtures covering
   both link forms, labels, headings, relative paths from other directories,
   reference-style links, and a conflicting concurrent write.
3. **Agents.** The MCP tools of section 13, on the phase 2 write path. Early,
   because agents are writers and need only the command-line core.
4. **Terminal interface:** tree, reader, link panel, search, quick open.
5. **Editor**, with conflicts and drafts.
6. **Migration** from `gnotes.db` and JSONL.
7. **Browser view.**

The work belongs on a branch until phase 4: the two storage models cannot share
a binary without doubling the front ends.

## 18. Open questions

1. **Global notes.** Do they keep `-g`, with the same layout under `~/notes`,
   or become a wiki with no repository to link into?
2. **Checklist items in other files.** Should `tasks` also collect `- [ ]`
   items from markdown outside `.gnotes/wiki`, such as `TODO.md`?
3. **Assignees without accounts.** With the developer and agents as writers,
   is `assignees` needed, and if so, what names it?
4. **Editor keys.** Plain keys with modifiers, or modal vim-style keys, or
   both behind a setting? This decides much of phase 5.
