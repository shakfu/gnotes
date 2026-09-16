---
title: Fewer escape sequences for links
type: task
status: open
priority: low
tags: [task, tui]
---

## Fewer escape sequences for links

lipgloss draws underlined text one character per escape sequence, so a link of 20 characters costs 20 sequences in every frame. Drawing a link as one underlined run would cut that. See [[Drawing]].
