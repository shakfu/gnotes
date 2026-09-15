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
3. Compare a fingerprint of every page's name, size and modification time with
   the stored one. If it matches, stop.
4. Take the write lock and compare again: another process may have indexed the
   same change meanwhile.
5. Compare size and modification time with `files`. Re-read and re-parse the
   pages that differ; delete the rows of pages that are gone.
6. Insert the changed pages' rows, resolving their links against every page as
   it will be after this refresh.
7. Re-resolve links on other pages whose target names a page added, changed or
   removed, by path, title or file name.

A removed page's content hash is kept, for the last 1,000 removals, so a page
that reappears elsewhere with the same content is a rename, in the same refresh
or a later one.

A page edited twice within the filesystem's timestamp resolution, keeping its
size, is not seen as changed. `cache --rebuild` recovers.

Measured on 5,000 pages in 50 directories, warm cache, 16-thread Linux:

| step | time |
|---|---|
| `filepath.WalkDir` with `Info` per file | 24-29 ms |
| list directories, then stat on 16 goroutines | 7-9 ms |
| read all 5,000 files (9 MB) | 73-89 ms |

An unchanged wiki of 5,000 pages costs about 9 ms per command before the query,
and a first build about 1.9 s (section 17, phase 1).

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

Until phase 4 these are `gnotes wiki <command>`, beside the notes commands they
replace.

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
- Modal, vim-style keys (below).
- A preview toggle, rendered as in the reader (section 12).

### Keys

The editor is modal and follows vim, so a vim user's habits work. Where vim
and Neovim differ, such as `Y`, it follows vim. It reads no vimrc.

- **Modes:** normal, insert, replace (`R`), visual (`v`, `V`), operator-pending,
  and the `:` and `/` lines.
- **Counts** before motions, operators and commands: `3j`, `d2w`, `2dd`.
- **Motions:** `h j k l`, `gj gk` on wrapped rows, `w b e W B E ge`, `0 ^ $`,
  `gg G {n}G`, `f F t T ; ,`, `%`, `{ }`, `H M L`, `ctrl-d ctrl-u ctrl-f
  ctrl-b`, `n N * #`.
- **Operators** with any motion or text object: `d c y > < gu gU g~`; doubled
  (`dd`, `cc`, `yy`); and `D C Y x X s S r J ~ p P`.
- **Text objects:** `iw aw iW aW is as ip ap`; quotes `i" a" i' a'` and the
  same for backticks; brackets `i( a( ib i[ a[ i{ a{ i< a<`; and `il al` for a
  markdown or wiki link.
- **Insert:** `i a I A o O gi`; `backspace ctrl-w ctrl-u ctrl-t ctrl-d
  ctrl-o`; `esc` and `ctrl-[` leave. `ctrl-n` and `ctrl-p` complete, as in vim,
  and `[[` opens completion. Enter continues lists.
- **Repeat and undo:** `.`, `u`, `ctrl-r`.
- **Registers:** unnamed, `"a`-`"z`, `"0`, `"_`. `"+` writes the system
  clipboard through OSC 52; pasting from it is the terminal's paste.
- **Search:** `/ ?` with Go regular expressions, which differ from vim's
  (no `\<`, `\v`); `:noh`.
- **Ex:** `:w :q :wq :x :q! :e!`, `:{n}`, `:s/re/new/[g]` on the line, a visual
  range, or `%`; `:preview`.
- **Wiki keys, from vim's own:** `ctrl-]` and `gf` follow the link under the
  cursor, `ctrl-o` and `ctrl-t` go back, `gx` shows an external link.
  `ctrl-space` toggles a checklist item, as in vimwiki; some terminals send it
  as `ctrl-@`, which is accepted too.

Not in the first version: macros (`q`, `@`), marks, line undo (`U`),
blockwise visual (`ctrl-v`), `:g`, ex ranges other than a visual range and
`%`, folds, splits and mappings. A vim user will hit these; the key reference lists them as
missing rather than letting a keypress do something else.

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

A component of our own, on bubbletea and lipgloss, not `bubbles/textarea`
(phase 0, section 17). It keeps the page as lines, styles and wraps only the
lines on screen, and records edits for undo. The prototype does not yet draw a
cursor, map a cursor through wide characters and wrapped rows, select or use
the clipboard; each of those is bounded by the rows on screen, not by the page.

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
the goldmark tree, styled with lipgloss, by a renderer of our own.
[glamour](https://github.com/charmbracelet/glamour) was measured and rejected
(phase 0). The renderer does not use goldmark's typographer or hard-wrap
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

0. **Spike.** Done; results below.
1. **Cache and read commands.** Done; results below.
2. **Writes:** the write path with base hashes, `new`, `mv`, `rm`, `tag`,
   `done`, `promote`, `check --fix`. Done; results below. Exit: rename and fix fixtures covering
   both link forms, labels, headings, relative paths from other directories,
   reference-style links, and a conflicting concurrent write.
3. **Agents.** The MCP tools of section 13, on the phase 2 write path. Early,
   because agents are writers and need only the command-line core. Done;
   results below.
4. **Terminal interface:** tree, reader, link panel, search, quick open.
   Done; results below.
5. **Language server** (`gnotes wiki lsp`), before the editor of section 11,
   which is then built only if still needed. Done; results below.
6. **Migration** from `gnotes.db` and JSONL.
7. **Browser view.**

The work belongs on a branch until phase 4: the two storage models cannot share
a binary without doubling the front ends.

### Phase 0 results (2026-09-15)

16-thread Linux machine, warm filesystem cache.

**Parser.** `internal/markdown` reads front matter, title, headings (with
GitHub slugs), links and checklist items, each with its source position. It
is goldmark with GFM, footnotes, and the wiki-link parser ported from
gopherwiki with source offsets. Tests cover both link forms, labels, anchors,
reference-style links, autolinks, links in inline, fenced and indented code,
front matter offsets, and concurrent use.

- Positions are exact where they exist, and missing, not guessed, where they
  do not. A markdown destination's offset comes from goldmark returning a
  slice of the source; the parser checks that the slice aliases the source
  rather than searching for equal bytes. Two cases have no position: text
  under tab-expanded indentation, which goldmark copies, and the use site of a
  reference-style link, whose destination is the shared definition.
- Destinations are kept as written, with backslash escapes and percent
  encoding. Resolution in phase 1 decodes them.

**Parse and index**, one page of 1.3 KB with front matter, four sections, both
link forms, a file link and checklist items:

| | time |
|---|---|
| parse one page | 73 us, 480 allocations |
| read 5,000 pages | 46-52 ms |
| parse 5,000 pages, one goroutine | 346-358 ms |
| parse 5,000 pages, 16 goroutines | 63-66 ms |
| insert 165,000 rows in one transaction | 477-499 ms |
| FTS5 query matching all 5,000 pages | 0.86 ms |

A first build is about 0.6 s, most of it inserts. After that, a changed page
costs one parse and its rows.

**Binary size**, stripped, each added to a program already using bubbletea,
lipgloss and SQLite:

| added | size |
|---|---|
| goldmark, with GFM and footnotes, parse and HTML | +721 KB |
| yaml.v3 | +303 KB |
| `bubbles/textarea` | +578 KB |
| glamour | +7.86 MB |

glamour also needs a lipgloss prerelease (`v1.1.1-0.20250404203927`) newer
than the `v1.1.0` gnotes uses.

**Renderer: our own, on the goldmark tree.** glamour costs more than twice the
whole browser view, almost all of it chroma for code highlighting.

**Editor component: our own.** Keystroke latency, an insert and a full redraw
of a 100x40 view, median of 500:

| page lines | `bubbles/textarea` | prototype |
|---|---|---|
| 200 | 2.8 ms | 33 us |
| 500 | 5.0 ms | 35 us |
| 1,000 | 9.9 ms | 35 us |
| 2,000 | 19.2 ms | 37 us |

`textarea` grows by about 9.6 us per line of page and passes one 60 Hz frame
near 1,700 lines. It also has no undo, styles only the whole text or the
cursor line (so it cannot highlight markdown or underline a broken link), and
stops at 10,000 lines. The prototype undid 500 edits in 2 ms. The prototype
is incomplete (section 11); its latency is a lower bound, and the design
keeps each missing feature bounded by the rows on screen.

The size and editor programs were throwaway and are not in the tree.

### Phase 1 results (2026-09-15)

`internal/wiki` holds the cache, refresh, link resolution and queries;
`gnotes wiki` exposes `init`, `ls`, `show`, `search`, `links`, `backlinks`,
`check`, `orphans`, `tasks` and `cache --rebuild`, each read command with
`--json`.

**Exit criterion 1, met.** A fixture wiki holds one link for each status and
resolution rule, and `TestEveryLinkStatus` checks every link's kind, status and
target. Further tests cover deletion, re-creation, renames across refreshes,
title and heading changes reaching links on other pages, file changes outside
the wiki, a stale or corrupt cache, concurrent refreshes, and the commands'
output, exit status and JSON.

**Exit criterion 2, met for selective queries only.** Median of 50 runs of the
built binary, process start included, on 5,000 generated pages (1.3 KB each,
four sections, 16 links and four checklist items per page):

| command | median |
|---|---|
| `search`, one page matches | 11.7 ms |
| `search`, about 40% of pages match | 19.8 ms |
| `search`, every page matches | 30.9 ms |
| `show` | 11.5 ms |
| `backlinks` | 10.8 ms |
| `search` after editing or retitling one page | 34 ms |
| `check` | 105 ms |
| `ls`, printing 5,000 rows | 40 ms |
| `tasks -s open`, printing 15,000 rows | 73 ms |
| `cache --rebuild` | 1.9 s |

Of an unchanged command's time, the scan is 5-8 ms and process start and
opening the cache about 3 ms. A query matching every page spends about 11 ms
in BM25 alone, so a broad query cannot meet 15 ms from the command line. The
terminal interface keeps the cache open and skips the scan per keystroke.

Changes from the design, found while building it:

- **The fingerprint** (section 7, step 3) replaced reading the file table on
  every command, which cost 5 ms.
- **Resolution per affected link.** Re-resolving every page link after any
  change cost 143 ms per edited page at 5,000 pages; resolving only links that
  can name a changed page, through normalised target columns, cost 34 ms.
  New links are resolved as they are inserted.
- **Search ranks in three steps**: BM25 picks up to 200 candidates, titles
  reorder them, and snippets and tags are built for the rows returned.
  Building them for every match cost 20-30 ms.
- **Renames are matched across refreshes**, not only within one, because the
  terminal interface's poll can see a deletion and an addition separately.
- **A reference resolves by file name** before fragments, so `page-42` is not
  ambiguous with `page-4200`.
- **The first build is 1.9 s**, not the spike's 0.6 s: the cache has more
  indexes and tables than the spike's. Dropping indexes during a large build
  saved 17% and was not kept, since the index list would have to track the
  schema by hand.

### Phase 2 results (2026-09-15)

`gnotes wiki` adds `new`, `edit`, `mv`, `rm`, `tag`, `untag`, `done`, `doing`,
`reopen`, `promote` and `check --fix[=first]`. Every write, one page or many,
goes through one batch: all bases are checked under the cache's write lock,
then each page is written to a synced temporary file and renamed.

**Exit criterion, met.** Tests in `internal/wiki` check exact page contents
after a move for:

- wiki links by title, path, file name and heading, with and without labels;
- markdown links, repository-rooted links, angle-bracket destinations and
  reference definitions;
- the moved page's own relative links to pages, headings, files and lines;
- a link that becomes ambiguous on a page the move does not edit, and one
  already broken that is not reported.

Further tests cover a move refused when a linking page changed after
planning, a conflicting write from a second process, fix offers for each
broken status, front matter edits, task status, promotion and removal. The CLI
tests run each command, including `$EDITOR` and interactive `check --fix`.

No fixture produced an incoming link without a source position, so the path
that reports such links as not rewritten has no test.

**Timings.** Median of the built binary, process start included, on the 5,000
pages of phase 1:

| command | median |
|---|---|
| `tag`, `done` on a checklist item, `edit --stdin`, `new`, `rm` | 37-40 ms |
| `mv`, 4 incoming links | 73 ms |
| `mv --dry-run`, 1,000 incoming links | 65 ms |
| `mv`, 1,000 incoming links in 500 pages | 662 ms |

A one-page write costs two scans and one re-index, about 38 ms, close to
phase 1's 34 ms for a read after an edit. A move writes and syncs every page
it edits, then re-indexes them.

Changes from the design, found while building it:

- **`new` does not open an editor**, and `edit` opens `$EDITOR`, checked
  against the page's hash, until the phase 5 editor exists.
- **A write re-indexes after its renames**, not in the same step. A crash
  between them leaves the cache stale, and the next command's scan corrects
  it.
- **A move reports newly broken links from the links it can change**: those
  on the pages written and those naming either path or the page's title.
  Comparing every broken link cost 110 ms on the benchmark wiki, which has
  15,011 broken links.
- **A file name matches with spaces read as hyphens**, so `edit parser notes`
  finds `parser-notes.md` after its title heading is removed.
- **Not built:** `check --files`, `tasks --due` and `log`.

### Phase 3 results (2026-09-15)

`gnotes wiki mcp` serves the wiki over MCP on standard input and output, as
`gnotes-wiki`:

```sh
claude mcp add gnotes-wiki -- gnotes wiki mcp
```

It has the eleven tools of section 13, named `gnotes_list`, `gnotes_search`,
`gnotes_read`, `gnotes_check`, `gnotes_tasks`, `gnotes_create`, `gnotes_edit`,
`gnotes_write`, `gnotes_rename`, `gnotes_fix_link` and `gnotes_set_task`. The
protocol code is shared with `gnotes mcp`, which keeps serving the database.

**Exit criterion: the tools work on the phase 2 write path.** Tests drive the
server through its framing and cover:

- an edit and a whole-page write against a stale hash, after an outside save:
  refused, nothing written, and the error carries the current hash and source;
- writes refusing a title or fragment where a path is required;
- a rename with `dry_run`, then for real;
- check offers, a fix refused for a destination not offered, and a fix applied;
- a checklist item refused after a line was inserted above it;
- tool descriptions, schemas and annotations, for both servers.

A command-line test runs `gnotes wiki mcp` and checks that standard output
holds only frames.

**Timings.** One server process on the 5,000 pages of phase 1, median; each
call starts with a refresh:

| tool | median |
|---|---|
| `gnotes_read`, `gnotes_search` (one match), `gnotes_tasks` (one page) | 5-6 ms |
| `gnotes_edit` | 27 ms |
| `gnotes_check`, 20 links with offers, of 15,011 broken | 211 ms |
| `gnotes_check`, one page | 126 ms |

`check` re-examines every file link in the wiki before filtering to a page,
which is most of its time.

Changes from the design, found while building it:

- **`write` replaces the whole source**, front matter included, not the body.
  An agent changes tags or status with `edit` on the front matter, so no tag
  tool is needed.
- **`fix_link` takes the link's page, line and text and the chosen
  destination**, which must be one of the current offers. An offer number
  would name a different repair once the list changed.
- **`set_task` requires the item's text** for a checklist item, since
  `page:line` names another item after a line is inserted above it.
- **Writes take exact paths**; reads also take titles and fragments.
- **Edits at cached byte offsets check the page against the cache.** Ticking
  an item or promoting it used an offset from the cache on a page read later,
  so a save in between could put the box character in the wrong place. Phase
  2 had this fault; moves already checked.
- **Offers for many links share one page index**, and the similar-name search
  stops an edit distance once it passes the limit. Twenty offers took 695 ms
  before, loading the index for each link.
- **No `delete` tool**, as in section 13. gnotes has no undo, so deletion is
  left to the developer.

### Phase 4 results (2026-09-15)

`gnotes wiki ui` opens the interface of section 12. It starts on `index`, or
the first page, and has:

- a tree of directories and pages, with folding;
- a reader drawn by `internal/render`, with `tab` and `shift-tab` between links,
  `enter` to follow and `backspace` to go back;
- a link panel with counts, the selected link's target and status, and
  backlinks (`b`);
- search as you type (`/`), quick open by title or path (`ctrl-p`), broken
  links across the wiki (`c`) and tasks (`t`, `space` toggles);
- repairs for a broken link (`f`), a new page (`n`), and a move with the
  number of links it rewrites shown before it runs (`r`);
- a one-second poll that reloads pages changed outside the interface and
  follows a rename of the open page.

**Renderer.** `internal/render` walks goldmark's tree: headings, emphasis,
code, lists, checklists, quotes, fenced code, tables, rules and footnotes. It
wraps at spaces to the width, records the output line of every link and
heading, and maps each output line to its source line. Control characters in
page text are replaced. A 5 KB page renders in 1.75 ms, so a selection
change renders the page again rather than patching lines.

**Exit criterion: the interface works on the wiki and fits the terminal.**
Tests drive the model with key messages over a real wiki and check following
page, heading, broken and file links; back; backlinks; search; quick open;
repairing a link; toggling a task; creating and moving a page; the poll; an
edit that conflicts with an outside save; an empty wiki; and every screen at
sizes from 100x30 down to 1x1, with no line wider than the terminal. A run
in a pseudo-terminal on the 5,000-page wiki starts, draws and quits.

**Timings.** One process on the 5,000 pages of phase 1, including drawing the
frame, median:

| action | median |
|---|---|
| start and first frame | 25 ms |
| scroll one line | 0.05 ms |
| select the next link | 0.3 ms |
| open a page | 0.8 ms |
| search keystroke | 13.8 ms (26 ms for one letter, which matches every page) |
| quick open keystroke | 1.3 ms |
| poll with nothing changed | 10.9 ms, once a second |
| `c`, checking 15,011 broken links | 82 ms |
| `t`, listing 15,000 tasks | 34 ms |

Changes from the design, found while building it:

- **It lives in `internal/tui`** beside the notes interface, reusing the input
  field, styles and poll. Both interfaces are in one binary until phase 6
  removes the notes one.
- **No `:` command line.** Every action has a key; a command line returns if
  an action needs arguments a prompt cannot take.
- **`e` opens `$EDITOR` on the whole page**, not the gnotes editor at the
  current line, until phase 5. The write is checked against the hash read
  before editing; on a conflict the edited text is kept in a temporary file
  and its path shown.
- **A file link opens `$EDITOR` with `+N`** for a line anchor. Editors that
  do not take `+N`, such as VS Code, open the file at its start or fail.
- **External links are shown, not opened.**
- **`space` pages down in the reader.** Toggling a checklist item is in the
  task list.
- **The tree sorts by path**, so `page-100` comes before `page-2`.

### Phase 5 results: language server (2026-09-16)

`gnotes wiki lsp` speaks LSP 3.17 on standard input and output. It opens the
wiki at the workspace root the editor sends, or the directory it runs in.

| request | does |
|---|---|
| diagnostics | a warning on each broken link in an open buffer, as typed |
| completion | pages after `[[`, headings after `#`, paths after `](` |
| definition | the page, heading or file line a link names |
| references | links to the page, or to the page a link names |
| hover | a page's title and backlinks; a line link's lines; a broken link's problem |
| document and workspace symbols | headings as an outline; pages by title |
| rename | moves a page: its link edits and the file rename, for the editor to apply |
| `workspace/willRenameFiles` | the link edits when the editor renames a page itself |
| code actions | each repair `check --fix` offers, and creating a missing page |

It writes nothing. Edits go to the editor, which applies them to its buffers;
the server sees the result on save or within a second.

**Unsaved buffers.** `wiki.Snapshot` holds the page index, every page's
headings included, and resolves a buffer's links as refresh would, with the
buffer's own headings. A test checks that it gives the same links as the
cache for every page of the phase 1 fixture. The snapshot is loaded again
only when a refresh finds a change.

**Exit criterion: an editor gets completion, diagnostics, following and
rename on the wiki.** Tests drive the server through its framing as an editor
would, with UTF-8 and UTF-16 positions, and cover each request above; a rename
applied to the files leaves no new broken link; renames are refused over
unsaved changes to a page they edit, and in an editor that cannot rename
files. Helix 25.07 ran against the built binary: it initialized, received the
diagnostic for a broken link at the right range, followed a link to its page,
and shut the server down.

**Timings.** One server on the 5,000 pages of phase 1, median, framing
included:

| request | median |
|---|---|
| initialize, index loaded | 55 ms |
| change to diagnostics, one page | 0.17 ms |
| `[[` completion, 200 of 5,000 pages | 0.7 ms |
| definition | 0.14 ms |
| references | 0.3 ms |
| save after an outside change, index reloaded | 65 ms |

Changes from the design, found while building it:

- **A rename does not write.** `gnotes wiki mv` writes each page atomically
  against its hash; through LSP the editor applies the edits, and the pages
  it edits stay unsaved until the user saves them.
- **A rename is refused when a page it edits has unsaved changes**, since the
  edits are positioned in the saved files.
- **Only pages under `.gnotes/wiki` are served.** A markdown file elsewhere in
  the repository, such as `README.md`, gets no diagnostics or completion.
- **Whole-document sync.** The server asks for the full text on each change;
  pages are small, and a page of 5 KB checks in under a millisecond.

## 18. Open questions

1. **Global notes.** Do they keep `-g`, with the same layout under `~/notes`,
   or become a wiki with no repository to link into?
2. **Checklist items in other files.** Should `tasks` also collect `- [ ]`
   items from markdown outside `.gnotes/wiki`, such as `TODO.md`?
3. **Assignees without accounts.** With the developer and agents as writers,
   is `assignees` needed, and if so, what names it?
4. **Editor keys.** Decided: modal, vim-style (section 11).
5. **A language server.** Decided: built first, in the same executable, as
   `gnotes wiki lsp` (phase 5). Whether the editor of section 11 is still
   needed is open.
