---
title: TUI
tags: [architecture, tui]
---
# TUI

`internal/tui` is the terminal interface, built on Bubble Tea and lipgloss. It is a section of [[Architecture]] because it has several parts:

- [[Keys and commands]]: keymaps, the `:` command table, and how the page's buffer takes keys.
- [[Drawing]]: the theme, the bars, aligned tables and the markdown renderer.

## Screens

| screen | shows |
|---|---|
| overview | three tabs: latest changes, tasks, and stats (health and structure) |
| read | the tree, the page's buffer, its backlinks |
| lists | search, quick open, broken links, tasks, repairs, page lists |
| help | every key and command |

## Files

| file | holds |
|---|---|
| `wiki.go` | the model, opening pages, following links |
| `wiki_edit.go` | the page's buffer: hooks, drafts, completion |
| `keys.go` | keymaps, dispatch, hints and help |
| `commands.go` | the `:` commands |
| `theme.go`, `table.go` | styles, bars and aligned rows |
| `wiki_home.go`, `wiki_lists.go`, `wiki_view.go` | the screens |

The review that led to this layout is `REVIEW.md` in the repository root; the modal page is [[The page is a vim buffer]].
