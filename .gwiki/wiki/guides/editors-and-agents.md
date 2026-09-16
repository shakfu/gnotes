---
title: Editors and agents
tags: [guide]
---

## Editors and agents

gwiki serves the same wiki to other programs. Each is a front end over the `wiki` package; see [[Servers]].

### Editors

`gwiki lsp` is a language server: link completion, broken-link diagnostics, go to definition, references for backlinks, hover, outlines, rename with links rewritten, and quick fixes.

### Agents

`gwiki mcp` speaks the Model Context Protocol. Register it with `claude mcp add gwiki -- gwiki mcp`. Every write checks the page's hash, so an agent cannot overwrite an edit it has not read.

### Browser

`gwiki serve` opens the wiki in a browser, with an access token in the address.
