---
title: Servers
tags: [architecture]
---
# Servers

Three programs serve the wiki to others. Each is a front end over `internal/wiki`, like the command line.

| command | package | speaks |
|---|---|---|
| `gwiki lsp` | [lsp](/internal/lsp/server.go) | Language Server Protocol, on standard input and output |
| `gwiki mcp` | [mcp](/internal/mcp/wiki.go) | Model Context Protocol, on standard input and output |
| `gwiki serve` | [webwiki](/internal/webwiki/server.go) | HTTP on loopback, with an access token |

## MCP tools

`gwiki_list`, `gwiki_search`, `gwiki_read`, `gwiki_check`, `gwiki_tasks`, `gwiki_create`, `gwiki_edit`, `gwiki_write`, `gwiki_rename`, `gwiki_fix_link` and `gwiki_set_task`.

## The browser view

The page is compiled into the binary. The token, not the loopback binding, is the protection: any page open in a browser can reach `127.0.0.1`.

How to use them is in [[Editors and agents]].
