# gnotes

Notes and tasks for one person, kept in a SQLite database in the repository they belong to.

gnotes stores everything in `.gnotes/gnotes.db`, which you commit like any other file. Notes and tasks are rows with typed columns, so any SQLite client can read, query and edit them. Every change, including one made outside gnotes, is recorded, so you can look at the project as it stood at any past moment.

It is built for one user. git cannot merge two copies of a database file, so edits made on two machines without committing in between cannot be combined.

Notes and tasks are distinct kinds sharing one tree. A note has a title, a markdown body and tags. A task has those plus a status, a priority, a due date and assignees. They sit side by side in a notebook, in whatever order you put them.

Four ways in: a command line, an interactive terminal interface, a browser page compiled into the binary, and an MCP server for agents. All four write through the same layer, so none of them can mean something different by an operation, and each notices when another writes.

```
parser rewrite                                                          1 open
 work                1| -- design sketch                              #design
 personal             | [ ] fix the lexer      ! #bug #parser 2026-08-21 @sa
                      | [x] benchmark it                              #parser
n note  t task  space done  e edit  d delete  / search  : command  ? help
```

## Install

```sh
go install github.com/shakfu/gnotes/cmd/gnotes@latest
```

Or from a clone: `make install`.

## Getting started

```sh
cd your-project
gnotes init                       # asks your name the first time only
gnotes note "design sketch" -m "The lexer tokenizes input."
gnotes task "fix the lexer" -t bug -d friday -p high
gnotes                            # open the interactive interface
```

## Global notes

```sh
gnotes -g init                    # a project of your own, in ~/notes
gnotes -g init ~/work/notes       # or wherever you choose
gnotes -g task "renew passport"   # from any directory
gnotes -g                         # the interactive interface, on global notes
```

Global notes belong to you, not to a repository. Only a leading `-g` reaches them. Without it, a command outside a project fails as before, so a note run in the wrong directory never lands in global notes.

The global notes are an ordinary project, in `.gnotes/gnotes.db` under the directory given. A second `-g init` prints the recorded location, which is kept in `global.json` beside your identity.

## The browser view

```sh
gnotes serve
```

Opens a three-pane page in your browser: notebooks, entries, and one entry in full. It refreshes by itself when you write from the command line, from the terminal interface, or from an agent.

`--no-open` prints the address without opening anything. No browser is launched where there is evidently no desktop to launch it on — over SSH, under a CI runner, or on a Unix session with no display server — since it would otherwise open on the wrong machine or hang on a headless one. `--open` forces the attempt anyway.

The whole page is compiled into the binary, so there is nothing to install and it works with no network at all.

The address gnotes prints carries an access token, and the API will not answer without it. That token, not the loopback binding, is the protection: any page open in your browser can make requests to `127.0.0.1`, so without a secret one of them could read and rewrite your notes. Because the page reads the token from its own URL and sends it in a header, a script on another origin cannot obtain it.

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

## Agents

```sh
claude mcp add gnotes -- gnotes mcp
claude mcp add gnotes-global -- gnotes -g mcp   # the global notes
```

Registers the project with Claude Code over the Model Context Protocol. The agent gets seven tools — list, search, get, create, update, delete and restore — and the same rules as every other view: task fields are refused on notes, an ambiguous reference lists the candidates rather than guessing, and deletion is recoverable.

Entries are addressed by the same six-character handle the command line prints, so a handle you read in your terminal can be pasted straight to the agent.

`gnotes mcp` speaks the protocol on standard input and output and is not meant to be run by hand; a client starts and stops it. Everything on standard output is protocol, and diagnostics go to standard error.

## Commands

Creating and editing:

```sh
gnotes notebook work                        # or: gnotes nb work
gnotes note "design sketch" -b work -t design -m "body text"
gnotes task "fix the lexer" -b work -t bug -d friday -p high -a me
gnotes edit lexer --title "fix the lexer properly"
gnotes edit lexer                           # opens $EDITOR on the body
git log --oneline -20 | gnotes note "release notes" --stdin
```

Tasks:

```sh
gnotes done lexer        gnotes doing lexer      gnotes reopen lexer
gnotes due lexer friday  gnotes prio lexer high  gnotes assign lexer me
```

Finding things:

```sh
gnotes ls                                   # everything, in your own order
gnotes ls -k task -s open -p high -t bug    # filters combine
gnotes ls --overdue --sort due
gnotes search "lexer ambiguity"             # full text, ranked
gnotes show lexer                           # one entry, with its backlinks
gnotes tags
```

Entries can be named by a handle (`3S2YEP`, the last six or more characters of the id), by title, or by a fragment of the title. An ambiguous name lists the candidates rather than guessing. Quotes around a title are optional, unless the words could be split into an entry and its arguments in more than one way; then gnotes asks for them.

Organising:

```sh
gnotes tag lexer parser        gnotes untag lexer parser
gnotes link lexer "design sketch"           # a task pointing at its note
gnotes mv lexer personal                    # to another notebook
gnotes mv lexer --top                       # or --bottom, --before X, --after X
gnotes rm lexer                gnotes restore lexer
```

History and other views:

```sh
gnotes log                                  # the recorded changes
gnotes ls --at 2026-08-01                   # the project as it stood at the start of that day
gnotes ls --at 3d
gnotes serve                                # the browser view
gnotes serve --no-open                      # just print the address
gnotes mcp                                  # serve to an agent (clients run this)
```

Put `-g` before any command to run it on the [global notes](#global-notes).

Add `--json` to `ls`, `show` or `search` for machine-readable output.

## SQL

The database is the project; there is nothing to export.

```sh
sqlite3 .gnotes/gnotes.db "SELECT title, due FROM nodes WHERE status = 'open' AND deleted_at IS NULL"
duckdb -c "ATTACH '.gnotes/gnotes.db' AS n (TYPE sqlite); SELECT tag, count(*) FROM n.tags GROUP BY tag"
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

A running `gnotes serve` or terminal interface picks up an outside edit within a second. The edit is recorded with no author.

### Diffs

git sees `gnotes.db` as binary. `.gnotes/.gitattributes` assigns it a diff driver named `gnotes`, and git config defines the driver, once per machine:

```sh
git config --global diff.gnotes.textconv \
  'echo .dump meta contributors nodes tags links assignees changes | sqlite3 -readonly'
```

`git diff`, `git log -p` and `git show` then print the changed rows as SQL. The full-text index is left out of the dump, since its rows are binary. Without the setting, or without `sqlite3` on the path, git falls back to "Binary files differ".

## The interactive interface

Run `gnotes` with no arguments. Notebooks on the left, their notes and tasks on the right. Writes from the command line, the browser or an agent appear within a second.

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

## How it works

The design follows [epiq](https://github.com/ljtn/epiq), a git-backed issue tracker, reimplemented in Go for notes and tasks.

**The tables are the notes.** A command changes the in-memory tree first, checked against the rules: a task field on a note is refused, a notebook cannot nest. Commit then writes the rows that changed, in one transaction. A command that fails partway writes nothing.

**History is recorded by triggers.** Each changed field becomes one row in `changes`. A write from gnotes is dated by the session's clock and carries your id; a write from any other client is dated by the wall clock. Nothing depends on gnotes being the writer.

**The file is complete after every write.** The database uses a rollback journal rather than WAL, so there is no second file holding recent writes. What you commit is what you have. `.gnotes/.gitignore` keeps out the journal, which exists only during a write.

**Projects from before the database are imported.** A `.gnotes/events` directory of JSONL logs is replayed into the tables the first time it opens, each change dated by its original event, so history survives. The logs are left in place, for you to remove.

**Fractional ranks.** Sibling order is a fixed-width 96-bit hex string, so comparing two of them is a string comparison and inserting between two is a midpoint. When repeated insertion at one spot exhausts the space, every sibling is respaced.

**Time travel replays `changes`.** `ls --at` folds the changes up to a cutoff into the rows as they stood then.

**Search is FTS5.** Every word must match and the last matches by prefix, so a partly typed word narrows results. A title containing the whole query ranks first, then BM25 with titles weighted 8, tags 5 and bodies 1. Typed text is quoted, so `c++` or `foo-bar` is searched as words rather than read as query syntax.

**The browser page keeps no model.** Every change is a request, and the server answers with the state to render. Holding a local copy in step with a database written by four front ends is exactly the class of bug that avoids.

**Every front end is a shell over one write path.** The command line, the terminal interface, the browser and the agent all call the same session layer, which is the only place that mints ids, checks the rules and resolves ranks. A rule added there holds everywhere at once; a rule added in a front end would hold in one place and quietly not in the other three.

### Differences from epiq

- **Domain**: notebooks holding notes and tasks as peers, rather than boards, swimlanes and issues.

- **Tags are plain strings**, not registry entries with ids. A registry buys renaming a tag everywhere at once, which is not worth a layer of indirection. Contributors do keep a registry, because their identity has to outlive their display name.

- **Storage is a database, not a log.** epiq's append-only logs merge across authors; gnotes keeps typed rows for one user, and gives up that merge.

- **The browser view is one page of vanilla HTML, CSS and JavaScript** with no build step and no framework, pushed live over server-sent events rather than a websocket. The traffic is one-way and tiny, and the browser reconnects on its own.

- **The MCP server is hand-written against the protocol** rather than taken from a framework: JSON-RPC over newline-delimited stdio is a few hundred lines of standard library, and it adds about 0.1 MB to the binary.

### Performance

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
