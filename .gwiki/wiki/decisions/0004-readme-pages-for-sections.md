---
title: README pages for sections
tags: [decision]
---
# README pages for sections

## Decision

A directory's `README.md` is the directory's own page. `[[dir]]` and a link to `dir/` reach it, and the tree draws it on the directory's row.

## Alternatives

- `dir/index.md`, matching the wiki's root `index` page.
- `dir.md` beside `dir/`, as Obsidian's folder notes do.

## Why

GitHub shows a directory's README when the directory is browsed, so a section reads the same on GitHub as in gwiki. `dir.md` beside `dir/` can drift apart when one is moved without the other.

## Consequences

- Every README has the file name `readme`, so each is found by its directory's name instead.
- The root keeps `index` as the home page.
- The cache schema went to version 4; see [[Cache and refresh#Schema version]].
