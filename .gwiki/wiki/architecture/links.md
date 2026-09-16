---
title: Links
tags: [architecture]
---

## Links

### Resolution

A `[[wiki]]` link is resolved by [the resolver](/internal/wiki/resolve.go#L256), trying in order:

1. a path, or a directory's path naming its README;
2. a title;
3. a file name, or a directory's name for a README.

Each rule is case-insensitive. The first rule that matches anything decides; two matches there make the link ambiguous.

A markdown link is a path relative to its page, or to the repository when it starts with `/`. A path to a directory of pages names its README ([isReadme](/internal/wiki/nested.go#L18)).

### Statuses

| status | means |
|---|---|
| `ok` | resolves |
| `missing-page` | no page matches |
| `ambiguous` | several pages match |
| `missing-heading` | the page has no such heading |
| `missing-file` | the file is not in the repository |
| `line-out-of-range` | the file is shorter than the range |
| `outside-repo` | the path leaves the repository |

### Repairs

[Offers](/internal/wiki/fix.go#L27) suggests replacements for a broken link: pages with a similar name, a heading that exists, the link without its line anchor, or the file found elsewhere. `gwiki check --fix` and `:fix` in the page apply one.

### Moves

[PlanMove](/internal/wiki/move.go#L63) rewrites each link to a moved page in its own form. A wiki link that still resolves is left alone; a link to a section's directory stays a directory link.

Back to [[Architecture]].
