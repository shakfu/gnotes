---
title: Editor engine
tags: [architecture, tui]
---

## Editor engine

`internal/vim` is a modal editor with vim's keys over a buffer of lines. It draws nothing and touches no files. The host feeds it keys and reads the buffer, the cursor and the mode.

### Hooks

The host supplies hooks for what the editor cannot do alone: `Save`, `Quit`, `Reload`, `Follow`, `Back`, `External`, `Clipboard`, `Complete`, `Command` and `Changed`. The terminal interface wires all but `External` in `openBuffer`; see [[Keys and commands]].

### What it has

Modes, counts, operators with motions and text objects, `.`, undo and redo, registers, visual mode, `/` search and `:s`. `il` and `al` select a link.

### What it lacks

- macros and marks
- blockwise visual
- clearing the modified flag when undo returns to the saved text; the interface does that itself

The keys it accepts are in [the design](/docs/dev/wiki-design.md#L367). Back to [[Architecture]].
