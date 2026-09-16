---
title: Keys and commands
tags: [architecture, tui]
---
# Keys and commands

## Keymaps

Each context has a keymap: a list of bindings with the keys, the help text, the hint and the action ([keysGlobal](/internal/tui/keys.go#L62)). Dispatch, the status bar's hints and the help all read the keymaps, so none can list a key the others lack. A test checks that every bound key is in the help.

## The page's buffer

The page is an `internal/vim` editor ([[Editor engine]]). [keyContent](/internal/tui/wiki_edit.go#L349) takes a few keys first in NORMAL mode, with no command half typed: `tab`, `ctrl-p`, `<` `>`, `[` `]` and `enter`. Every other key goes to the editor.

## Commands

[The command table](/internal/tui/commands.go#L26) serves both the buffer, through the editor's `Command` hook, and a `:` line on the tree, the overview and the lists. The help lists the same table.

Back to [[TUI]].
