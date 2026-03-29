# ADR-001: Storage Engine — BadgerDB vs SQLite

**Status:** Under Review
**Date:** 2026-03-07
**Context:** Choosing the embedded storage engine for mtix before implementation begins

---

## Evaluation Criteria

Every claim below is grounded in a specific mtix requirement. No hand-waving.

---

## 1. Performance Requirements

### NFR-1.1: Node creation <10ms

**BadgerDB:** LSM-tree architecture is optimized for writes. A single key-value put in BadgerDB completes in ~1-3ms. Creating a node requires writing the node JSON (`n:` key) plus 3-5 index keys (`c:`, `s:`, `p:`, `a:`, `cseq:`), all in one transaction. Comfortably under 10ms.

**SQLite (WAL mode, pure Go driver):** A single INSERT with indexes updates completes in ~2-5ms. The pure Go driver (modernc.org/sqlite) is ~75% as fast as the CGO-based driver. With WAL mode and `synchronous=NORMAL`, SQLite handles 70k-100k write transactions/second. A node creation is one INSERT plus automatic index updates — comfortably under 10ms.

**Verdict:** Both pass. BadgerDB has a slight edge on raw write latency, but the difference is negligible at mtix's scale (agents create nodes per minute, not per millisecond).

### NFR-1.2: Tree retrieval (1000 nodes) <100ms

**BadgerDB:** Retrieving a subtree requires a prefix scan on `c:{project}:{parent}:*`, then recursively scanning children. For 1000 nodes, this means ~1000 individual key lookups (one per `n:` key) after walking the child index. Each lookup is ~0.1ms. Total: ~100ms — tight against the requirement.

**SQLite:** A recursive CTE query (`WITH RECURSIVE children AS (...)`) retrieves the entire subtree in a single query. SQLite's B-tree is optimized for reads. With proper indexes, 1000-node retrieval typically completes in 10-30ms. No application-level recursion needed.

**Verdict:** SQLite wins decisively. Recursive tree queries are SQL's strength. BadgerDB requires application-level tree walking with N+1 lookups.

### NFR-1.3: Progress propagation (depth 10) <50ms in single transaction

**BadgerDB:** Progress propagation walks from leaf to root, reading each ancestor's children to recalculate progress, then writing the updated progress. At depth 10, this is ~10 reads + ~10 writes in one transaction. BadgerDB SSI transactions handle this in ~20-30ms.

**SQLite:** Same logic, but the reads are SQL queries (`SELECT COUNT(*) FROM nodes WHERE parent_id = ? AND status = 'done'`) with indexed lookups. Each level is ~1ms. Total: ~10-15ms. However, SQLite serializes all writes — concurrent progress rollups (FR-5.7's test scenario) queue behind each other.

**Verdict:** Close. BadgerDB's SSI transactions handle concurrent progress rollups better (optimistic concurrency with retry). SQLite serializes writes, which simplifies the code (no SSI conflicts to retry) but means concurrent agents wait in line. For mtix's scale (3-10 agents), SQLite's write serialization is not a bottleneck.

### NFR-1.4: CLI cold start <200ms

**BadgerDB:** Opening a BadgerDB database takes 10-50ms for small databases. However, a known issue reports 2+ minute open times for large databases (hundreds of GB, hundreds of SST files). mtix databases will be small (thousands of nodes = megabytes), so this is not a concern.

**SQLite:** Opening a SQLite file takes <5ms regardless of size (it's just opening a file handle). No SST file scanning.

**Verdict:** SQLite has a slight edge on cold start, but both are well within 200ms for mtix-scale data.

---

## 2. Query Complexity

### FR-6.3: `mtix ready` — unblocked, unassigned, open work

**BadgerDB:** Scan `s:{project}:open:*` for open nodes, then for each, check (a) no assignee (read `a:` index), (b) no unresolved blockers (scan `d:` keys). This is a multi-step scan-and-filter operation requiring application-level joining.

**SQLite:**
```sql
SELECT * FROM nodes
WHERE status = 'open' AND assignee IS NULL
  AND id NOT IN (SELECT to_id FROM deps WHERE dep_type = 'blocks' AND resolved = false)
ORDER BY priority LIMIT 50;
```
One query. The database engine handles the join and filtering.

### FR-6.3: `mtix stale` — nodes not updated in >24h (+ deferred auto-wake per FR-3.8b)

**BadgerDB:** Full scan of all nodes, filtering by `updated_at` timestamp. No way to index by "time since last update" without a dedicated time-based index key. For deferred auto-wake, scan all deferred nodes checking `defer_until`.

**SQLite:**
```sql
SELECT * FROM nodes WHERE updated_at < datetime('now', '-24 hours')
UNION ALL
SELECT * FROM nodes WHERE status = 'deferred' AND defer_until IS NOT NULL AND defer_until < datetime('now');
```

### FR-5.3: Progress calculation — count(done children) / count(total - cancelled - invalidated)

**BadgerDB:** Scan `c:{project}:{parent}:*` to get children, read each child's status from `n:` key, count in application code. N+1 reads.

**SQLite:**
```sql
SELECT
  COUNT(CASE WHEN status = 'done' THEN 1 END) AS done_count,
  COUNT(CASE WHEN status NOT IN ('cancelled', 'invalidated') THEN 1 END) AS denominator
FROM nodes WHERE parent_id = ?;
```

### NFR-2.7: Full-text search

**BadgerDB (Phase 1):** Prefix scan with regex matching on title, prompt, description. Scans every node in the project — O(n) with string matching. For Phase 5, needs bleve or another FTS library.

**SQLite:** FTS5 virtual table gives you fuzzy matching, ranking, snippets, and prefix search out of the box:
```sql
SELECT * FROM nodes_fts WHERE nodes_fts MATCH 'retry logic' ORDER BY rank;
```
No separate library needed. Index updates are automatic and synchronous.

**Verdict across all queries:** SQLite wins comprehensively. Every complex query that requires multiple steps in BadgerDB is a single SQL statement. The `ready`, `blocked`, `stale`, `orphans`, and `search` queries — which are core to the agent workflow — are all significantly simpler to implement and maintain with SQL.

---

## 3. Transaction Model

### NFR-2.3: All writes transactional (indexes update atomically)

**BadgerDB:** SSI (Serializable Snapshot Isolation) with optimistic concurrency. Multiple transactions can proceed concurrently; conflicts are detected at commit time. FR-5.7 specifies retry with jittered backoff for SSI conflicts during concurrent progress rollup. This works but adds application complexity (retry loops, conflict handling).

**SQLite (WAL mode):** Single-writer with unlimited concurrent readers. All write transactions are serialized — no conflicts possible. This is simpler to reason about but means concurrent writes queue. FR-5.7's concurrent progress rollup scenario doesn't produce conflicts — the writes simply serialize. The "first agent finishes, second agent finishes, third agent finishes" order is deterministic.

**Verdict:** Trade-off. BadgerDB's SSI allows higher write concurrency but requires application-level conflict handling (FR-5.7 retry logic). SQLite's single-writer eliminates conflicts but serializes writes. For mtix's scale (3-10 concurrent agents, writes measured in tens per second), SQLite's serialization is not a bottleneck — and it eliminates an entire class of bugs (SSI conflict handling, retry logic, jittered backoff). The FR-5.7 retry specification becomes unnecessary with SQLite.

---

## 4. Index Management

### NFR-2.2: 14 key patterns for efficient range scans

**BadgerDB:** All 14 key patterns are manually managed. When creating a node, you write `n:`, `c:`, `s:`, `p:`, `a:` keys — 5 writes. When updating status, you delete the old `s:` key and insert the new one. When soft-deleting, you write a `del:` key and delete `c:`, `s:`, `p:`, `a:` keys. Every state transition requires manually maintaining these secondary indexes. FR-3.3a (gc cleanup) must manually remove all associated keys. This is significant implementation surface area — getting any index update wrong produces silent data corruption (node appears in wrong status filter, orphaned index entries, etc.).

**SQLite:** You declare indexes once in DDL:
```sql
CREATE INDEX idx_nodes_status ON nodes(project, status);
CREATE INDEX idx_nodes_priority ON nodes(project, priority);
CREATE INDEX idx_nodes_assignee ON nodes(project, assignee);
CREATE INDEX idx_nodes_parent ON nodes(project, parent_id);
```
The database engine maintains all indexes automatically on every INSERT/UPDATE/DELETE. You cannot have stale indexes. No manual cleanup needed for gc — `DELETE FROM nodes WHERE id = ?` removes the row and all index entries atomically.

**Verdict:** SQLite wins on correctness guarantees and implementation simplicity. Manual index management in BadgerDB is a significant source of potential bugs. The 14 key patterns represent ~200 lines of careful key-building code that must stay in sync with every write operation. With SQLite, this entire concern disappears.

---

## 5. Cloud Sync (NFR-3)

### NFR-3.1-3.5: Local-first, offline, optional sync with conflict resolution

**BadgerDB:** You build sync from scratch. NFR-3.2 requires generating sync events with vector clocks on every write. NFR-3.3 requires implementing LWW, set union, append-merge, and status priority conflict resolution. NFR-3.4 requires content hash comparison. This is a substantial engineering effort — weeks of development plus ongoing maintenance.

**SQLite + cr-sqlite:** cr-sqlite adds CRDT-based conflict-free replication to SQLite tables. You mark tables as CRRs (conflict-free replicated relations), and cr-sqlite automatically tracks changes, generates merge operations, and resolves conflicts. LWW (last-write-wins) comes built-in. The sync protocol is: extract changesets → transfer → apply changesets. No vector clocks to implement, no manual conflict resolution.

**Important caveat:** cr-sqlite is a C extension, not pure Go. Using it with modernc.org/sqlite (pure Go) would require either: (a) using the CGO SQLite driver instead (breaks easy cross-compilation), (b) using cr-sqlite as a separate process, or (c) porting the CRDT logic to Go application code. This is a significant practical concern.

**SQLite + Litestream:** Litestream provides continuous streaming replication of SQLite databases to S3/cloud storage. This gives you backup and read replicas trivially but does NOT give you multi-writer conflict resolution — it's single-writer replication only.

**BadgerDB alternative — just use PostgreSQL for cloud:** NFR-3.5 already specifies PostgreSQL for the cloud authoritative store. The sync layer is essentially: "serialize local changes → push to PostgreSQL → pull remote changes → merge locally." The storage engine choice (BadgerDB vs SQLite) affects the local representation, but the sync complexity is mostly in the merge logic, which is application-level regardless.

**Verdict:** Slight edge to SQLite for future sync work, but this is a Phase 5 concern. For Phase 1 (local-only), both are equivalent. The cr-sqlite path is compelling but has practical Go integration challenges.

---

## 6. Single Binary (NFR-4.7)

**BadgerDB:** Pure Go. Compiles into the binary with zero external dependencies. Proven.

**SQLite (modernc.org/sqlite):** Pure Go (transpiled from C). Compiles into the binary with zero CGO dependency. Adds ~15-20MB to the binary size (the transpiled SQLite engine). Cross-compilation works. Proven in production (used by Gogs, Gitea, and others).

**Verdict:** Both meet NFR-4.7. SQLite adds more to binary size but it's a one-time cost.

---

## 7. Data Model Fit

### FR-3.1: Node with 20+ fields, activity stream, annotations

**BadgerDB:** Each node is stored as a single JSON blob under the `n:` key. The activity stream is embedded in the JSON. Simple to serialize/deserialize using Go structs. No schema to maintain. Adding a new field is just adding a Go struct field — old data without the field gets zero-valued.

**SQLite:** Nodes map to a `nodes` table. Activity entries map to an `activity` table (one-to-many). Annotations map to an `annotations` table. Dependencies to a `dependencies` table. Schema changes require SQL migrations. However, SQLite's `ALTER TABLE ADD COLUMN` is instant (just metadata), and the schema is explicit documentation of the data model.

**Verdict:** Trade-off. BadgerDB's schemaless JSON is more flexible and requires no migrations. SQLite's explicit schema catches type errors at the database level and is self-documenting. For a project with 20+ fields and complex relationships (parent-child, dependencies, annotations), an explicit schema is arguably better for long-term maintainability — but adds upfront work.

---

## 8. Export/Import (FR-7.8)

**BadgerDB:** Export requires scanning all key prefixes, deserializing each value, and assembling the JSON export. Import requires writing all keys and maintaining index consistency.

**SQLite:** Export is straightforward — `SELECT * FROM nodes; SELECT * FROM dependencies; ...` serialized to JSON. Or even simpler: the `.dump` command or `sqlite3_serialize()` gives you the entire database as a portable blob. Import is a set of INSERTs or a database file copy.

**Verdict:** SQLite is slightly simpler for export/import, but the difference is minor.

---

## 9. Maintenance Concerns

### BadgerDB

- **Maintenance status:** The primary maintainer (Dgraph Labs) has had financial difficulties. The Tendermint project considered removing BadgerDB support citing maintenance concerns. The `outcaste-io/badger` fork exists as a community alternative, but the ecosystem is fragmented.
- **Compaction:** LSM-tree databases require periodic compaction to reclaim space. BadgerDB handles this automatically, but compaction can cause latency spikes.
- **Value log GC:** BadgerDB separates keys and values. The value log requires periodic garbage collection (`DB.RunValueLogGC()`). Forgetting this leads to unbounded disk usage.

### SQLite

- **Maintenance status:** Actively maintained by the original developers since 2000. Used in billions of deployments. Not going away.
- **WAL checkpointing:** WAL files grow and need periodic checkpointing. SQLite handles this automatically (`wal_autocheckpoint`), with configurable thresholds.
- **VACUUM:** Deleted data doesn't shrink the file. Periodic `VACUUM` or `auto_vacuum` reclaims space. This is simpler than BadgerDB's compaction + value log GC.

**Verdict:** SQLite has better long-term maintenance health. BadgerDB's future is less certain.

---

## 10. Spec Impact Assessment

If mtix switches from BadgerDB to SQLite, these spec sections change:

| Section | Impact |
|---------|--------|
| NFR-2.1 | Change "BadgerDB" to "SQLite" |
| NFR-2.2 | Replace 14 key patterns with SQL schema DDL |
| NFR-2.4 | Simplify — SQLite WAL inherently separates readers/writers |
| NFR-2.5a | Simplify — SQLite file locking is simpler than BadgerDB's LOCK file |
| NFR-4.4 | Change "BadgerDB" to "SQLite (modernc.org/sqlite)" |
| FR-3.3a | GC cleanup becomes `DELETE FROM nodes WHERE deleted_at < ?` |
| FR-5.7 | SSI retry logic becomes unnecessary (single-writer eliminates conflicts) |
| FR-7.8 | Export simplifies to SQL queries |
| NFR-2.7 | Phase 1 FTS becomes FTS5 instead of regex scanning |
| Project structure | `internal/store/badger/` (7 files) collapses to `internal/store/sqlite/` (~3 files) |

Sections that don't change: all FR-3.x (state machine), FR-5.x (progress), FR-6.x (CLI), FR-7.x (REST), FR-8.x (gRPC), FR-9.x (UI), FR-10.x (agents), FR-12.x (prompts), FR-14.x (MCP), NFR-3.x (sync), NFR-5.x (security).

---

## Summary Scorecard

| Criterion | BadgerDB | SQLite | Weight for mtix |
|-----------|----------|--------|-----------------|
| Write performance | ★★★★★ | ★★★★ | Low (agents write ~10/min) |
| Read/query performance | ★★★ | ★★★★★ | High (every agent op queries) |
| Query complexity | ★★ | ★★★★★ | High (ready, blocked, stale, search) |
| Transaction simplicity | ★★★ | ★★★★★ | High (less app-level retry code) |
| Index management | ★★ | ★★★★★ | High (14 manual indexes vs automatic) |
| Full-text search | ★★ | ★★★★★ | Medium (core agent workflow) |
| Tree operations | ★★ | ★★★★ | High (recursive CTE vs N+1 lookups) |
| Single binary | ★★★★★ | ★★★★ | Must-have (both pass) |
| Schema flexibility | ★★★★★ | ★★★ | Low (schema is stable post-v1) |
| Sync story (future) | ★★ | ★★★ | Low (Phase 5, both need work) |
| Long-term maintenance | ★★★ | ★★★★★ | Medium |
| Go ecosystem fit | ★★★★★ | ★★★★ | Medium |
| Implementation effort | ★★★ | ★★★★ | High (less code to write/maintain) |

---

## Risks

### Risk of choosing BadgerDB
- **Manual index bugs:** 14 key patterns maintained in application code. A single missed index update produces silent data corruption (node appears in wrong query results). This is the highest-risk item.
- **Maintenance trajectory:** Dgraph Labs' uncertain future means BadgerDB may not receive critical fixes.
- **Query complexity explosion:** Every new query (and mtix has many) requires hand-coded multi-step prefix scans.

### Risk of choosing SQLite
- **Pure Go driver maturity:** modernc.org/sqlite is a transpiled C-to-Go port. It's ~75% as fast as native SQLite and has been production-tested, but edge cases in the transpilation layer are possible.
- **Write serialization under high concurrency:** If mtix scales to 50+ concurrent agents all writing simultaneously, SQLite's single-writer model could become a bottleneck. (This is unlikely for Phase 1-3.)
- **Binary size:** The transpiled SQLite engine adds ~15-20MB to the binary. Minor but non-zero.

---

## Recommendation Disclosure

This document attempts to present both options fairly. However, the analysis inherently reflects that mtix's requirements (complex queries, tree operations, full-text search, multiple index maintenance) align more naturally with a relational database than a key-value store. A different application (high-throughput event streaming, simple key lookups, write-heavy workloads) would favor BadgerDB.

The honest framing: BadgerDB is a fine choice that will work. SQLite is a better fit for *this specific application's* query patterns and complexity profile.
