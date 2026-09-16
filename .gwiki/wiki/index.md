---
title: Home
tags: [meta]
---

## Home

This wiki documents gwiki, and is kept in gwiki's own repository as an example of how a wiki can be organized. Every page is a markdown file under `.gwiki/wiki`, committed with the code it describes.

### Sections

Each section is a directory with a README, which is the section's own page.

| section | holds |
|---|---|
| [[Guides]] | how to use gwiki, task by task |
| [[Architecture]] | how the code is built, with links into the source |
| [[Decisions]] | why it is built that way, one record per decision |
| [[Tasks]] | work to do, as task pages with a status and due date |

Pages that fit no section sit at the top: [[Glossary]] and [[2026 Objectives]].

### Start here

1. [[Getting started]] installs gwiki and makes a first page.
2. [[Writing pages]] covers links, front matter, tasks and sections.
3. [[The terminal interface]] lists the keys and commands.

### How this wiki is organized

- **One directory per kind of page.** Guides explain, architecture describes, decisions justify, tasks track. A reader looking for "why" goes to [[Decisions]], not to a guide.
- **A README per directory.** `[[Architecture]]` and a link to `architecture/` both reach `architecture/README.md`. See [[Writing pages#Sections]].
- **Nesting follows the code.** [[Architecture]] has a `tui/` section because the terminal interface is large enough to need several pages.
- **Tags cut across sections.** `#tui` marks every page about the terminal interface, wherever it sits.
- **Links into the code carry line ranges**, such as [the resolver](/internal/wiki/resolve.go#L256), so `gwiki check` reports them when the file shrinks.
