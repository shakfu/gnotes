---
title: Glossary
tags: [meta]
---

## Glossary

Terms used across this wiki. Back to [[Home]].

### Page

A markdown file under `.gwiki/wiki`. Its path without `.md` names it, such as `architecture/links`.

### Section

A directory of pages. Its `README.md` is the section's own page; see [[Writing pages#Sections]].

### Title

The front matter `title`, else the first level-one heading, else the file name. A README without either takes its directory's name.

### Backlink

A link from another page to this one. The terminal interface lists them under the page when `tab` reaches them; see [[The terminal interface#The page]].

### Orphan

A page nothing links to. The overview counts them.

### Dead end

A page that links to no page. The overview counts them.

### Draft

The text of a page being edited, saved to `.gwiki/drafts/` once a second and offered again after an interruption.

### Cache

`.gwiki/cache.db`, derived from the pages and never committed. See [[Cache and refresh]].
