---
title: The terminal interface
tags: [guide, tui]
---
# The terminal interface

Run `gwiki`. It opens on the overview, three tabs in the header bar that `tab` and `shift-tab` move between:

- **latest**: pages by when they changed;
- **tasks**: task pages and checklist items, soonest due first;
- **stats**: broken links, orphans, dead ends, the most-linked pages, directories and tags.

## The tree

| key | |
|---|---|
| `j` `k` `g` `G` | move |
| `enter` | open a page, or a section's README |
| `l` `→` | unfold a directory, step into it, or open a page |
| `h` `←` | fold a directory, or go to its parent |
| `space` | fold a directory |
| `n` `c` `t` `O` | new page, broken links, tasks, overview |
| `:` | a command |

## The page

A page opens as markdown source in a vim buffer. `tab` moves between the tree, the page and its backlinks.

| key | |
|---|---|
| `<` `>` | previous and next link |
| `enter` | follow the link under the cursor |
| `ctrl-o` | back |
| `[` `]` | half a screen |
| `i` `v` `:` `/` | vim's insert, visual, command and search |

## Commands

`:w` writes and `:q` quits. The wiki's own commands are `:new`, `:mv`, `:search`, `:broken`, `:tasks`, `:overview`, `:backlinks`, `:fix`, `:check`, `:preview`, `:external`, `:reload` and `:help`.

Why the page is a buffer rather than a rendered view is in [[The page is a vim buffer]]. How the keys are wired is in [[Keys and commands]].
