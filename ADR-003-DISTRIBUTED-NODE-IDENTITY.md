# ADR-003: Distributed Node Identity — UID anchor + dot-path display

**Status:** Proposed (direction agreed; Tier A/B and rollout are open — see §6, §8)
**Decision:** Give every node a durable, collision-free **UID** that is its true identity and the target of all references; keep the human **dot-path** (`PRJX-1.4`) as a *mutable display/address*. A node that cannot register a clean sequential number with the hub takes a **UID-form provisional id** (`PRJX-1.1.<uid>`) instead of guessing a sequential that could collide. The hub registry becomes a *display-number allocator*, not identity-critical state.
**Context ticket:** MTIX-28 (sync: concurrent create-under-same-parent collides; one ticket silently lost). Empirical reproduction: `e2e/sync_collision_test.go`.

---

## 1. Problem

mtix assigns dot-notation IDs from a **local** sequence counter (`node_service.go` → `NextSequence`); the hub assigns no IDs (SYNC-DESIGN §2). Two CLIs that have not yet pulled each other both compute the same next seq under a shared parent and mint the same id (e.g. `PRJX-1.4`). At sync, both `create_node` events carry that id with distinct `event_id`s, the hub stores both, `detectConflicts` skips `create_node`, and `applyCreateNode` is `INSERT OR IGNORE` — so each creator keeps its own row and ignores the other's. The empirically-reproduced result (against a real Postgres hub) is **split-brain**: the two replicas permanently disagree about what `PRJX-1.4` is, a newcomer sides with the hub-order winner, and no conflict is surfaced. Worse, an external immutable reference (a git commit "fixes PRJX-1.4.3" read later by a CI agent) can silently resolve to a *different* task.

The root cause is structural: a dot-notation id does **identity**, **position**, **human address**, and **offline assignment** in one token. No massively-parallel system keeps all four — they split identity from display and make concurrency land on whichever is cheapest to keep collision-free (Snowflake/UUID for opaque identity; Figma/CRDT fractional indexing for position; Git content-hash identity with a mutable branch label; Cloudflare Durable Objects for per-key serialization). This ADR adopts the **Git/Snowflake lineage**: opaque durable identity (UID) + a reconciled human label (dot-path).

## 2. Decision

1. **UID is the durable identity.** Every node gets a UUIDv7 at creation (reuse `clock.NewEventID`; collision is a non-consideration at mtix scale). The UID is immutable for the node's life, stored, and **indexed** (not scan-resolved).
2. **Dot-path is display/address, and it is mutable.** It is the friendly id humans read and type, but it is not the thing the system keys identity on.
3. **Provisional ids are UID-form and collision-free by construction.** A node that cannot (yet) claim a clean sequential number from the hub is `PRJX-1.1.<uid>`; its offline descendants are sequential under it (`PRJX-1.1.<uid>.1`) and still globally unique because the ancestor segment is unique. Two offline users adding under the same parent therefore **never collide** while provisional.
4. **Eager background claim at create.** When online, creation triggers a background claim of the next clean sequential; it lands *permanent* within ~1 hub-RTT (seconds on a Neon cold start), so the provisional window is tiny in the connected case. When offline, the node stays provisional (safe) and resolves on next sync. Claiming is the *same* mechanism online and offline — one code path, two latencies.
5. **The hub registry is a derived display-number allocator, not identity-critical state.** Implement it as a **partial unique index** `UNIQUE (project_prefix, node_id) WHERE op_type='create_node'` over the append-only event log — no separate authoritative table. First successful `create_node` push wins the number; a second for the same number is **rejected loudly** (new push outcome: "renumber required"), never silently ignored.
6. **Collisions on the display number resolve deterministically and losslessly.** Winner is chosen by the order LWW already trusts — `(lamport_clock, wall_clock_ts, author_machine_hash)` (SYNC-DESIGN §8.2). The loser's *display number* renumbers (subtree rides along); **all references survive because they resolve by UID.** Offline/import resolution uses the same tiebreak and emits a **UID-keyed remap file** (`<uid> → new dot-path`) plus a loud report — housekeeping is mechanical, not eyeball-a-tree.
7. **Restore-safety inherits NFR-2.8.** Because the registry is derived from the append-only log and the hub originates nothing (clients are the source of truth), a hub restored from a stale backup **self-heals**: every client re-pushes its own events idempotently (`ON CONFLICT (event_id) DO NOTHING`). The previously-catastrophic case — a restore re-issuing a "permanent" number to two nodes — is now **cosmetic**: the two nodes have distinct UIDs (distinct identities, valid references), so the only damage is two nodes wanting the same display number, fixed by a deterministic renumber. **No data-loss or unresolvable case remains.**

### Precise stability guarantee

A node's **UID is immutable forever**. A node's **dot-path is immutable only once the node *and all its ancestors* are permanent** — registering a provisional ancestor renumbers descendants' path strings (the path encodes ancestry). Therefore: **external references MUST resolve by UID, never by dot-path.** The dot-path is always "current display."

### Self-announcing provisional state (resolves the Hole-2 "user must know" requirement)

Provisional-ness is encoded in the id's *shape* (`PRJX-1.1.<uid>` vs `PRJX-1.4`), so it is self-announcing — no separate flag or banner, and tooling (commit hooks, mgit, CI agents) can warn "this is a provisional reference" from the string alone. Because provisional ids are collision-free, carrying one is *safe*; the message is "this id is provisional and gets a clean number on sync," not "danger, sync now." Human surfaces should show a truncated/friendly form (e.g. `PRJX-1.1.~a3f`) while keeping the full UID underneath.

## 3. What this changes vs. today

- Reverses SYNC-DESIGN §2 "the hub assigns no IDs": the hub now arbitrates the clean sequential number (via the derived index). This is a deliberate, documented change; the hub still runs no business logic and still originates no events.
- `create_node` payload gains a `uid` field; nodes table gains an indexed `uid` column.
- Push gains a "renumber required" rejection outcome.

## 4. Options considered and rejected

- **Coordinate the id at create (synchronous hub allocation / seq-range leasing).** Rejected: puts the hub on the create hot path and breaks offline creation — violates local-first.
- **Author-namespaced sequential segments** (`PRJX-1.4~m2`). Rejected: destroys the clean human-readable id that justifies the product.
- **Pure CRDT fractional indexing for child position** (Figma/Logoot). Most elegant for concurrent inserts, but the visible number stops being a clean contiguous integer — taxes the exact thing mtix sells. Adopted only in spirit (UID = the actor-unique position key).
- **Do nothing / surface-only.** Rejected: leaves silent split-brain; MTIX-28 is data loss.

## 5. Consequences

**Positive:** concurrent creates never collide while provisional; references survive renumbering and restore; the worst failure is cosmetic; restore-safety reduces to the durability bar already built; provisional state is self-evident.

**Negative / costs:** provisional ids are long/ugly (mitigated by short eager-claim window + truncated display); a node carries two handles (UID + dot-path); the hub gains an id-arbitration role; Tier B (below) is a wire-protocol change.

## 6. Open decision — Tier A vs Tier B (what does the event log key on?)

- **Tier A — UID as reference anchor only.** Events keep keying on the dot-path; UID is stored/indexed for resolving references. Renumbering still requires remapping the loser's events from old path → new (bounded: local/un-pushed at push time, or via the remap file offline). Smaller change; mixed old/new CLIs interoperate (the `uid` field is additive and ignored by old clients).
- **Tier B — UID as the event key (recommended target).** Events reference the UID; the dot-path becomes a pure display attribute. Renumbering then touches **zero events**, so the cross-replica remap problem that drove Holes 2/3/4 *vanishes entirely*. Larger change: schema + every event + the node_id-is-dotpath assumption across CLI/MCP/REST/web, and a protocol-major version bump.

Recommendation: adopt **Tier B as the target** (it removes the disease rather than patching symptoms); Tier A is acceptable as an interim if Tier B's scope must be staged. **This is the one decision still open.**

## 7. Migration / upgrade path for existing users

Existing stores key everything on the dot-path and have no UIDs. Migration must be safe (we now back up before migrate and gate on the NFR-2.8 integrity checks) and must tolerate a mixed-version synced team.

**Phase 0 — local UID backfill (both tiers).** Schema bump (v2 → v3), auto-migrated on first startup of the new binary, in a per-step transaction (existing `init()` pattern), after the NFR-2.8 open-time gates and the automatic pre-mutation backup (MTIX-26.6). For every existing node lacking a UID, generate a UUIDv7 into the new indexed `uid` column. **Existing dot-paths do not change** — already-created nodes are non-colliding within a store, so they keep their paths and simply gain an anchor. Additive and backward-safe: old data stays readable; the `uid`/payload field is ignored by pre-v3 binaries.

**Phase 1 — hub pre-constraint collision sweep.** The partial unique index cannot be added to a hub whose log already contains duplicate `create_node (project_prefix, node_id)` (i.e., where the MTIX-28 bug already bit). On first hub migration by an upgraded CLI: scan for such duplicates, resolve each with the deterministic tiebreak, renumber losers, emit UID-keyed remaps, and surface the result to affected CLIs via `sync_conflicts` — **never silently**. Only once the log is collision-free, add the index.

**Phase 2 — reference resolution.** CLIs resolve references by UID *and* dot-path. References written before the upgrade are dot-path-only, but they keep working because pre-existing nodes' paths don't move; the UID scheme protects *future* references and *future* concurrent creates. (Migration is forward-looking, not retroactive — stated honestly.)

**Phase 3 — Tier cutover.**
- *Tier A:* stops at Phases 0–2. Wire format unchanged (events still carry dot-path `node_id`; `uid` rides in the `create_node` payload as an additive field). Old and new CLIs fully interoperate.
- *Tier B:* protocol-major bump. During the transition, `create_node` events carry **both** `node_id` (dot-path, for old clients) and `uid`; the hub translates. A project cuts over to UID-keyed events only when **all** its CLIs report a compatible version — use the existing `sync_projects.last_seen_cli_version` hook (migration 003) for that negotiation; the hub refuses the cutover until the whole project is upgraded. After cutover, the dot-path is a derived display attribute and renumbering stops touching events.

**Safety properties (all phases):** backup-before-migrate; per-step transactions with `schema_version` bumped only on success; idempotent re-run; hub collision sweep logged and surfaced; mixed-version rollout safe by construction (Tier A) or gated by version negotiation (Tier B).

## 8. Open questions

1. **Tier A vs Tier B** (§6) — the primary open decision.
2. Display form of provisional ids (full UID vs truncated) and exactly which surfaces show which.
3. Whether external-reference tooling (mgit/commit hooks) should *warn* or *block* on a provisional-id reference.
4. The user-facing "next steps" wording for the (now cosmetic) display-number collision and for a permanent sweep during Phase 1.
