# Changelog

Notable changes to gwiki, newest first.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). From the first tagged release onwards the project follows [semantic versioning](https://semver.org/spec/v2.0.0.html), where a breaking change means one that stops an existing database from opening or reading correctly.

## [Unreleased]

## [0.2.0]

### Added

**`gwiki check` reports line anchors whose lines moved or changed.** Each line link is compared with the commit that last added it to its page. `line-moved` carries the new anchor as a repair, which `--fix` offers; `line-changed` has none. Both are warnings unless `--strict` is given. The MCP `gwiki_check` lists them and `gwiki_fix_link` applies the repair. `gwiki lsp` shows them as information diagnostics with a quick fix, rechecked when a commit or a linked file changes. Links not yet committed, and every link in a shallow clone, are skipped. The baseline is the link's commit, not the page's, because a later edit to the page would otherwise hide the drift; see `docs/dev/anchor-drift.md`.

**A `line-out-of-range` link whose lines are still in the file is offered their new range** instead of dropping the anchor, in `check --fix`, MCP and the language server.

### Changed

**`gwiki check --json` also lists drifted line anchors**, in the same array as broken links, with `status` `line-moved` or `line-changed`, `since` and `offer`. A script treating every entry as broken must filter on `status`.

### Removed

**The notes database and its commands.** `gwiki notes`, global notes (`gwiki notes -g`), `gwiki migrate`, the notes browser view and the notes MCP server are gone, with `.gwiki/notes.db` support, the identity file and `GWIKI_HOME`. The wiki replaced the notes model in 0.1.0, and keeping both meant two `ls`, `edit` and `done` commands, two browser views and two agent servers. About 9,200 lines of non-test Go went with it. To convert a notes database, run `gwiki migrate` from 0.1.1 first; this release ignores `notes.db`.

## [0.1.1]

### Fixed

**The page scrolls as the cursor moves down past wrapped lines.** The buffer counted buffer lines against the pane's height, but a wrapped line takes several rows. On a page of long paragraphs the cursor went below the pane, and the page scrolled only once the cursor was a pane's height of lines below the top. `H`, `M`, `L` and `ctrl-y` used the same count. The `:preview` view ignored the arrow keys, and `j` and `k` there moved a hidden cursor over the source. Arrows, `j`, `k`, `[`, `]`, page keys, `g` and `G` now scroll its rendered rows.

## [0.1.0]

The first release.

### Changed

**Renamed to gwiki; the wiki is the product.** The binary is `gwiki`, the module `github.com/shakfu/gwiki`, the project directory `.gwiki/`, and the environment variables `GWIKI_HOME` and `GWIKI_TOKEN`. The wiki commands moved to the top level, and a bare `gwiki` opens the wiki interface. The notes database stays, as `.gwiki/notes.db`, with its commands under `gwiki notes`: they share names such as `ls`, `edit` and `done` with the wiki's, so both could not be top-level. A project with `.gnotes/` must rename the directory, and a link written as `/.gnotes/wiki/...` must be updated; `gwiki` names the old directory when it finds one. The identity file moved with the configuration directory, from `gnotes/` to `gwiki/`.

**The terminal interface draws in xterm 256 colours.** The palette matches the browser view's, with a light and a dark value picked from the terminal's background. Fixed colours over the terminal's 16 theme colours, so the interface looks the same under every theme.

**The wiki interface has header and status bars.** The header names the project, the page's title and path, and the page's broken links, links and backlinks, which the link panel listed before. The status bar names the screen or the editor's mode, then hints or the last message.

**The wiki interface's lists and overview are aligned tables.** Tasks, broken links, page lists, quick open, repairs and each overview section draw their fields in columns, and cut the path before the title when the terminal is narrow. The status bar shows the position in a list. Search snippets drop markdown syntax and highlight matches. On the overview, the most-linked pages go under whichever column is shorter.

**Backlinks fold away until `tab` reaches them.** The panel under the page took up to a third of a short terminal on every page something links to. It now opens only while it has the focus, and the page keeps its full height otherwise; the header bar still counts the backlinks. Hidden rather than a one-line stub, since that count already says they exist.

**The tree folds with left and right.** Right (`l`) moved focus to the page; it now unfolds a directory, steps into an unfolded one, or opens a page, and left (`h`) folds a directory or moves to its parent, as file trees in editors do. `tab` still moves to the page. Folds are drawn with `▾` and `▸` instead of `v` and `>`.

**The overview is three tabs.** It drew recent changes, tasks, health, directories, tags and the most-linked pages on one screen, capped at 6 to 10 rows each. Its header is now a tab bar, `latest`, `tasks` and `stats`, moved through with `tab` and `shift-tab`; each tab has the whole screen, so the lists are no longer capped. The tasks screen is the tasks tab, and `O` returns to the tab last shown. The status bar names the tab.

**A page is read and edited in one vim buffer.** Opening a page shows its markdown source beside the tree, in the editor that was a separate screen, with the pages linking to it underneath. `tab` moves between the three. In normal mode `<` and `>` jump between links, `enter` follows one, `ctrl-o` goes back and `[` `]` move half a screen; the status bar names the link under the cursor. Wiki actions that were single keys in the reader are `:` commands (`:new`, `:mv`, `:broken`, `:tasks`, `:fix`, `:search` and more), since vim uses those keys; the tree, overview and lists keep their letters and gain a `:` line. `:q` quits gwiki. A source buffer over a rendered reader, so reading and editing need no switch; `:preview` draws the page. `<` and `>` no longer indent in normal mode; `V` then `>`, or `ctrl-t` in insert mode, still does.

**The backlinks panel shows only when it has something.** It took 6 rows on every page and listed each link separately. It now appears only for a page something links to, up to a third of the screen, with one row per linking page. Headings in `:preview` drop their `#` markers above level 3, and bullets, checkboxes, quotes, tables and rules draw with box-drawing characters, so a quote bar no longer looks like a table column. The preview shows the page's type, status, priority, due date and tags above it.

**The notes terminal interface is removed.** `gwiki notes ui` and a bare `gwiki notes`, which now lists its commands, no longer open it. The notes remain available through the command line, the browser view and the agent server.

### Added

**A directory's README is its page.** `lexer/README.md` is the page for `lexer/`: `[[lexer]]` and `[lexer](lexer/)` reach it, a README without a title takes the directory's name, `gwiki show lexer` finds it, and the tree draws it on the directory's row, where `enter` opens it and `space` folds. Moving it rewrites `[[lexer]]` to the new directory and keeps directory links as directories. README over `index.md` because GitHub shows a directory's README when it is browsed. A link to a directory of pages without a README is now a missing page rather than a working file link; a directory of other files is unchanged. The cache schema is version 4, so existing caches are rebuilt.

**An example wiki.** `.gwiki/wiki` in this repository documents gwiki itself, in sections for guides, architecture, decisions and tasks.

**Wiki overview.** The wiki interface opens on an overview: recent changes with the last commit's author and date and the pages git has not recorded, open tasks with overdue and due-soon counts, broken links, orphan pages and dead ends, and directories, tags and the most-linked pages. Each row opens its page or list; `O` returns to it.

**Notes and tasks.** Notebooks holding notes and tasks as peers. A note has a title, a markdown body and tags; a task has those plus a status (open/doing/done), a priority, a due date and assignees. They are distinct kinds, so a task-only operation aimed at a note is refused rather than quietly giving the note a status. Entries can reference each other, and both directions of a reference are shown.

**Storage.** A project is one SQLite database, `.gwiki/notes.db`, committed with the repository. Notes and tasks are rows with typed columns in `nodes`, `tags`, `links`, `assignees` and `contributors`, so any SQLite client can query and edit them, and gwiki loads such an edit like its own. Typed columns over JSON rows, because a JSON column is neither queryable by field nor safely editable by hand. gwiki is a single-user tool: git cannot merge two copies of the file. A command's changes commit in one transaction, and the database uses a rollback journal rather than WAL, so the committed file holds every write. `.gwiki/.gitattributes` names a `gwiki` diff driver, which renders the database as a `sqlite3` dump once configured; see the README. `modernc.org/sqlite` over `mattn/go-sqlite3` keeps the build free of cgo, at 4 MB of binary.

**History and time travel.** Triggers record every change in a `changes` table, so an edit from another client is recorded too, with no author. `log` and the browser's history pane read it, and `gwiki notes ls --at 2026-08-01` or `--at 3d` replays it to list the project as it stood then. Writes by other processes are noticed by comparing the highest `changes.seq`; `PRAGMA data_version` is per connection, and `database/sql` pools connections.

**Search.** An FTS5 table kept current by triggers, ranked by BM25 with titles weighted above tags and tags above bodies. Typed words are quoted, so FTS5 syntax in a query is searched as text.

**Command line.** 27 commands in all, covering creation, editing, task fields, tags, links, moving, deletion and restore, alongside history and the two other views. Entries are addressed by a six-character handle, by title, or by a fragment of one; an ambiguous name lists the candidates instead of guessing. `--json` on `ls`, `show` and `search` for scripting.

**Interactive interface.** A two-pane terminal browser with vim movement, a `:` command line with history and tab completion, and `/` search that narrows as you type.

**Browser view.** `gwiki notes serve` opens a three-pane page in your browser. The whole page is compiled into the binary, so there is nothing to install and it works with no network. It refreshes by itself when any other front end writes. The detail pane ends with the entry's recorded changes.

**Agent access.** `gwiki notes mcp` serves the project over the Model Context Protocol, so an agent can read and write notes and tasks through seven tools. It is a front end like the others rather than a separate path: an agent is subject to the same rules a person is, so it cannot put a status on a note or delete something irrecoverably.

**Global notes.** `gwiki notes -g init [dir]` creates a personal project in `dir` or `~/notes`, or adopts one already there, and records its location. A leading `-g` sends any command to it, including `ui`, `serve` and `mcp`. Outside a project there is no fallback to it: a command without `-g` still refuses, so a note run in the wrong directory cannot land there. The location is kept in `global.json`, not `user.json`, because `whoami --set` rewrites `user.json` with only the identity fields.

**Wiki.** `gwiki` works on markdown pages under `.gwiki/wiki`, the storage model that replaces the database (see `docs/dev/wiki-design.md`). Pages link with `[[wiki]]` links and markdown links; `gwiki check` reports links to missing pages, headings, files and line ranges, and exits with status 1 when any exist. `ls`, `show`, `search`, `links`, `backlinks`, `orphans` and `tasks` read a gitignored cache, `.gwiki/cache.db`, which each command brings up to date from the pages and rebuilds when it is missing, stale or corrupt.

`new`, `edit`, `mv`, `rm`, `tag`, `untag`, `done`, `doing`, `reopen` and `promote` write pages, and `check --fix` offers repairs for each broken link. `mv` rewrites links to and from the page in their own form, and `--dry-run` lists them. A write checks the content hash of every page it changes before writing any; if one changed since it was read, nothing is written. The hash check replaces a lock because editors outside gwiki take no lock.

`gwiki mcp` serves the wiki to code agents over MCP. An agent reads a page with its hash, then replaces exact text or the whole page against that hash. If the developer saved the page in between, the agent's write is refused and returns the current source. The agent can also create and rename pages, repair broken links, and change task status. There is no delete tool: gwiki has no undo, so deletion is left to the developer.

`gwiki ui` is a terminal interface to the wiki: a page tree, a reader that follows links and goes back, a panel of links and backlinks, search as you type, quick open, broken links with repairs, and tasks. Pages are drawn by a markdown renderer of gwiki' own rather than glamour, which added 7.86 MB to the binary. Pages changed outside the interface reload within a second.

`gwiki lsp` is a language server in the same executable, so an editor with an LSP client, such as Neovim or Helix, works on the wiki: link completion, warnings on broken links as you type, following links, backlinks, renaming a page with its links rewritten, and quick fixes. It was built before a gwiki editor because it keeps a vim user in their own vim, with their configuration. Configuration for Neovim, Helix and Vim is in the README.

**A vim-style editor.** `e` in the wiki interface opens the page in gwiki's own modal editor: counts, operators with motions and text objects, registers, `.`, undo and redo, search, and `:w`, `:q` and `:s`. `il` and `al` select the link at the cursor, `ctrl-space` ticks a checklist item, `enter` continues markdown lists, and `ctrl-n` completes pages, headings and paths. A write is checked against the hash the buffer was read from, and the buffer autosaves to `.gwiki/drafts/`, so an interrupted edit is offered back. Macros, marks, blockwise visual and `:g` are not implemented; the key reference says so. `E` still hands the page to `$EDITOR`.

**Browser view of the wiki.** `gwiki serve` opens the overview, the page tree, pages with their links and backlinks, search, broken links, tasks, and an editor, and follows a link into the code to the file at its lines. Pages are rendered to HTML through goldmark with wiki links resolved; raw HTML in a page is escaped rather than passed through. Editing saves against the hash the page was read at, so a page saved elsewhere is refused, not overwritten. The page is one file of vanilla HTML, CSS and JavaScript with no build step, pushed live over server-sent events, and protected by the access token in the address as the notes view is. `gwiki notes serve` still opens the notes view.

**Migration into pages.** `gwiki migrate` copies the notes database into wiki pages: notebooks become directories, notes and tasks become pages, task fields and tags become front matter, and a reference becomes a `[[wiki]]` link by title. It deletes nothing, so `gwiki notes` keeps working, and a second run writes only what has no page yet. `--dry-run` lists the pages, and `--include-deleted` keeps deleted entries under an unindexed directory. A project of 5,020 entries converts in 3.9 seconds.

**Import of JSONL projects.** A project with `.gwiki/events/*.jsonl` logs, from builds before the database, is replayed into the tables on first open, each change dated by its event. Unknown actions and rejected events are counted, not imported. The logs are left in place.

### Fixed

**References named the wrong entry.** An unquoted multi-word reference used only its first word, so `gwiki notes done the lexer` marked "the plan" done. Commands taking one reference now read every word. Commands taking a reference and then values try every split, and refuse when more than one resolves. An id suffix shorter than the six-character handle is no longer matched; two title letters were often valid id characters, so `rm db` could delete an unrelated entry. An exact title beats longer titles starting with it. The workspace matches only when asked for. `restore` reports ambiguity instead of guessing, in the command line, the terminal interface and the agent server.

**Writes by another process went unseen.** The browser view and the agent server read the project's state after their own commit, so a write landing in between was marked seen and never loaded. The session now tracks what it loaded and absorbs only its own write. A failed load is retried, and the browser view refuses writes until it succeeds. The terminal interface now polls once a second and keeps the cursor on the same entry across a reload, filter or sort. The browser view also shared a node's tag slice with responses encoded after unlocking, a data race under concurrent tag edits.

**Restoring a notebook restored too much.** It also brought back entries deleted on their own before it.

**Due dates were compared as instants.** A date without a time read back as midnight UTC, so a task due today was overdue all day, and from the evening before west of Greenwich. It is now a calendar date in the reader's time zone, and a time typed without a zone is local. `ls --at` reads dates in local time, refuses a future cutoff and durations such as `1.5d`, and judges overdue as of the cutoff.

**Appending rebalanced a notebook every 95 entries.** An append took the midpoint to the end of the rank space, halving it each time; 1,000 notes caused 11 rebalances. Appends and prepends now step 2^64. A malformed rank triggers a rebalance instead of failing every insert beside it.

**The browser view lost edits.** The detail pane was rebuilt only when its entry left the list, so an added tag did not appear, and renaming an entry back to its previous title was silently not saved. It now refreshes on every change and keeps a field that is being typed in. A newer search could render an older one's results, Escape in the new-entry dialog could still create the entry, and the first change after the page loaded could be missed. A link to an entry that is deleted or missing can now be removed, here, in the agent server and with `gwiki unlink`.

**Command line.** `--` did not stop flag parsing. `edit -m ""` opened the editor instead of clearing the body. `init` saved the identity before refusing; it now checks first, succeeds on a fresh clone that only lacks an identity, and refuses to create a project beneath another in the same repository. The editor runs through `sh -c`, as git runs it, so a quoted path containing spaces works, and closing it unchanged writes nothing.

**A checklist item's due date showed twice.** `- [ ] benchmark due:2026-10-01` kept the token in its text, so `gwiki tasks`, the MCP task list and the browser view printed the date in the text and again beside it. The parser now takes the date out of the text; MCP item lines add `due:` themselves. The fix is in the parser rather than each front end, and the web and MCP servers' check that a line still holds the same item compares text that lacks the date on both sides. The cache schema is version 5.

**`"+y` copied nowhere.** The yanked text was kept in a field nothing read. It now reaches the system clipboard through OSC 52, which works over SSH.

**`:preview` drew broken links as working ones.** It rendered without the link index, so a missing page looked like any link.

**The tasks screen hid due dates' urgency.** It drew overdue tasks like any other, while the overview coloured them. A checklist item's inline `due:` also showed twice, in its text and after it. The tasks screen and the overview now share one row: the date once, marked `✗` when past.

**The editor's status line lost its fields.** Entering insert mode, or any message, replaced the mode, the page, the unsaved mark and the cursor position. The message now shows beside them.

**The wiki interface's help was cut off.** It did not scroll, so a 24-row terminal hid the keys for quitting and for the overview, and it omitted keys such as `a` in tasks. The status line dropped `? help` first when it ran out of room. The help, the hints and the key handlers now come from one table; the help scrolls, and `? help` stays at the right edge. Reloading the overview moved from `r`, which moves a page in the reader, to `R`, which works on every screen.

**The terminal interface showed no selection without colour.** Under `NO_COLOR`, lipgloss dropped reverse video along with colour, so the cursor in lists, the tree and the editor, and the selected link, were invisible. Bold, underline and reverse video now stay, and a selected row starts with a marker.

**Interactive interface.** Layout counted runes, so wide characters overflowed rows. The notebook column and the key reference now scroll, and `e` opens `$EDITOR` on the whole body instead of editing its first line.

**Smaller fixes.**

- The agent server answers a malformed JSON-RPC frame with -32600, ignores response frames, runs no request sent without an id, and always replies with a result or an error. `gwiki_update` reports only the fields that changed, and a missing `ref` is named.

- The command line prints a flag's error before the usage line, answers `-h` after any command, and suggests `tag` for `tga`. It refuses standard input that is not UTF-8 or exceeds 16 MiB, and validates `mv` placements. `ls` shows assignees. `--json` always emits `tags`, `links` and `assignees` as arrays, and adds `notebookId`. Assigning someone twice writes nothing the second time, and a second notebook with an existing name is refused.

- The interactive interface completes aliases without extending a complete command, draws the cursor anywhere in the line, keeps an answer visible after a long prompt, and counts tasks in progress as open in the header.

- Search snippets no longer split a character, and the identity file is written atomically.

### Security

The browser view is protected by a per-run access token carried in the address gwiki prints, not by its loopback binding. Any page open in your browser can make requests to `127.0.0.1`, so a bound port alone would let one of them read and rewrite your notes. The page reads the token from its own URL and sends it in a header, which a script on another origin cannot do, and cross-origin requests are rejected outright.

No browser is opened where there is evidently no desktop to open it on: over SSH, under a continuous integration runner, or on a Unix session with no display server. The address is printed either way.

The browser view refuses requests whose Host is not a loopback name, which a DNS-rebinding page would send. It accepts the token in the query string only on its event stream, and sends a Content-Security-Policy, `X-Content-Type-Options: nosniff` and `Referrer-Policy: no-referrer`. `serve` warns when listening beyond loopback, refuses a chosen token shorter than 16 characters, and reads one from `$GNOTES_TOKEN` to keep it out of shell history.

Stored text is untrusted: any SQLite client can write it, and a clone brings in whatever was committed. The command line and the terminal interface printed it raw, so an escape sequence in a title could clear the screen, retitle the window or write the clipboard, and a newline could forge table rows. Both now replace control characters on output. Titles, notebook names and tags containing them are refused on input. Bodies keep newlines and tabs.

### Build

`make build-slim` (`-tags noweb`) leaves out the browser view and the HTTP server it needs, taking the binary from about 12.8 MB to about 9.2 MB. The command line and the interactive interface are unaffected, and `serve` still exists in such a build to explain that it was left out.

### Format

`.gwiki/notes.db` is at schema version 1, stored in `PRAGMA user_version`. A database written by a newer gwiki is refused rather than misread. Writes use a rollback journal with `synchronous=FULL`, so a command that returns is on disk and the file is complete when you commit it.
