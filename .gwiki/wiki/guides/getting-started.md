---
title: Getting started
tags: [guide]
---
# Getting started

## Install

```sh
go install github.com/shakfu/gwiki/cmd/gwiki@latest
```

## Create a wiki

```sh
cd your-project
gwiki init
```

`init` creates `.gwiki/wiki` for the pages, `.gwiki/config.json` with the project name, and a `.gwiki/.gitignore` that keeps the cache and drafts out of git.

## Write a first page

```sh
gwiki new "Design sketch" --in lexer -m "The lexer tokenizes input. See [[Grammar]]."
gwiki check
```

`check` reports `[[Grammar]]` as a missing page and exits with status 1. Create it, and the link resolves:

```sh
gwiki new Grammar --in lexer
```

## Checklist

- [x] install gwiki
- [x] run `gwiki init`
- [ ] write a README for each section, as [[Writing pages#Sections]] explains
- [ ] open the interface with `gwiki`, then read [[The terminal interface]]
