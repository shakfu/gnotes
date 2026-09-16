---
title: Own markdown renderer
tags: [decision, tui]
---
# Own markdown renderer

## Decision

`internal/render` draws markdown from goldmark's tree.

## Alternative

glamour, which renders markdown for terminals.

## Why

glamour added 7.86 MB to the binary. The renderer also records where each link and heading lands, which the interface needs to follow links.

See [[Drawing#Markdown]].
