# gnotes

Git-backed notes and tasks, kept in the repository they belong to.

gnotes stores everything as an append-only event log inside your project. Every
view is replayed from that log, which means the full history is always
recoverable, edits made on several machines merge without conflicts, and you can
look at the project as it stood at any past moment.

Notes and tasks are distinct kinds sharing one tree. A note has a title, a
markdown body and tags. A task has those plus a status, a priority, a due date
and assignees. They sit side by side in a notebook, in whatever order you put
them.

Four ways in: a command line, an interactive terminal interface, a browser page
compiled into the binary, and an MCP server for agents. All four write through
the same layer, so none of them can mean something different by an operation,
and each notices when another writes.

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

## The browser view

```sh
gnotes serve
```

Opens a three-pane page in your browser: notebooks, entries, and one entry in
full. It refreshes by itself when you write from the command line, from the
terminal interface, or when a sync pulls in someone else's work.

`--no-open` prints the address without opening anything. No browser is launched
where there is evidently no desktop to launch it on — over SSH, under a CI
runner, or on a Unix session with no display server — since it would otherwise
open on the wrong machine or hang on a headless one. `--open` forces the
attempt anyway.

The whole page is compiled into the binary, so there is nothing to install and
it works with no network at all.

The address gnotes prints carries an access token, and the API will not answer
without it. That token, not the loopback binding, is the protection: any page
open in your browser can make requests to `127.0.0.1`, so without a secret one
of them could read and rewrite your notes. Because the page reads the token
from its own URL and sends it in a header, a script on another origin cannot
obtain it.

The detail pane ends with the events that produced the entry you are looking
at. Nothing here stores a note; a note is the sum of those lines, and the page
says so rather than hiding it.

```
3S2YEP  fix the lexer
parser rewrite / work / fix the lexer

STATUS    open        TAGS  #bug ×  #parser ×  + tag
PRIORITY  high
DUE       2026-08-21

HISTORY · 7 EVENTS
3S2YEQ  add.task                    Aug 17, 01:51 PM
QVMFXR  add.tag bug                 Aug 17, 01:51 PM
WPN5G1  set.due 2026-08-21          Aug 17, 01:51 PM
MZGDN3  link.node design sketch     Aug 17, 01:52 PM
```

`j` `k` move, `/` searches, `n` and `t` create, `space` toggles a task,
`u` undoes the last delete, `esc` steps back out.

The view costs about 3 MB of binary, almost all of it `net/http`. Build with
`make build-slim` (`-tags noweb`) to leave it out; the command line and the
terminal interface are unaffected.

## Agents

```sh
claude mcp add gnotes -- gnotes mcp
```

Registers the project with Claude Code over the Model Context Protocol. The
agent gets eight tools — list, search, get, create, update, delete, restore and
sync — and the same rules as every other view: task fields are refused on notes,
an ambiguous reference lists the candidates rather than guessing, and deletion
is recoverable.

Entries are addressed by the same six-character handle the command line prints,
so a handle you read in your terminal can be pasted straight to the agent.

`gnotes mcp` speaks the protocol on standard input and output and is not meant
to be run by hand; a client starts and stops it. Everything on standard output
is protocol, and diagnostics go to standard error.

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

Entries can be named by a handle (`3S2YEP`, the tail of the id), by title, or by
a fragment of one. An ambiguous name lists the candidates rather than guessing.

Organising:

```sh
gnotes tag lexer parser        gnotes untag lexer parser
gnotes link lexer "design sketch"           # a task pointing at its note
gnotes mv lexer personal                    # to another notebook
gnotes mv lexer --top                       # or --bottom, --before X, --after X
gnotes rm lexer                gnotes restore lexer
```

History and sync:

```sh
gnotes log                                  # the raw events
gnotes ls --at 2026-08-01                   # the project as it stood then
gnotes ls --at 3d
gnotes sync                                 # commit the logs to git
gnotes sync --push                          # and exchange with origin
gnotes export | sqlite3 notes.db            # the project as a SQL database
gnotes serve                                # the browser view
gnotes serve --no-open                      # just print the address
gnotes mcp                                  # serve to an agent (clients run this)
```

Add `--json` to `ls`, `show` or `search` for machine-readable output.

## SQL

```sh
gnotes export | sqlite3 notes.db
```

Renders the whole project as a SQL script: the tree, the tags and links, the
raw event log, a full-text index, and a history table holding every value each
field has ever held. Anything that reads SQLite can then read your notes.

```sh
sqlite3 notes.db "SELECT title, due FROM nodes WHERE status = 'open'"
duckdb -c "ATTACH 'notes.db' AS n (TYPE sqlite); SELECT * FROM n.events"
```

This is for the questions the command line cannot ask. Which tags occur
together, how long tasks take from creation to done, who has been writing and
when. The history table makes one more possible: the project as it stood at any
past event, as an index lookup rather than a replay.

```sql
SELECT node, value AS title FROM node_history
 WHERE field = 'title'
   AND from_seq <= 120 AND (to_seq IS NULL OR to_seq > 120);
```

The script is emitted rather than the database, so gnotes carries no database
driver: the command costs 65 KB of binary where a driver would have cost
several megabytes, more than the browser view. The script ends with worked
queries for each of the above.

The database is derived and disposable. The event logs stay the only source of
truth, a stale copy is fixed by exporting again, and exporting the same log
twice produces the same bytes. `--no-history` drops the point-in-time table;
`--no-fts` drops the index, for a SQLite built without FTS5.

## The interactive interface

Run `gnotes` with no arguments. Notebooks on the left, their notes and tasks on
the right.

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

`:filter kind task`, `:filter status open`, `:sort due`, `:sync push` and
`:help` are the commands you will reach for most. `esc` clears the search, then
the filter.

## How it works

The design follows [epiq](https://github.com/ljtn/epiq), a git-backed issue
tracker, reimplemented in Go for notes and tasks.

**Everything is an event.** `.gnotes/events/<your-id>.<your-name>.jsonl` holds
one JSON object per line, appended and never rewritten. Deleting a note appends
a deletion; it does not remove anything.

```json
{"v":1,"id":"01M07S0S1X…","ref":"01M07S0S1M…","a":"add.notebook","node":"01M07S…","parent":"01M07S…","rank":"7fffffffffffffffffffffff","name":"work"}
```

**One file per author, so git never conflicts.** Two people working offline
append to different paths, so a merge is a union of files rather than a textual
conflict.

**Causal ordering, not timestamps.** Every event names the last event its author
had seen. Those references form a tree, and a depth-first walk of it — with
siblings ordered by ULID — produces one canonical sequence that every machine
agrees on. Sorting by wall clock would not: two machines with skewed clocks
would replay the same data differently. The references make the order a property
of the data.

**Fractional ranks.** Sibling order is a fixed-width 96-bit hex string, so
comparing two of them is a string comparison and inserting between two is a
midpoint. When repeated insertion at one spot exhausts the space, a rebalance
event respaces every sibling — recorded, so every machine arrives at the same
ranks.

**Time travel is free.** An event id is a ULID, so it carries its own timestamp.
Viewing the past means replaying the events before a cutoff. Nothing is stored
for it.

**Search is rebuilt, not maintained.** The inverted index is built from the tree
at startup, in a few milliseconds. An index on disk would be one more thing to
invalidate on sync and merge between machines.

**The browser page keeps no model.** Every change is a request, and the server
answers with the state to render. Holding a local copy in step with an
append-only log written by four front ends and by other machines is exactly the
class of bug that avoids.

**Every front end is a shell over one write path.** The command line, the
terminal interface, the browser and the agent all call the same session layer,
which is the only place that mints events, chains their references and resolves
ranks. A rule added there holds everywhere at once; a rule added in a front end
would hold in one place and quietly not in the other three.

### Differences from epiq

- **Domain**: notebooks holding notes and tasks as peers, rather than
  boards, swimlanes and issues.
- **Wire format**: the action is a fixed field and the payload is inlined,
  rather than the action being the payload's key. epiq's shape forces a map
  decode on every line just to discover which action it is; this one decodes
  into a single struct in one pass.
- **Tags are plain strings**, not registry entries with ids. A registry buys
  renaming a tag everywhere at once, which is not worth an extra event and a
  layer of indirection. Contributors do keep a registry, because their identity
  has to outlive their display name.
- **State lives on your working branch** by default, so notes travel with the
  code and appear in your diffs. `eventsRoot` in `.gnotes/project.json` is the
  seam for moving them into a worktree on a separate branch later.
- **Sync is explicit about the remote.** `gnotes sync` commits only the
  `.gnotes` paths, leaving whatever you have staged untouched. Pulling and
  pushing needs `--push`, because the logs are on your working branch and moving
  it is your decision.
- **The browser view is one page of vanilla HTML, CSS and JavaScript** with no
  build step and no framework, pushed live over server-sent events rather than
  a websocket. The traffic is one-way and tiny, and the browser reconnects on
  its own.
- **The MCP server is hand-written against the protocol** rather than taken from
  a framework: JSON-RPC over newline-delimited stdio is a few hundred lines of
  standard library, and it adds about 0.1 MB to the binary.

### Performance

Measured on an M-series laptop.

| | |
|---|---|
| load and replay 20,000 events | 17 ms |
| canonical sort, 100,000 events | 14 ms |
| build the search index, 400,000 tokens | 23 ms |
| decode one event | 1.7 µs, 11 allocations |
| binary, everything | 7.4 MB |
| binary, `-tags noweb` | 4.2 MB |

Author logs are parsed in parallel; the canonical sort works in place; the
tokenizer hands back substrings of the input rather than allocating per word.

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
