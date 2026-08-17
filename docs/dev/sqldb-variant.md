# A single-file database variant of gnotes

Sketch and design space, not a plan. Nothing here is committed to.

## 1. The question behind the question

"gnotes, but on SQLite or DuckDB" is under-specified in one important way, and
the missing piece decides everything else: **what happens to sync?**

The current architecture is not really "a notes tool that happens to store
JSONL". It is a set of five decisions that exist solely so that `git merge` can
never produce a conflict in the notes:

| decision | exists because |
|---|---|
| append-only event log | a rewrite would conflict; an append cannot |
| one file per author | two authors touch disjoint paths, so a merge is a union |
| ULID causal ordering by `ref` | replay order must not depend on clock skew or file arrival order |
| fractional 96-bit ranks | reordering must be a local edit, not a renumbering of siblings |
| search index rebuilt at startup | a persisted index would be a sixth thing to merge |

Put a binary file at the centre and the merge property evaporates. Git stores a
whole new copy per sync and cannot merge two of them; one side's writes are
silently lost. That was the finding when SQLite-as-truth was evaluated on
2026-08-17, and it has not changed.

So a genuine variant has to pick a different answer for sync, and there are only
three honest ones:

- **A.** Give it up. Single user, single machine. The DB is the truth, the tool
  gets much smaller, and you are building a different (perfectly reasonable)
  product.
- **B.** Keep the event log as the unit of exchange, but make its *transport*
  something other than "the log file is the storage format". Events are
  immutable and self-identifying, so sync is set union — the easiest
  distributed problem there is. Git can still be the transport if the DB writes
  a text mirror.
- **C.** Delegate to a replication layer (Litestream, rqlite, Turso, cr-sqlite).
  This buys sync at the cost of a server or a large extension, which contradicts
  "self-contained single executable".

**Alternative framing worth considering.** The interesting axis may not be
storage at all. It is whether the tool is *collaboration-first* (today's gnotes:
offline, multi-author, git-native, history is the point) or *query-first* (a
local knowledge base you interrogate: full-text with real ranking, temporal
questions, aggregates, joins against other project data). SQLite is a good
answer to the second question and a bad answer to the first. If the actual
appetite is query-first, the variant is worth building and should not pretend to
be a drop-in.

## 2. SQLite or DuckDB

**SQLite, decisively.** Measured locally, not recalled:

DuckDB 1.5.5 takes an exclusive lock on the database file. A second process is
refused even in read-only mode while a read-write connection is open:

```
$ duckdb d.duckdb            # held open in another terminal
$ duckdb -readonly d.duckdb -c "SELECT count(*) FROM a;"
IO Error: Could not set lock on file "d.duckdb":
Conflicting lock is held in .../duckdb (PID 42306) by user sa.
```

gnotes runs `gnotes serve` as a long-lived process while you use the CLI, the
TUI and an MCP server against the same project. DuckDB makes that arrangement
impossible without a broker process, which is a larger change than the storage
swap itself. SQLite in WAL mode gives multi-process readers plus one writer,
which is exactly the shape needed.

Secondary reasons: DuckDB's static library is tens of megabytes against
SQLite's ~1 MB amalgamation; DuckDB's file format has a shorter stability track
record than SQLite's (which carries a stated commitment to 2050); DuckDB's FTS
is an extension rather than a compiled-in feature; and the workload is small-row
OLTP, which is the case a column store is worst at.

**But DuckDB is still useful — as a reader, not a store.** It attaches SQLite
files directly, so all the analytical upside is available without adopting it as
the format. Verified:

```
$ duckdb -c "ATTACH 'notes.db' AS s (TYPE sqlite); SELECT count(*) FROM s.a;"
4
```

That is the right split: SQLite owns writes; DuckDB, `datasette`, or plain
`sqlite3` are ad-hoc readers. It also means the "I want SQL over my notes" wish
is satisfiable by exporting to SQLite from today's gnotes without any variant at
all.

## 3. Three variants, in increasing radicalism

### V0 — SQLite as a derived cache (not really a variant)

JSONL stays the truth. `.gnotes/cache.db` is gitignored, holds the materialized
tree plus an FTS5 index, and is keyed by the log fingerprint. Startup replays
only the appended tail.

- Buys: FTS5 search with BM25 ranking, SQL queries, sub-millisecond startup at
  any log size, no full replay.
- Costs: a dependency, a second code path, invalidation logic.
- Loses: nothing. Every current property is untouched.

This is the snapshot-cache idea with a query engine bolted on. It is the
cheapest route to ~80% of the benefit and should be the baseline that the real
variants are measured against. If a variant cannot beat V0 on something
specific, it is not worth building.

### V1 — Event log *in* SQLite, with a text mirror for git (recommended variant)

The DB is the working store; the JSONL files become an export/import format
produced at `sync` time.

```
write path:  session -> INSERT INTO events -> fold into nodes (same txn)
sync out:    SELECT events WHERE user_id = me AND id > last_exported
             -> append to .gnotes/events/<id>.<name>.jsonl -> git commit
sync in:     git pull -> read appended lines -> INSERT OR IGNORE INTO events
             -> reorder -> fold the new suffix into nodes
```

The important property: because events are immutable and keyed by ULID,
`INSERT OR IGNORE` *is* the merge. There is no conflict resolution code, no
three-way diff, no CRDT library. The set union is the merge, and the canonical
`ref`-tree ordering turns the set back into a sequence. Git is reduced to a
transport for append-only text, which is the one thing it is unambiguously good
at.

- Buys: everything in V0, plus real transactions (a multi-event command is
  atomic by the database rather than by hoping one `write(2)` is), plus
  constant-time time travel (section 5), plus a place to put attachments and
  derived indexes without inventing a file layout.
- Costs: two representations of the same events, so the export must be proven
  deterministic and append-only per author. Rules: an author only ever exports
  their own events, only in ULID order, only appending. Violate that and git
  starts producing diffs in the middle of files.
- Loses: the DB can be deleted and rebuilt from the mirror, so nothing is lost
  permanently, but a machine mid-sync has state in two places.

The failure mode to design against is a torn export: events written to the DB,
process dies before the mirror is appended. Fix: record the export watermark in
the same transaction that reads the events, and make the mirror append
idempotent (re-appending an already-present ULID is caught on import by `INSERT
OR IGNORE`, and on export by comparing the file's last line).

### V2 — Plain CRUD, no event log

Mutable `nodes` rows. Undo via a bounded journal or the SQLite session
extension. No `ref` tree, no ranks-as-events, no replay.

- Buys: dramatically less code. The current tree is ~19k lines of Go; V2 is
  plausibly 4-6k, because `event`, `state`, half of `session` and all of the
  ordering machinery disappear.
- Loses: history, time travel, offline multi-author merge, and undo-of-anything
  rather than undo-of-the-last-delete. Sibling ordering becomes an `ORDER BY`
  on a float or an integer with periodic renumbering — fine for one user,
  no longer a mergeable value.
- Sync: file-level only. Note that syncing a live SQLite file through Dropbox,
  iCloud or Syncthing is a known corruption source: the `-wal` and `-shm`
  sidecars are copied inconsistently. If V2 syncs at all, it must be via
  `VACUUM INTO` a snapshot with the tool closed, which is not really sync.

V2 is the honest answer if the goal is "a small, fast, local notes tool with a
proper query language". It should not be sold as a gnotes variant; it is a
different tool that shares a UI.

## 4. Schema sketch (V1)

```sql
PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;
PRAGMA synchronous = NORMAL;   -- WAL + NORMAL is crash-safe, not power-cut-safe

-- The log. Insert-only. The only table that is truth; everything below is a
-- fold over it and can be dropped and rebuilt.
CREATE TABLE events (
  seq     INTEGER PRIMARY KEY,   -- canonical position; see the note below
  id      TEXT NOT NULL UNIQUE,  -- ULID, 26 chars, lexicographically time-ordered
  ref     TEXT,                  -- the last event this author had seen; NULL at a log root
  action  TEXT NOT NULL,
  user_id TEXT NOT NULL,
  payload TEXT NOT NULL          -- the flat JSON payload, byte-identical to the JSONL line
) STRICT;
CREATE INDEX events_ref  ON events(ref);
CREATE INDEX events_user ON events(user_id, id);

-- Materialized tree. Surrogate INTEGER key so FTS5 external-content and every
-- join is cheap; the ULID stays the external identity.
CREATE TABLE nodes (
  rowid_      INTEGER PRIMARY KEY,
  id          TEXT NOT NULL UNIQUE,
  kind        TEXT NOT NULL,      -- workspace | notebook | note | task
  parent      TEXT REFERENCES nodes(id),
  rank        TEXT NOT NULL,      -- 96-bit hex, unchanged from today
  title       TEXT NOT NULL DEFAULT '',
  body        TEXT NOT NULL DEFAULT '',
  deleted     INTEGER NOT NULL DEFAULT 0,
  status      TEXT,               -- NULL on non-tasks; a CHECK enforces that
  priority    INTEGER,
  due         TEXT,               -- ISO 8601, NULL means none
  created_at  TEXT NOT NULL, updated_at TEXT NOT NULL,
  created_by  TEXT NOT NULL, updated_by TEXT NOT NULL,
  CHECK (kind IN ('workspace','notebook','note','task')),
  CHECK (kind = 'task' OR (status IS NULL AND priority IS NULL AND due IS NULL))
) STRICT;
CREATE INDEX nodes_parent_rank ON nodes(parent, rank);
CREATE INDEX nodes_due ON nodes(due) WHERE due IS NOT NULL AND deleted = 0;

CREATE TABLE tags      (node TEXT REFERENCES nodes(id), tag TEXT, PRIMARY KEY(node,tag)) STRICT;
CREATE TABLE links     (src  TEXT REFERENCES nodes(id), dst TEXT, PRIMARY KEY(src,dst)) STRICT;
CREATE TABLE assignees (node TEXT REFERENCES nodes(id), user_id TEXT, PRIMARY KEY(node,user_id)) STRICT;
CREATE TABLE contributors (id TEXT PRIMARY KEY, name TEXT NOT NULL) STRICT;
CREATE TABLE meta (k TEXT PRIMARY KEY, v TEXT NOT NULL) STRICT;  -- schema_version, export watermarks

-- Not content='nodes': FTS5 external content requires every indexed column to
-- exist on the content table, and tags live in their own. A view supplies the
-- column, and filters the deleted rows out of the index for free.
CREATE VIEW nodes_text AS
  SELECT n.rowid_ AS rowid_, n.title AS title, n.body AS body,
         (SELECT group_concat(t.tag, ' ') FROM tags t WHERE t.node = n.id) AS tags
    FROM nodes n
   WHERE n.deleted = 0 AND n.kind IN ('note','task');

CREATE VIRTUAL TABLE nodes_fts USING fts5(
  title, body, tags,
  content='nodes_text', content_rowid='rowid_',
  tokenize = "unicode61 remove_diacritics 2"
);
```

The `CHECK` on the last line of `nodes` is worth noting: the rule "task fields
are refused on notes" currently lives in the materializer and is enforced by
convention across four front ends. In the DB variant it becomes an invariant the
storage layer cannot be talked out of. That is a real, if modest, gain in
correctness.

**The `seq` wrinkle.** Canonical order is a depth-first walk of the `ref` tree,
which means an event arriving from another author can insert *between* existing
events and shift the suffix. Three options:

1. Do not store `seq`; order at load time as today. Correct, but throws away the
   incremental-startup win.
2. Store `seq` and renumber the affected suffix on import. Renumbering only
   happens on sync, touches at most the events after the insertion point, and is
   a single `UPDATE ... WHERE seq >= ?` in one transaction. Simple; probably
   right.
3. Give `seq` gaps (like ranks) so most insertions are local. Solves a problem
   that option 2 does not obviously have. Do not do this first.

Start with 2, and measure before considering 3.

## 5. What the database buys that JSONL cannot

Not startup speed. The measured cost today is ~1.1 us/event: 10k events in
11.5 ms, 50k in 54 ms. That is already imperceptible, and a snapshot cache (V0
without SQL) would fix the tail without any of this. Arguing for the variant on
performance grounds is arguing from the weakest position.

The real case is **queries the current design cannot answer cheaply**:

- **Ranked full text.** FTS5 gives BM25 with per-column weights, phrase search,
  prefix search, `NEAR`, and snippet extraction. The in-memory inverted index
  gives term matching with hand-rolled ranking. Verified that
  `contentless_delete=1` works on SQLite 3.51, so a contentless FTS table is
  also available if the duplicated text is unwelcome.

- **Constant-time time travel.** Today `--at` replays a prefix: linear in
  history. A bitemporal side table makes it an index seek:

  ```sql
  CREATE TABLE node_history (
    node TEXT, field TEXT, value TEXT,
    from_seq INTEGER NOT NULL, to_seq INTEGER,   -- NULL = still current
    PRIMARY KEY (node, field, from_seq)
  ) STRICT;
  ```

  "every task that was open on 1 June" stops being a replay and becomes a
  `WHERE from_seq <= ? AND (to_seq IS NULL OR to_seq > ?)`. Cost: roughly one
  extra row per event. This is the single most interesting thing the variant
  unlocks, and it is a *new capability*, not a faster version of an old one.

- **Aggregates over history.** Completion rates, burndown, stale notes,
  per-author activity, tag co-occurrence. All one query; none currently
  expressible.

- **Joins against other project data.** `ATTACH` a CI database, a coverage
  database, an exported issue tracker. The notes stop being an island.

- **Change notification without polling stat().** `PRAGMA data_version`
  increments when *another* connection commits and does not move for your own
  writes. Verified:

  ```
  reader data_version: 2
  after external commit: 3
  own commit does not bump: 3
  ```

  That replaces the mtime/size fingerprint with a primitive that cannot produce
  a false negative from coarse timestamp granularity.

## 6. What breaks, and how badly

| property | V0 | V1 | V2 |
|---|---|---|---|
| conflict-free git merge | kept | kept via text mirror | lost |
| readable/greppable history in the repo | kept | kept | lost |
| offline multi-author | kept | kept | lost |
| time travel | kept | improved | lost |
| single binary, no cgo | at risk | at risk | at risk |
| four concurrent front ends | kept | kept (WAL) | kept (WAL) |
| binary size | +3-6 MB | +3-6 MB | +3-6 MB, minus removed code |

Additional risks that apply to any of them:

- **Network filesystems.** SQLite's locking is unreliable on NFS and SMB. The
  current design is immune because appends to distinct files need no locking. If
  anyone keeps a project on a network share, the variant regresses.
- **`SQLITE_BUSY`.** Four front ends writing means every write path needs a
  `busy_timeout` and a retry policy. Today the kernel's `O_APPEND` handles this
  for free.
- **Corruption is now a single point of failure.** One bad page loses the
  project. Today a truncated log loses a tail and the rest still replays. V1
  mitigates this because the git mirror is a full backup; V2 does not.
- **Two writers of the same author on one machine** currently both append and
  the ordering sorts it out. With a DB they serialize, which is better, but the
  `ref` chain has to be read inside the write transaction or two commands will
  branch from the same ref for no reason.

## 7. Implementation language

Requirement is a self-contained single executable. Ranked by fit:

| option | binary (est.) | cgo/static | notes |
|---|---|---|---|
| Go + `modernc.org/sqlite` | +4-6 MB | pure Go, cross-compiles everywhere | slower than C SQLite on write-heavy work; the existing 19k lines are reusable |
| Go + `mattn/go-sqlite3` | +1-2 MB | needs cgo | fast, but loses trivial cross-compilation and clean static linking against glibc |
| Rust + `rusqlite` (`bundled`) | 5-10 MB total | statically links the C amalgamation | best size/speed/self-containment; `ratatui` for the TUI, `axum` or `tiny_http` for the web view; total rewrite |
| Zig + SQLite amalgamation | smallest | trivially static, superb cross-compilation | ecosystem for TUI/HTTP is thin; language still churning |
| C# NativeAOT | 12-20 MB | single file | good tooling, heavy runtime footprint |
| Python + PyInstaller | 20-60 MB | bundle, not a binary | `sqlite3` is stdlib, so prototyping is fastest here; ships badly |

Sizes other than the current build are estimates and should be measured before
being relied on. The current binary is 7.4 MB with the web view and 4.2 MB with
`-tags noweb`; a pure-Go SQLite would more than double the slim build, which is
the specific cost that argued against it before.

**The decisive factor is not the language, it is how much is being reused.**
Under V1, only `store` is replaced and `state` gains an incremental-fold path.
`event`, `session`, `rank`, `ulid`, `search` (or its removal), `cli`, `tui`,
`web` and `mcp` are all untouched, because `session` already talks to storage
through a narrow interface and `state.Materialize` is a pure function. That is
perhaps 1500-2500 lines of change against a 19k-line tree. Rewriting in Rust to
change the storage layer is throwing away 17k working, tested lines to avoid a
4 MB dependency.

So: **stay in Go for a variant; choose Rust only if the intent is V2, a
different and much smaller program.**

## 8. Port map (V1, in Go)

| package | change |
|---|---|
| `internal/event` | none. The wire format is reused verbatim as the `payload` column |
| `internal/ulid`, `internal/rank` | none |
| `internal/store` | replaced. `Load` becomes "open DB, ensure schema, fold any unfolded tail"; `Append` becomes an `INSERT` inside the caller's transaction; `Fingerprint` becomes `PRAGMA data_version` |
| `internal/state` | `Materialize` stays as the pure fold (needed for rebuild and for `--at`); gains `Apply(tx, event)` so a single event can be folded incrementally |
| `internal/search` | deleted, replaced by an FTS5 query. Keep the tokenizer's tests as FTS5 acceptance tests |
| `internal/session` | `Commit` wraps the batch in a transaction instead of building a byte buffer |
| `internal/gitsync` | gains export/import of the text mirror around the existing commit/pull |
| `internal/cli`, `tui`, `web`, `mcp` | none, if the seam holds. If any of them break, the seam was not where it was claimed to be — which is itself worth learning |

That last row is the real test. If a storage swap requires touching the four
front ends, the "every front end is a shell over one write path" claim in the
README is weaker than it reads.

## 9. Suggested order of work

1. **`gnotes export`** in the existing tool. One command, no architectural
   commitment, and it answers the SQL-and-analytics wish immediately. It also
   produces the schema and the fold code that V1 needs, so none of it is
   throwaway. **Done** — see section 11.
2. **Measure V0.** Wire the export up as a gitignored cache with FTS5 and
   compare search quality and startup against the current build on a synthetic
   50k-event log. If FTS5 does not noticeably beat the in-memory index on real
   queries, the strongest argument for the variant is gone.
3. **V1 behind a build tag or a `store` field in `project.json`.** Both backends
   in one binary, the same test suite run twice. Any behavioural divergence is a
   bug in one of them, which is the cheapest possible way to keep them honest.
4. **Decide.** Either V1 wins and the JSONL becomes a mirror, or it does not and
   step 1 was still worth having.

V2 is a separate repository if it happens at all.

## 10. Step 1 as built

`gnotes export` writes a **SQL script**, not a database file. That is a
deliberate departure from the sketch above, and the reason is the last open
question: linking a driver to write a `.db` directly would have cost 4-6 MB on
a binary whose entire browser view costs 3 MB, for a command most people will
never run. Emitting text costs **65 KB measured** (7,768,818 to 7,835,266 bytes
with `-s -w`), needs no new module in `go.mod`, and loses nothing — `sqlite3`
reads a script from a pipe, so the user-facing form is one character longer:

```sh
gnotes export | sqlite3 notes.db
```

The exact-text form has two side benefits that a driver would not have given.
The output is byte-for-byte reproducible from the same log, so a regenerated
export diffs cleanly against the previous one. And the tests can assert on the
script directly, then pipe it through the system `sqlite3` to confirm a real
database accepts it — the package under test has no database dependency, so
neither does its test suite, and the checks that need `sqlite3` skip where it
is absent rather than failing.

What shipped, against the schema in section 4:

| | |
|---|---|
| `events` | the whole log in canonical order, with `seq`, the decoded timestamp, and the payload as JSON |
| `nodes` | one table for all four kinds, with the section-4 CHECK constraints and a generated `priority_name` |
| `tags`, `links`, `assignees`, `contributors` | as sketched, with no foreign key on link targets or assignee ids, because both may legitimately name something that has not synced yet |
| `node_history` | the bitemporal table from section 5, one row per interval a field held a value, with set members (`tag`, `assignee`, `link`) using the same shape as scalars so one query answers both |
| `nodes_fts` | FTS5 external-content over a view, so the text is stored once and tags are searchable despite living in their own table |

Three things were settled by building it rather than by argument.

**FTS5 external content can be backed by a view.** That was the open question in
section 4 — `tags` is not a column on `nodes`, so `content='nodes'` could not
work. A view joining the tag table supplies the column, `content_rowid` points
at the surrogate integer key, and `INSERT INTO nodes_fts(nodes_fts)
VALUES('rebuild')` populates it. The view also filters deleted entries, so a
removed note leaves the index without a trigger.

**Foreign keys can stay enabled for the whole load.** Nodes are emitted parents
first, ordered by depth and then by id, which makes the import a structural
check on the exported tree rather than only a copy. A parent that is somehow
not in the tree is written as NULL instead: an export that refuses to load
would be worse than one that reports an orphan as a root.

**The history fold is straightforward and is the piece V1 reuses.** It is the
same walk as `state.Materialize`, recording what each event changed rather than
only where it ended up: scalars close their open interval when set again, set
members close theirs when removed. Canonical values are stored, not typed ones,
so `prio h` and `priority high` compare equal in a query. About 120 lines.

What this does *not* settle: nothing here measures V0. The export builds a
database from a full replay, which is what step 2 has to replace with an
incremental fold before the cache is worth anything.

## 11. Open questions

- Is the driver query capability, startup speed, search quality, or curiosity?
  The four have different cheapest answers, and only the first justifies this.
- Does the variant need to stay multi-author, or is this a single-user tool?
  That single answer chooses between V1 and V2.
- Does it need to stay git-native? If the notes no longer live in the repo and
  travel with the code, a large part of the original premise is gone and the
  design should be reconsidered from scratch rather than ported.
- Is a 4-6 MB binary increase acceptable? It is larger than the entire embedded
  web view, which was itself considered worth a build tag to remove. Step 1
  sidestepped this by emitting text, but V0 and V1 cannot: they need a driver
  in the binary, so the question returns in full at step 2.
