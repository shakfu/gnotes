---
title: Due date shown twice in the CLI
type: task
status: done
priority: medium
due: 2026-09-30
tags: [task]
---

## Due date shown twice in the CLI

A checklist item `- [ ] benchmark due:2026-10-01` kept `due:2026-10-01` in its text, so `gwiki tasks`, the MCP task list and the browser view printed the date twice.

Done: the parser now takes the date out of the item's text, so every front end shows it once, from the item's due date. The web and MCP servers compare the text a client sends with the indexed text; both now lack the date, so the comparison holds. MCP item lines print `due:` themselves. The cache schema went to version 5. See [[Servers]].
