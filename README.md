# gwiki

A wiki of markdown pages kept in the repository it documents.

Pages live in `.gwiki/wiki` and are committed with the code. They link to each other with `[[Page title]]` or markdown links, and to source files and line ranges such as `../../src/lexer.go#L42`. gwiki indexes the links, headings, tags and tasks in a cache it rebuilds from the pages, reports broken links, and rewrites links when a page moves.

Pages are plain files, so any editor works. gwiki adds:

- a terminal interface: an overview of the wiki, a page tree, a reader that follows links, backlinks, search, broken links and tasks;
- a language server, so Neovim, Helix or Vim complete links, flag broken ones and follow them;
- an MCP server, so a code agent can read and edit pages without overwriting yours;
- a command line for all of the above.

All of it is one executable. Every write checks that the page has not changed since it was read, and refuses rather than overwrite.

```
myproject  overview
Recent changes                                        | Health
  Design sketch  lexer/design-sketch  3h ago  Ada     |   broken links   2
  Grammar  lexer/grammar  1d ago  Ada  uncommitted    |   orphan pages   1
                                                      |   dead ends      4
Tasks  5 open, 1 overdue, 2 due within a week         |
  Ship it  tasks/ship  overdue 2026-09-01             | Structure  38 pages
  benchmark the lexer  lexer/design-sketch:12         |   lexer/  12
                                                      |   #parser  7
j k move  h l column  enter open  / search  ^p open  n new  c broken  t tasks  ? help
```

## Install

```sh
go install github.com/shakfu/gwiki/cmd/gwiki@latest
```

Or from a clone: `make install`.

## Getting started

```sh
cd your-project
gwiki init                                  # .gwiki/wiki, committed; .gwiki/cache.db, ignored
gwiki new "Design sketch" --in lexer -m "The lexer tokenizes input. See [[Grammar]]."
gwiki                                       # the interface, on the overview
```

## Pages and links

A page is a markdown file under `.gwiki/wiki`; its path without `.md` names it, such as `lexer/design-sketch`. Its title is the front matter `title`, else its first level-one heading, else its file name. Front matter can also set `tags`, and `type: task` with `status`, `priority` and `due` for a task page. Checklist items (`- [ ] text`) in any page are tasks too.

| link | reaches |
|---|---|
| `[[Design sketch]]` | a page by path, then title, then file name |
| `[[lexer/design-sketch#Tokens\|the tokens]]` | a heading, with a label |
| `[notes](../grammar.md#rules)` | a page by relative path |
| `[lexer](../../src/lexer.go#L42-L50)` | a file, or a line range in it |

A reference names a page by its path, its title, its file name, or a fragment; an ambiguous one lists the candidates.

## The interface

Run `gwiki` with no arguments. It opens on the overview:

- **Recent changes**, with the author and date of the last commit to each page, and pages git has not recorded;
- **Tasks**, overdue and due soon first;
- **Health**: broken links, orphan pages that nothing links to, and dead ends that link to nothing;
- **Structure**: directories, tags and the most-linked pages.

Every row opens its page or list. `O` returns to the overview from anywhere.

| key | |
|---|---|
| `j` `k` `g` `G` | move |
| `h` `l` | between the tree and the reader, or the overview's columns |
| `tab` `shift-tab` | next and previous link in the page |
| `enter` | open, or follow the selected link; a file link opens `$EDITOR` at the line |
| `backspace` | back |
| `b` | backlinks |
| `/` | search as you type |
| `ctrl-p` | open a page by title or path |
| `c` `f` | broken links; repairs for one |
| `t` `space` | tasks; toggle one |
| `n` `e` `r` | new page, edit the page here, move with links rewritten |
| `E` | edit the page in `$EDITOR` instead |
| `?` | every key |

Pages changed outside the interface, by an editor, git or an agent, reload within a second.

### The editor

`e` opens the page in gwiki's own editor, which is modal and follows vim: modes, counts, `d`, `c`, `y`, `>` and `<` with motions and text objects, quotes and brackets, `.`, `u` and `ctrl-r`, registers, `/` search and `:s`. Two additions are for the wiki: `il` and `al` select the link at the cursor, and `ctrl-space` ticks a checklist item.

```
:w   write the page      ctrl-]  follow the link under the cursor
:q   leave the editor    ctrl-o  back
:w!  write over a page saved elsewhere       :e!  load it and lose your edits
:preview   the page as the reader draws it   :check  the broken links in it
```

In insert mode, `enter` continues a list, numbering and checkboxes included, and `ctrl-n` completes a page after `[[`, a heading after `#`, or a path after `](`. The buffer is autosaved to `.gwiki/drafts/`, and offered again if the editor is interrupted. A write is refused when the page changed on disk since it was read.

Macros, marks, blockwise visual, `:g`, folds and mappings are not there; `?` lists what is.

## Editors

`gwiki lsp` is a language server in the same executable. An editor starts it and gets, in wiki pages: `[[` and `](` completion of pages, headings and paths; warnings on broken links; go to definition to follow a link; references for backlinks; hover; heading outlines; rename of a page with its links rewritten; and quick fixes for broken links. Open buffers are checked as typed, before saving.

Neovim 0.11 or later:

```lua
vim.lsp.config('gwiki', {
  cmd = { 'gwiki', 'lsp' },
  filetypes = { 'markdown' },
  root_markers = { '.gwiki' },
})
vim.lsp.enable('gwiki')
```

Its default LSP keys then apply: `ctrl-]` follows a link, `grr` lists backlinks, `grn` renames the page, `gra` offers fixes, `K` hovers, `ctrl-x ctrl-o` completes.

Helix, in `.helix/languages.toml` or `~/.config/helix/languages.toml`:

```toml
[language-server.gwiki]
command = "gwiki"
args = ["lsp"]

[[language]]
name = "markdown"
language-servers = ["gwiki"]
```

Vim has no built-in LSP client; with [vim-lsp](https://github.com/prabirshrestha/vim-lsp):

```vim
au User lsp_setup call lsp#register_server({'name': 'gwiki', 'cmd': {server_info->['gwiki', 'lsp']}, 'allowlist': ['markdown']})
```

A rename returns edits for the editor to apply, so the pages it changes are left modified and unsaved; save them all (`:wall`).

## Agents

```sh
claude mcp add gwiki -- gwiki mcp
```

The agent gets tools to list, search and read pages, create them, edit exact text or write whole pages against the hash it read, rename pages, list and repair broken links, and change task status. A write against a stale hash is refused and returns the current page to retry against. There is no delete tool.

## Commands

```sh
gwiki ls lexer                              # pages under a directory
gwiki show "design sketch"                  # a page, its links and backlinks
gwiki search tokeniz                        # ranked; the last word matches as a prefix
gwiki links index    gwiki backlinks grammar
gwiki check                                 # broken links; exit status 1 while any remain
gwiki check --fix                           # choose a repair for each
gwiki orphans
gwiki new "Parser notes" --in lexer -t parser -m "First line."
gwiki new "Ship it" --task
gwiki edit "parser notes"                   # opens $EDITOR on the page
gwiki mv lexer/parser-notes archive/ --dry-run   # the links it would rewrite
gwiki rm lexer/parser-notes                 # refused while pages link to it
gwiki tag grammar parser    gwiki untag grammar parser
gwiki tasks -s open
gwiki done index:12   gwiki doing tasks/ship   gwiki reopen index:12
gwiki promote index:12                      # a checklist item becomes a task page
gwiki cache --rebuild
```

Read commands take `--json`. [The design](docs/dev/wiki-design.md) covers the cache, link resolution and writes in detail.

## Notes

`gwiki notes` is the older part of gwiki: notes and tasks in a SQLite database, `.gwiki/notes.db`, committed with the project. It shares `.gwiki` with the wiki and is independent of it. Its commands are under `gwiki notes`: `gwiki notes help` lists them, `gwiki notes -g` selects the global notes, and `gwiki notes` alone opens its interface.

Notes and tasks are rows with typed columns, so any SQLite client can read, query and edit them. Every change, including one made outside gwiki, is recorded, so you can look at the project as it stood at any past moment. It is built for one user: git cannot merge two copies of a database file.

### Global notes

```sh
gwiki notes -g init                    # a project of your own, in ~/notes
gwiki notes -g init ~/work/notes       # or wherever you choose
gwiki notes -g task "renew passport"   # from any directory
gwiki notes -g                         # the interactive interface, on global notes
```

Global notes belong to you, not to a repository. Only a leading `-g` reaches them. Without it, a command outside a project fails as before, so a note run in the wrong directory never lands in global notes.

The global notes are an ordinary project, in `.gwiki/notes.db` under the directory given. A second `-g init` prints the recorded location, which is kept in `global.json` beside your identity.

### The browser view

```sh
gwiki notes serve
```

Opens a three-pane page in your browser: notebooks, entries, and one entry in full. It refreshes by itself when you write from the command line, from the terminal interface, or from an agent.

`--no-open` prints the address without opening anything. No browser is launched where there is evidently no desktop to launch it on — over SSH, under a CI runner, or on a Unix session with no display server — since it would otherwise open on the wrong machine or hang on a headless one. `--open` forces the attempt anyway.

The whole page is compiled into the binary, so there is nothing to install and it works with no network at all.

The address gwiki prints carries an access token, and the API will not answer without it. That token, not the loopback binding, is the protection: any page open in your browser can make requests to `127.0.0.1`, so without a secret one of them could read and rewrite your notes. Because the page reads the token from its own URL and sends it in a header, a script on another origin cannot obtain it.

The detail pane ends with the recorded changes to the entry you are looking at.

```
3S2YEP  fix the lexer
parser rewrite / work / fix the lexer

STATUS    open        TAGS  #bug ×  #parser ×  + tag
PRIORITY  high
DUE       2026-08-21

HISTORY · 4 CHANGES
12      add.task                    Aug 17, 01:51 PM
18      set.due 2026-08-21          Aug 17, 01:51 PM
19      add.tag bug                 Aug 17, 01:51 PM
23      link.node design sketch     Aug 17, 01:52 PM
```

`j` `k` move, `/` searches, `n` and `t` create, `space` toggles a task, `u` undoes the last delete, `esc` steps back out.

The view costs about 3 MB of binary, almost all of it `net/http`. Build with `make build-slim` (`-tags noweb`) to leave it out; the command line and the terminal interface are unaffected.

### Notes for agents

```sh
claude mcp add gwiki-notes -- gwiki notes mcp
claude mcp add gwiki-notes-global -- gwiki notes -g mcp   # the global notes
```

Registers the project with Claude Code over the Model Context Protocol. The agent gets seven tools — list, search, get, create, update, delete and restore — and the same rules as every other view: task fields are refused on notes, an ambiguous reference lists the candidates rather than guessing, and deletion is recoverable.

Entries are addressed by the same six-character handle the command line prints, so a handle you read in your terminal can be pasted straight to the agent.

`gwiki notes mcp` speaks the protocol on standard input and output and is not meant to be run by hand; a client starts and stops it. Everything on standard output is protocol, and diagnostics go to standard error.

### Notes commands

Creating and editing:

```sh
gwiki notes notebook work                        # or: gwiki notes nb work
gwiki notes note "design sketch" -b work -t design -m "body text"
gwiki notes task "fix the lexer" -b work -t bug -d friday -p high -a me
gwiki notes edit lexer --title "fix the lexer properly"
gwiki notes edit lexer                           # opens $EDITOR on the body
git log --oneline -20 | gwiki notes note "release notes" --stdin
```

Tasks:

```sh
gwiki notes done lexer        gwiki notes doing lexer      gwiki notes reopen lexer
gwiki notes due lexer friday  gwiki notes prio lexer high  gwiki notes assign lexer me
```

Finding things:

```sh
gwiki notes ls                                   # everything, in your own order
gwiki notes ls -k task -s open -p high -t bug    # filters combine
gwiki notes ls --overdue --sort due
gwiki notes search "lexer ambiguity"             # full text, ranked
gwiki notes show lexer                           # one entry, with its backlinks
gwiki notes tags
```

Entries can be named by a handle (`3S2YEP`, the last six or more characters of the id), by title, or by a fragment of the title. An ambiguous name lists the candidates rather than guessing. Quotes around a title are optional, unless the words could be split into an entry and its arguments in more than one way; then gwiki asks for them.

Organising:

```sh
gwiki notes tag lexer parser        gwiki notes untag lexer parser
gwiki notes link lexer "design sketch"           # a task pointing at its note
gwiki notes mv lexer personal                    # to another notebook
gwiki notes mv lexer --top                       # or --bottom, --before X, --after X
gwiki notes rm lexer                gwiki notes restore lexer
```

History and other views:

```sh
gwiki notes log                                  # the recorded changes
gwiki notes ls --at 2026-08-01                   # the project as it stood at the start of that day
gwiki notes ls --at 3d
gwiki notes serve                                # the browser view
gwiki notes serve --no-open                      # just print the address
gwiki notes mcp                                  # serve to an agent (clients run this)
```

Put `-g` before any command to run it on the [global notes](#global-notes).

Add `--json` to `ls`, `show` or `search` for machine-readable output.

### SQL

The database is the project; there is nothing to export.

```sh
sqlite3 .gwiki/notes.db "SELECT title, due FROM nodes WHERE status = 'open' AND deleted_at IS NULL"
duckdb -c "ATTACH '.gwiki/notes.db' AS n (TYPE sqlite); SELECT tag, count(*) FROM n.tags GROUP BY tag"
```

| table | holds |
|---|---|
| `nodes` | the workspace, notebooks, notes and tasks: `kind`, `parent`, `rank`, `title`, `body`, `status`, `priority`, `due`, `deleted_at`, and who created and last changed each |
| `tags`, `links`, `assignees` | one row per tag, link and assignee |
| `contributors` | the people who have written |
| `changes` | every change to the tables above: `at`, `author`, `node`, `field`, `old_value`, `new_value` |
| `nodes_fts` | the FTS5 full-text index over titles, bodies and tags |

Triggers keep `changes` and `nodes_fts` current, whichever client writes. The schema refuses what the tree cannot hold, such as a status on a note. A row that breaks the tree another way, such as a note inside a note, is reported and left out.

```sql
-- how long each task took from creation to done
SELECT n.title, julianday(c.at) - julianday(n.created_at) AS days
  FROM nodes n JOIN changes c ON c.node = n.id AND c.field = 'status' AND c.new_value = 'done';

-- ranked full-text search
SELECT n.title FROM nodes_fts JOIN nodes n ON n.num = nodes_fts.rowid
 WHERE nodes_fts MATCH 'lexer' ORDER BY bm25(nodes_fts, 8.0, 1.0, 5.0);
```

A running `gwiki notes serve` or terminal interface picks up an outside edit within a second. The edit is recorded with no author.

#### Diffs

git sees `notes.db` as binary. `.gwiki/.gitattributes` assigns it a diff driver named `gwiki`, and git config defines the driver, once per machine:

```sh
git config --global diff.gwiki.textconv \
  'echo .dump meta contributors nodes tags links assignees changes | sqlite3 -readonly'
```

`git diff`, `git log -p` and `git show` then print the changed rows as SQL. The full-text index is left out of the dump, since its rows are binary. Without the setting, or without `sqlite3` on the path, git falls back to "Binary files differ".

### The notes interface

Run `gwiki notes` with no arguments. Notebooks on the left, their notes and tasks on the right. Writes from the command line, the browser or an agent appear within a second.

| key | |
|---|---|
| `j` `k` `h` `l` | move, vim-style; arrows work too |
| `g` `G` | first, last |
| `enter` | open the entry full-screen |
| `n` `t` `N` | new note, task, notebook |
| `space` | toggle a task done |
| `x` `s` `o` | done, doing, open |
| `e` `r` `d` `u` | edit body, rename, delete, undo delete |
| `J` `K` | reorder |
| `/` | search as you type |
| `:` | command line, with history and tab completion |
| `?` | full key reference |

`:filter kind task`, `:filter status open`, `:sort due` and `:help` are the commands you will reach for most. `esc` clears the search, then the filter.

### How it works

The design follows [epiq](https://github.com/ljtn/epiq), a git-backed issue tracker, reimplemented in Go for notes and tasks.

**The tables are the notes.** A command changes the in-memory tree first, checked against the rules: a task field on a note is refused, a notebook cannot nest. Commit then writes the rows that changed, in one transaction. A command that fails partway writes nothing.

**History is recorded by triggers.** Each changed field becomes one row in `changes`. A write from gwiki is dated by the session's clock and carries your id; a write from any other client is dated by the wall clock. Nothing depends on gwiki being the writer.

**The file is complete after every write.** The database uses a rollback journal rather than WAL, so there is no second file holding recent writes. What you commit is what you have. `.gwiki/.gitignore` keeps out the journal, which exists only during a write.

**Projects from before the database are imported.** A `.gwiki/events` directory of JSONL logs is replayed into the tables the first time it opens, each change dated by its original event, so history survives. The logs are left in place, for you to remove.

**Fractional ranks.** Sibling order is a fixed-width 96-bit hex string, so comparing two of them is a string comparison and inserting between two is a midpoint. When repeated insertion at one spot exhausts the space, every sibling is respaced.

**Time travel replays `changes`.** `ls --at` folds the changes up to a cutoff into the rows as they stood then.

**Search is FTS5.** Every word must match and the last matches by prefix, so a partly typed word narrows results. A title containing the whole query ranks first, then BM25 with titles weighted 8, tags 5 and bodies 1. Typed text is quoted, so `c++` or `foo-bar` is searched as words rather than read as query syntax.

**The browser page keeps no model.** Every change is a request, and the server answers with the state to render. Holding a local copy in step with a database written by four front ends is exactly the class of bug that avoids.

**Every front end is a shell over one write path.** The command line, the terminal interface, the browser and the agent all call the same session layer, which is the only place that mints ids, checks the rules and resolves ranks. A rule added there holds everywhere at once; a rule added in a front end would hold in one place and quietly not in the other three.

#### Differences from epiq

- **Domain**: notebooks holding notes and tasks as peers, rather than boards, swimlanes and issues.

- **Tags are plain strings**, not registry entries with ids. A registry buys renaming a tag everywhere at once, which is not worth a layer of indirection. Contributors do keep a registry, because their identity has to outlive their display name.

- **Storage is a database, not a log.** epiq's append-only logs merge across authors; gwiki keeps typed rows for one user, and gives up that merge.

- **The browser view is one page of vanilla HTML, CSS and JavaScript** with no build step and no framework, pushed live over server-sent events rather than a websocket. The traffic is one-way and tiny, and the browser reconnects on its own.

- **The MCP server is hand-written against the protocol** rather than taken from a framework: JSON-RPC over newline-delimited stdio is a few hundred lines of standard library, and it adds about 0.1 MB to the binary.

#### Performance

Old JSONL build against the SQLite build, on the same projects: each task has a body, two tags and a priority, five events as JSONL. Median of 50 runs, process start included, on a 16-thread Linux machine.

| tasks | `ls` JSONL / SQLite | `search` | `show` | `note` | on disk |
|---|---|---|---|---|---|
| 5 | 2.9 / 4.1 ms | 3.2 / 4.4 ms | 3.0 / 4.3 ms | 3.6 / 6.8 ms | 6 KB / 116 KB |
| 100 | 4.0 / 4.8 ms | 4.4 / 5.2 ms | 3.9 / 4.8 ms | 4.6 / 7.9 ms | 120 KB / 472 KB |
| 500 | 9.4 / 7.9 ms | 10.9 / 8.3 ms | 9.2 / 7.2 ms | 10.0 / 10.3 ms | 572 KB / 1.9 MB |
| 1,000 | 15.4 / 13.2 ms | 18.0 / 13.2 ms | 14.6 / 11.5 ms | 15.1 / 14.9 ms | 1.1 MB / 3.7 MB |
| 5,000 | 65.3 / 45.4 ms | 77.4 / 45.2 ms | 59.9 / 39.4 ms | 64.2 / 43.2 ms | 5.6 MB / 18.7 MB |

Reads cross over between 100 and 500 tasks. Writes are within 3% of each other at 500 and 1,000 tasks, and faster in SQLite at 5,000. Below 1,000 tasks the two differ by at most 3.3 ms. SQLite writes sync to disk (`synchronous=FULL`); the JSONL build did not. git stores the full database file in each commit that changes it.

| binary | JSONL | SQLite |
|---|---|---|
| everything | 8.9 MB | 12.8 MB |
| `-tags noweb` | 5.2 MB | 9.2 MB |
## Development

```sh
make test        # everything, with the race detector
make check       # vet, gofmt and test
make build-slim  # without the browser view
make bench
make cover
```

## Changelog

See [CHANGELOG.md](CHANGELOG.md).

## Licence

MIT. See [LICENSE](LICENSE).
