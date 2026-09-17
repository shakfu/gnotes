# Line anchor drift

Status: implemented, 2026-09-17. Extends [wiki-design.md](wiki-design.md)
section 5, which checked line anchors for range only.

## Problem

A link such as `[keyContent](/internal/tui/wiki_edit.go#L349)` names lines, not
code. Insert three lines above it and the link points at a comment, while
`gwiki check` passes. In this repository, commit `92e1824` did exactly that to
`architecture/tui/keys-and-commands.md`.

## Rule

For each line link:

1. **Baseline:** the newest commit that changed how many times the page
   contains the link's destination text, such as `/internal/tui/wiki_edit.go#L349`.
   This is `git log -S` semantics.
2. **Compare** the anchored lines in the file at the baseline with the same
   range in the working tree.
3. **Equal:** no report.
4. **The old lines occur exactly once elsewhere:** `line-moved`, with the
   rewritten anchor as a repair.
5. **Otherwise:** `line-changed`, with no repair.

Skipped:

- a link not in the page at HEAD, since it was written against the working
  tree, which git does not record;
- every link whose text the page holds more times than HEAD does. A fix can
  rewrite `#L2` to `#L3` beside a committed `#L3`; the two cannot be told
  apart, and comparing both offered a wrong move to `#L4`. The committed one
  goes unchecked until the fix is committed;
- a link whose file is missing or whose range is past its end, which `Check`
  reports already;
- every link outside a repository or in a shallow clone (`ErrNoHistory`).

A `line-out-of-range` link is not drift, but when its old lines occur exactly
once in the shorter file, the moved anchor replaces "drop the line anchor" as
its only repair, so `--fix=first` applies it. `Wiki.Offers` runs git for this;
the language server takes the anchor from its cached `DriftAll` result, so
`Snapshot.Offers` stays free of git.

`gwiki check` lists drift after broken links. It is a warning; `--strict` fails
on it. `--fix` offers `line-moved` repairs after broken links. The MCP tools
`gwiki_check` and `gwiki_fix_link` do the same for agents.

The language server publishes drift as information diagnostics, with a quick
fix for `line-moved`. Drift runs git, so the server does not run it per
keystroke. It reruns it when a page changes, or when `DriftStamp` changes: the
mtime and size of `.git/HEAD`, `.git/logs/HEAD`, `.git/index` and every file a
line link names, checked on each one-second poll. A buffer's links match the
result by page and written destination, so editing an anchor clears its
diagnostic at once.

## Baseline choices

| baseline | failure |
|---|---|
| the page's last commit | a commit that fixes a typo in the page hides drift in all its links |
| HEAD for an uncommitted page | `--fix` rewrites `#L349` to `#L352`; the page is now dirty, its other links compare HEAD with HEAD, and their drift disappears |
| HEAD for an uncommitted link | with uncommitted code edits, a fixed `#L3` compares with stale HEAD lines and is "moved" again, to `#L4` |
| the link's own commit, uncommitted links skipped | chosen |
| a hash of the lines stored in the page | exact and needs no history, but pages carry machine state that people must update |

The third row is not hypothetical: `TestWikiCheckDrift` failed that way before
uncommitted links were skipped.

Whole-file comparison ("the file has commits newer than the page") flagged 4 of
this repository's 17 line links, of which 1 had moved. Line comparison flagged
the 1.

## Cost

One `git log -M -p -U0` over the pages directory, streamed and stopped once
every link has a baseline, and one `git cat-file --batch` for the distinct
baseline files and the pages at HEAD.

1,000 pages, 4,000 line links into 50 files, 5 commits, on an Apple laptop:

| | `gwiki check` |
|---|---|
| one `git log --follow -p` per page | 16.9 s |
| one log for the directory | 1.0 s |
| and each distinct object read once | 0.32 s |
| without history (not a repository) | 0.03 s |

## Not done

- The terminal interface and the browser view do not show drift.
- The language server runs drift in its request loop, so a rerun on a large
  wiki (0.3 s at 1,000 pages) delays requests that arrive meanwhile.
- Comparison is byte-exact, so reindenting a line is `line-changed`.
- A page renamed but not committed has no page at HEAD, so its links are
  skipped until the rename is committed.
