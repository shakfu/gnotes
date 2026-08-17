# Changelog

Notable changes to gnotes, newest first.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
From the first tagged release onwards the project follows
[semantic versioning](https://semver.org/spec/v2.0.0.html), where a breaking
change means one that stops an existing event log from replaying correctly.

## [Unreleased]

Nothing has been tagged yet, so everything below is the initial body of work.

### Added

**Notes and tasks.** Notebooks holding notes and tasks as peers. A note has a
title, a markdown body and tags; a task has those plus a status
(open/doing/done), a priority, a due date and assignees. They are distinct
kinds, so a task-only operation aimed at a note is refused rather than quietly
giving the note a status. Entries can reference each other, and both directions
of a reference are shown.

**Command line.** 28 commands in all, covering creation, editing, task fields,
tags, links, moving, deletion and restore, alongside history, sync and the two
other views. Entries are addressed by a six-character
handle, by title, or by a fragment of one; an ambiguous name lists the
candidates instead of guessing. Ranked full-text search over titles, bodies and
tags. `--json` on `ls`, `show` and `search` for scripting.

**Interactive interface.** A two-pane terminal browser with vim movement, a `:`
command line with history and tab completion, and `/` search that narrows as
you type.

**Browser view.** `gnotes serve` opens a three-pane page in your browser. The
whole page is compiled into the binary, so there is nothing to install and it
works with no network. It refreshes by itself when any other front end writes,
or when a sync brings in someone else's work. The detail pane ends with the
events that produced the entry, which is the one thing this view can show that
an ordinary notes application cannot.

**Agent access.** `gnotes mcp` serves the project over the Model Context
Protocol, so an agent can read and write notes and tasks through eight tools.
It is a front end like the others rather than a separate path: an agent is
subject to the same rules a person is, so it cannot put a status on a note or
delete something irrecoverably.

**Time travel.** `gnotes ls --at 2026-08-01` or `--at 3d` replays the log up to
a past moment and lists the project as it stood then. Nothing is stored for
this; it is a prefix of the events already on disk.

**Git sync.** `gnotes sync` commits the event logs, staging only the `.gnotes`
paths so that whatever else you have staged or edited is untouched. `--push`
also pulls from and pushes to `origin`; it is opt-in because the logs live on
your working branch, and moving that branch is your decision.

### Security

The browser view is protected by a per-run access token carried in the address
gnotes prints, not by its loopback binding. Any page open in your browser can
make requests to `127.0.0.1`, so a bound port alone would let one of them read
and rewrite your notes. The page reads the token from its own URL and sends it
in a header, which a script on another origin cannot do, and cross-origin
requests are rejected outright.

No browser is opened where there is evidently no desktop to open it on: over
SSH, under a continuous integration runner, or on a Unix session with no
display server. The address is printed either way.

### Build

`make build-slim` (`-tags noweb`) leaves out the browser view and the HTTP
server it needs, taking the binary from about 7.3 MB to about 4.1 MB. The
command line and the interactive interface are unaffected, and `serve` still
exists in such a build to explain that it was left out.

### Format

The event log is at schema version 1: one JSON object per line, in
`.gnotes/events/<author-id>.<author-name>.jsonl`, appended and never rewritten.

Two properties are worth knowing before the format settles:

- **Unknown actions are skipped, not fatal.** A log written by a newer gnotes
  still opens in an older one, which steps over what it does not understand and
  says so. The lines stay on disk, so upgrading applies them.
- **The version field is the compatibility gate.** It will only be raised for a
  change an older build cannot read. Additive changes take the unknown-action
  path instead.
