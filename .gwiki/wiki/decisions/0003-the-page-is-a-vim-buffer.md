---
title: The page is a vim buffer
tags: [decision, tui]
---

## The page is a vim buffer

### Decision

A page opens as its markdown source in a vim buffer beside the tree. Reading and editing happen in one place; `:preview` draws the rendered page.

### Alternatives

- A rendered reader with a separate editor screen, as before.
- Source with markup concealed except on the cursor line, as vim's `conceallevel` does.

### Why

- No switch between reading and editing.
- The editor already existed in `internal/vim`; the rendered reader was the extra code.
- Concealment needs cursor columns mapped between hidden and raw text, so it is left for later.

### Keys this cost

Vim uses most single letters, so the reader's letter commands became `:` commands. `<` `>` jump between links, which costs NORMAL-mode indenting; `,` `.` were rejected because they repeat, and the arrows because they move the cursor.

How the keys are wired is in [[Keys and commands]]; the full record is [the modal reader design](/docs/dev/modal-reader.md).
