---
title: Architecture
tags: [architecture]
---
# Architecture

gwiki is one Go binary. Every front end reads and writes through one package, so none can mean something different by an operation.

```
cmd/gwiki
  internal/cli ---------+
  internal/tui ---------+
  internal/lsp ---------+--> internal/wiki --> .gwiki/wiki (pages)
  internal/mcp ---------+         |
  internal/webwiki -----+         +--> .gwiki/cache.db (derived)
```

## The wiki

- [[Cache and refresh]]: pages are the truth; the cache is rebuilt from them.
- [[Links]]: how a link is resolved, checked, repaired and rewritten on a move.

## Front ends

- [[TUI]]: the terminal interface, a section of its own.
- [[Editor engine]]: `internal/vim`, the modal editor the page uses.
- [[Servers]]: the language server, the MCP server and the browser view.

## Shared packages

| package | does |
|---|---|
| [markdown](/internal/markdown/) | reads front matter, headings, links and checklist items with their positions |
| [render](/internal/render/render.go) | draws a page as styled terminal lines |
| [display](/internal/display/) | replaces control characters in untrusted text |
| [editor](/internal/editor/) | opens `$EDITOR` |

The notes database (`gwiki notes`) is older and separate: `internal/session`, `state`, `store`, `event`, `rank`, `search`, `ulid` and `web`.
