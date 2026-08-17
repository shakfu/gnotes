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
```

Add `--json` to `ls`, `show` or `search` for machine-readable output.

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

### Performance

Measured on an M-series laptop.

| | |
|---|---|
| load and replay 20,000 events | 17 ms |
| canonical sort, 100,000 events | 14 ms |
| build the search index, 400,000 tokens | 23 ms |
| decode one event | 1.7 µs, 11 allocations |

Author logs are parsed in parallel; the canonical sort works in place; the
tokenizer hands back substrings of the input rather than allocating per word.

## Development

```sh
make test      # everything, with the race detector
make check     # vet, gofmt and test
make bench
make cover
```

## Licence

MIT. See [LICENSE](LICENSE).
