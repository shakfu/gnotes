---
title: Files are the truth
tags: [decision]
---

## Files are the truth

### Decision

Pages are markdown files committed with the code. SQLite is a cache, local to each clone.

### Alternative

The notes database: rows in a committed SQLite file.

### Why

- git can merge text files and review them in a diff; it cannot merge two copies of a database.
- Pages stay readable in any editor and on GitHub.
- The cache can always be rebuilt, so it never needs a migration.

See [[Cache and refresh]] and [the design](/docs/dev/wiki-design.md#L49).
