---
title: Writing pages
tags: [guide]
---

## Writing pages

### Links

| written | reaches |
|---|---|
| `[[Design sketch]]` | a page by path, then title, then file name |
| `[[lexer]]` | a section: `lexer/README.md` |
| `[[lexer/grammar#Rules\|the rules]]` | a heading, shown as "the rules" |
| `[the grammar](grammar.md#rules)` | a page by path relative to this one |
| `[lexer](../lexer/)` | a section's README, by its directory |
| `[code](/internal/wiki/resolve.go#L256)` | a file, or a line range, from the repository root |

A link that matches more than one page is ambiguous, and `gwiki check` lists the candidates. [[Links]] describes the rules in full.

### Front matter

```yaml
---
title: Ship the parser
tags: [parser, release]
type: task
status: open
priority: high
due: 2026-10-01
---
```

`type: task` makes the page a task page; see [[Tasks]].

### Checklists

Any `- [ ] item` is a task too, listed by `gwiki tasks` and ticked with `ctrl-space` in the page. An item can carry a date: `- [ ] benchmark the lexer due:2026-10-15`.

### Sections

A directory of pages is a section. Its `README.md` is the section's own page:

- `[[architecture]]` and `[architecture](architecture/)` reach `architecture/README.md`;
- the tree in [[The terminal interface]] draws the README on the directory's row: `enter` opens it, `space` folds the directory;
- a README with no title is titled by its directory's name.

Sections nest to any depth; this wiki's [[TUI]] section sits inside [[Architecture]]. README over `index.md` because GitHub shows a directory's README when it is browsed; see [[README pages for sections]].
