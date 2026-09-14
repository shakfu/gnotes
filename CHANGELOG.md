# Changelog

Notable changes to gnotes, newest first.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). From the first tagged release onwards the project follows [semantic versioning](https://semver.org/spec/v2.0.0.html), where a breaking change means one that stops an existing event log from replaying correctly.

## [Unreleased]

Nothing has been tagged yet, so everything below is the initial body of work.

### Added

**Notes and tasks.** Notebooks holding notes and tasks as peers. A note has a title, a markdown body and tags; a task has those plus a status (open/doing/done), a priority, a due date and assignees. They are distinct kinds, so a task-only operation aimed at a note is refused rather than quietly giving the note a status. Entries can reference each other, and both directions of a reference are shown.

**Command line.** 29 commands in all, covering creation, editing, task fields, tags, links, moving, deletion and restore, alongside history, sync, export and the two other views. Entries are addressed by a six-character handle, by title, or by a fragment of one; an ambiguous name lists the candidates instead of guessing. Ranked full-text search over titles, bodies and tags. `--json` on `ls`, `show` and `search` for scripting.

**Interactive interface.** A two-pane terminal browser with vim movement, a `:` command line with history and tab completion, and `/` search that narrows as you type.

**Browser view.** `gnotes serve` opens a three-pane page in your browser. The whole page is compiled into the binary, so there is nothing to install and it works with no network. It refreshes by itself when any other front end writes, or when a sync brings in someone else's work. The detail pane ends with the events that produced the entry, which is the one thing this view can show that an ordinary notes application cannot.

**Agent access.** `gnotes mcp` serves the project over the Model Context Protocol, so an agent can read and write notes and tasks through eight tools. It is a front end like the others rather than a separate path: an agent is subject to the same rules a person is, so it cannot put a status on a note or delete something irrecoverably.

**Time travel.** `gnotes ls --at 2026-08-01` or `--at 3d` replays the log up to a past moment and lists the project as it stood then. Nothing is stored for this; it is a prefix of the events already on disk.

**Git sync.** `gnotes sync` commits the event logs, staging only the `.gnotes` paths so that whatever else you have staged or edited is untouched. `--push` also pulls from and pushes to `origin`; it is opt-in because the logs live on your working branch, and moving that branch is your decision.

The commit runs your hooks. A repository that requires signing or an audit trail should not have notes as its one exception, so a hook that refuses fails the sync with git's own message.

**Global notes.** `gnotes -g init [dir]` creates a personal project in `dir` or `~/notes`, or adopts one already there, and records its location. A leading `-g` sends any command to it, including `ui`, `serve` and `mcp`. Outside a project there is no fallback to it: a command without `-g` still refuses, so a note run in the wrong directory cannot land there. The location is kept in `global.json`, not `user.json`, because `whoami --set` rewrites `user.json` with only the identity fields.

**SQL export.** `gnotes export | sqlite3 notes.db` renders the project as a SQL
script: the tree, the tags and links, the raw event log, a full-text index, and a history table recording every value each field has ever held. It exists for the questions the command line cannot ask — what was open on a given date, how long tasks take, which tags occur together — and for reading the notes from anything that speaks SQLite, DuckDB included.

The script is emitted rather than the database, so gnotes needs no database driver: the command costs 65 KB of binary against the several megabytes a driver would have added. The result is derived and disposable; the event logs remain the only source of truth, and exporting the same log twice produces the same bytes.

### Fixed

**References named the wrong entry.** An unquoted multi-word reference used only its first word, so `gnotes done the lexer` marked "the plan" done. Commands taking one reference now read every word. Commands taking a reference and then values try every split, and refuse when more than one resolves. An id suffix shorter than the six-character handle is no longer matched; two title letters were often valid id characters, so `rm db` could delete an unrelated entry. An exact title beats longer titles starting with it. The workspace matches only when asked for. `restore` reports ambiguity instead of guessing, in the command line, the terminal interface and the agent server.

**Writes by another process went unseen.** The browser view and the agent server read the logs' state after their own commit, so a write landing in between was marked seen and never loaded. The session now tracks what it loaded and absorbs only its own append, checked by byte count. A failed load is retried, and the browser view refuses writes until it succeeds. The terminal interface now polls once a second, keeps the cursor on the same entry across a reload, filter or sort, and runs sync in the background. The browser view also shared a node's tag slice with responses encoded after unlocking, a data race under concurrent tag edits.

**Concurrent edits against a delete were lost.** A note added to a notebook that another author had deleted was rejected on replay, and restoring the notebook did not bring it back. It now arrives deleted and returns with the notebook. Edits to a deleted entry are kept. Restoring a notebook no longer restores entries deleted on their own before it. The export's history table replays the same rules; it had recorded descendants of a deleted notebook as live. Logs containing these cases replay differently.

**One identity on two machines conflicted in git.** Both machines append to the same log file, and git's line merge reports two additions at the end of a file as a conflict. The events directory now carries a `.gitattributes` with `merge=union`, added to existing projects on their next sync. The terminal interface, browser view and agent server run git with prompts disabled and a two-minute limit per command. The command line still prompts. `sync --push` on a detached HEAD is refused; it had passed the commit id to git as a branch name.

**Due dates were compared as instants.** A date without a time read back as midnight UTC, so a task due today was overdue all day, and from the evening before west of Greenwich. It is now a calendar date in the reader's time zone, and a time typed without a zone is local. `ls --at` reads dates in local time, refuses a future cutoff and durations such as `1.5d`, and judges overdue as of the cutoff.

**One clock set ahead moved everyone's events into the future.** Each new event was dated after the last one in the log, so a machine years ahead dated every later event, by every author, in that future, and time travel hid them all. The floor is now capped at five minutes past the local clock. Time travel dates an event stamped after the reader's clock by the first event written after it.

**Appending rebalanced a notebook every 95 entries.** An append took the midpoint to the end of the rank space, halving it each time; 1,000 notes wrote 11 rebalances, 61% of the log. Appends and prepends now step 2^64. A malformed rank from another log triggers a rebalance instead of failing every insert beside it.

**The export corrupted NUL bytes and was not reproducible.** A NUL inside a string literal ends the string for sqlite3, which dropped the rest of the value or aborted the load; it is now written as `char(0)`. The script was stamped with the wall clock; it is now dated by its last event, in the `last_event_at` meta key that replaces `exported_at`.

**The browser view lost edits.** The detail pane was rebuilt only when its entry left the list, so an added tag did not appear, and renaming an entry back to its previous title was silently not saved. It now refreshes on every change and keeps a field that is being typed in. A newer search could render an older one's results, Escape in the new-entry dialog could still create the entry, and the first change after the page loaded could be missed. A link to an entry that is deleted or not yet synced can now be removed, here, in the agent server and with `gnotes unlink`.

**Command line.** `--` did not stop flag parsing. `edit -m ""` opened the editor instead of clearing the body. `init` saved the identity before refusing; it now checks first, succeeds on a fresh clone that only lacks an identity, and refuses to create a project beneath another in the same repository. The editor runs through `sh -c`, as git runs it, so a quoted path containing spaces works, and closing it unchanged writes nothing.

**Interactive interface.** Layout counted runes, so wide characters overflowed rows. The notebook column and the key reference now scroll, and `e` opens `$EDITOR` on the whole body instead of editing its first line.

**Smaller fixes.**

- The agent server answers a malformed JSON-RPC frame with -32600, ignores response frames, runs no request sent without an id, and always replies with a result or an error. `gnotes_update` reports only the fields that changed, and a missing `ref` is named.
- git commands ignore an inherited `GIT_DIR` or `GIT_WORK_TREE`, which a hook sets. Logs relocated outside the repository fail the sync instead of committing nothing.
- The command line prints a flag's error before the usage line, answers `-h` after any command, and suggests `tag` for `tga`. It refuses standard input that is not UTF-8 or exceeds 16 MiB, and validates `mv` placements. `ls` shows assignees. `--json` always emits `tags`, `links` and `assignees` as arrays, and adds `notebookId`. Assigning someone twice writes one event, and a second notebook with an existing name is refused.
- The interactive interface completes aliases without extending a complete command, draws the cursor anywhere in the line, keeps an answer visible after a long prompt, and counts tasks in progress as open in the header.
- Search snippets no longer split a character, and the identity file is written atomically.

### Security

The browser view is protected by a per-run access token carried in the address gnotes prints, not by its loopback binding. Any page open in your browser can make requests to `127.0.0.1`, so a bound port alone would let one of them read and rewrite your notes. The page reads the token from its own URL and sends it in a header, which a script on another origin cannot do, and cross-origin requests are rejected outright.

No browser is opened where there is evidently no desktop to open it on: over SSH, under a continuous integration runner, or on a Unix session with no display server. The address is printed either way.

The browser view refuses requests whose Host is not a loopback name, which a DNS-rebinding page would send. It accepts the token in the query string only on its event stream, and sends a Content-Security-Policy, `X-Content-Type-Options: nosniff` and `Referrer-Policy: no-referrer`. `serve` warns when listening beyond loopback, refuses a chosen token shorter than 16 characters, and reads one from `$GNOTES_TOKEN` to keep it out of shell history.

Text from the event log is untrusted: it arrives from other people through git. The command line and the terminal interface printed it raw, so an escape sequence in a title could clear the screen, retitle the window or write the clipboard, and a newline could forge table rows. Both now replace control characters on output. Titles, notebook names and tags containing them are refused on input. Bodies keep newlines and tabs.

### Build

`make build-slim` (`-tags noweb`) leaves out the browser view and the HTTP server it needs, taking the binary from about 7.3 MB to about 4.1 MB. The command line and the interactive interface are unaffected, and `serve` still exists in such a build to explain that it was left out.

### Format

The event log is at schema version 1: one JSON object per line, in `.gnotes/events/<author-id>.<author-name>.jsonl`, appended and never rewritten.

Three properties are worth knowing before the format settles:

- **Unknown actions are skipped, not fatal.** A log written by a newer gnotes still opens in an older one, which steps over what it does not understand and says so. The lines stay on disk, so upgrading applies them.

- **The version field is the compatibility gate.** It will only be raised for a change an older build cannot read. Additive changes take the unknown-action path instead.

- **An interrupted write costs one command, not the project.** A process killed mid-append leaves a last line with no closing newline. If it is a whole record it is kept and the next append terminates it; if it is half a record it is reported, dropped, and removed before the next append lands behind it. A record malformed anywhere but the end is still fatal, because that is corruption rather than a write that was cut short.

A command that returns has reached the page cache, not the disk. There is no fsync per append: what protects the log across a lost machine is `gnotes sync`, not a disk barrier.
