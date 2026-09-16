---
title: Highlight quick open matches
type: task
status: open
priority: low
due: 2026-10-15
tags: [task, tui]
---

## Highlight quick open matches

`ctrl-p` ranks pages by title prefix, title fragment, path fragment and letters in order, but does not show which letters matched. Search highlights its matches; quick open should too.

- [ ] return the matched positions from `findPages`
- [ ] draw them with the match style in [[Drawing#Tables]]
