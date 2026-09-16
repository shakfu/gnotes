---
title: Drawing
tags: [architecture, tui]
---

## Drawing

### Theme

[The palette](/internal/tui/theme.go#L17) uses xterm 256 colours, each with a light and a dark value, matching the browser view. Under `NO_COLOR` the styles keep bold, underline and reverse video, so the selection, the cursor and links still show. A selected row starts with a marker for the same reason.

### Bars

The header and status line are filled bars. Lines being typed into stay plain: the input cursor's reverse video would end a reverse-video bar.

### Tables

Lists and the overview draw rows in aligned columns. Each column has a shrink order, so a narrow terminal cuts a path before a title.

### Markdown

[Render](/internal/render/render.go#L76) walks goldmark's tree for `:preview`. It is gwiki's own because glamour added 7.86 MB to the binary; see [[Own markdown renderer]].

Back to [[TUI]].
