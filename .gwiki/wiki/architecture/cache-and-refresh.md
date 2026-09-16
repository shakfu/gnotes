---
title: Cache and refresh
tags: [architecture]
---
# Cache and refresh

## Pages are the truth

Pages are committed; `.gwiki/cache.db` is not. The cache holds titles, headings, links, tags and tasks, derived from the pages. The decision is [[Files are the truth]].

## Schema version

The cache's `PRAGMA user_version` is [schemaVersion](/internal/wiki/cache.go#L15). A cache with any other version, or one SQLite cannot read, is deleted and rebuilt. Version 4 re-indexed every wiki when sections were added, so READMEs got their directory's title.

## Refresh

[Refresh](/internal/wiki/refresh.go#L52) runs before every query. It compares each file's size and modification time with the cache, parses the pages that changed, and writes their rows. A page removed while another appears with the same content hash is recorded as a rename.

Links on unchanged pages are re-resolved only when they name a page that was added, changed or removed, by path, title or file name ([resolve](/internal/wiki/resolve.go#L324)). Adding `architecture/README.md` also re-resolves links that name `architecture`.

## Writes

[Write](/internal/wiki/ops.go#L167) takes the hash the page was read at, and refuses when the file changed since. Editors outside gwiki take no lock, so a hash check is the only guard that holds for them.

See also [[Links]] and [[Glossary#Cache]].
