# ADR-003: Distributed node identity — stable dot-path on the surface, UID anchor underneath

**Status:** Accepted (design finalized; implementation not started)
**Context:** MTIX-28 (concurrent create-under-same-parent collides; one ticket silently lost). Empirical reproduction: `e2e/sync_collision_test.go`.
**Supersedes:** the earlier draft of this ADR, which over-rotated by making the UID the surface reference key. That is explicitly rejected here (§3): the dot-path remains the only identity exposed to humans, agents, and external systems.

## 1. Problem

Node IDs are dot-paths (`PRJX-1.4`, `PRJX-1.4.3`) assigned from a **local** sequence counter; the hub assigns no IDs. Two CLIs that have not pulled each other both compute the same next sequence under a shared parent and mint the same `node_id`. On sync the two `create_node` events share that `node_id` (distinct `event_id`s); the hub keeps both; `applyCreateNode` does `INSERT OR IGNORE`, so each replica keeps its own row and ignores the other's — permanent split-brain, no surfaced conflict. The dot-path is simultaneously (a) the identity events key on, (b) the human/agent/external reference, (c) a position encoding, and (d) locally assignable offline. No coordination-free scheme can keep all four; this ADR splits identity from the surface reference.

## 2. Identity model

Each node has two identifiers:

- **`display_path`** — the dot-notation ID (`PRJX-1.4`). The **only** identifier exposed to users, agents, CLI/MCP/REST output, and external systems (git, CI). Encodes tree position. **Mutable** (changes only under the controlled cases in §5).
- **`uid`** — a stable internal identity, defined as **the node's `create_node` event `event_id`** (UUIDv7). Unique by the event log's existing `event_id` primary key; **replica-consistent by construction** (the same create event has the same `event_id` everywhere). Used only for: internal node-to-node linkage, idempotent apply/dedup, and as the provisional-form path segment. **Never surfaced for a settled node.**

Defining `uid` as the create-event id (rather than a freshly minted UUID) is load-bearing: it makes the UID globally unique with no new constraint and identical across replicas without coordination, which the migration depends on (§7).

## 3. Surface vs. internal (the rejected over-rotation)

- **Surface (humans, agents, git, CI, all APIs): `display_path` only.** An agent calls `mtix context PRJX-1.4.3`; a commit says `PRJX-1.4.3`. The `uid` is never required, displayed, or referenced externally for a settled node. This preserves mtix's core value (clean, hierarchical, human-readable IDs / the briefing chain).
- **Internal (event payloads, node-to-node links, dedup): `uid`.** Events reference the node by `uid`; `display_path` is a stored, derivable attribute. Consequence: **renumbering a node rewrites a display attribute and recomputes descendants' display paths, and touches zero events** — no cross-replica event remap, which removes the convergence hazard the earlier "events key on dot-path" option carried.

Keying events on `uid` internally does **not** force the surface to expose `uid`: surface reads take a `display_path`, resolve `display_path → uid → node`. The two are independent; only the surface decision (dot-path) is user-visible.

## 4. Creation, claim, provisional vs. settled

- **Settled node:** `display_path` is fully numeric (clean), with the trailing sequence assigned by the hub registry (§6). A node is settled once it and all ancestors have a hub-confirmed number.
- **Provisional node:** created offline or before claim confirmation. Its trailing segment under the highest unsettled ancestor is the `uid` (rendered hyphenless to avoid grammar clashes), e.g. `PRJX-1.1.<uid>`; descendants created offline are sequential under it (`PRJX-1.1.<uid>.1`) and globally unique because the ancestor segment is the UID. Provisional form is **collision-free by construction** and **visibly provisional**.
- **Claim protocol:** on create, a background claim requests the next free sequence under the parent from the registry. Online: confirmed in ~1 hub-RTT (seconds on a Neon cold start) → settled; if the number was taken, retry → next free. Offline / claim unavailable: node stays provisional and re-attempts the claim on the next sync. Claiming is the same mechanism online and offline (one path, two latencies).
- **Invariant:** a provisional node's events MUST NOT be considered "settled-referenceable"; the claim-confirm for a node MUST happen-before that node's first push as settled.

## 5. Reference resolution and renumber

- External/agent reference (`display_path`) resolves `display_path → uid → node`. Internal links are stored as `uid`, so they survive any renumber.
- **A clean (fully numeric) `display_path` is stable**: it can only change if an ancestor renumbers, and a node cannot have a clean child under an unsettled ancestor (the child's path would contain the ancestor's UID). So "fully clean" ⇒ "all ancestors settled" ⇒ stable. External references therefore use clean dot-paths and stay valid in normal operation.
- **Renumber is atomic (audit F-2)**: a renumber of node N rewrites N's `display_path` and recomputes the **entire subtree** — all descendants at any depth, recursively — within a **single transaction**. No external read (context, export, list) may observe N's new number together with a descendant's old number, or vice versa: the subtree is consistent before commit. `display_path → uid` resolution MUST never observe a number bound to two nodes mid-operation.

## 6. Collision handling

- **Online (steady state):** the registry is a derived **partial unique index** `UNIQUE(project_prefix, display_path) WHERE op_type='create_node'` over the append-only event log — no separate authoritative table. First `create_node` for a number wins; a second is rejected at push with a "renumber required" outcome (the claimer retries the next free number). It is a **liveness mechanism, not a security boundary** (§9).
- **Settled-vs-settled (only reachable via a hub restore — §6.1):** detect, **fail loud, block the affected node, require admin resolution** (Decision: Option B).
- **Offline / export-import (no arbiter):** renumber incoming provisional nodes deterministically, emit a `uid`-keyed remap file (`uid → new display_path`) and a loud report; **apply to a live store only with explicit confirmation**, never silently. **Import-boundary UID validation (audit F-3):** UID uniqueness is guaranteed by construction only over the hub path (the event-log `event_id` PK); at `import` it is NOT. Before applying any imported node, validate each incoming `uid` against the local store — identical `uid`+`display_path` is an idempotent no-op; a `uid` that duplicates an existing node with a *different* `display_path` is rejected (or, with explicit `--force-rename`, the colliding import node is re-stamped with a locally-minted UID) and reported loudly. A buggy or crafted export MUST NOT silently produce two nodes sharing a UID.

### 6.1 The restore case (Decision: Option B)

The only way two **settled** nodes share a number is a single hub handing the number out twice, which requires it to forget the first grant — i.e., a restore (or equivalent state loss). Precise conditions, all required: a single hub; a grant made *after* the last backup and lost by the restore; and a partner node that had *not* settled a clean number before the crash (provisional/offline), which then settles into the freed number after restore. Worked example: backup has `1.1–1.3` (next free `1.4`); A (online) is granted `1.4`; hub restored to before that grant; B (offline, provisional) reconnects and is settled into `1.4`; A returns and pushes its long-held `1.4` → two settled `1.4`s.

Detection signal: two distinct `uid`s hold a legitimate claim to the same `display_path`, with the earlier claim absent from the hub's surviving registry (the fingerprint of a lost grant).

**Resolution (Option B):** the system MUST NOT auto-pick or silently overwrite. **Block scope (audit F-1):** detection happens at hub push validation; the affected node's events are queued (its push returns a structured `collision/option-b` error) while **events for every other node continue to push and pull normally** — a single unresolved collision MUST NOT wedge the team's sync stream. It surfaces both nodes (content, claim age, time-on-hub) and prompts the admin to choose which keeps `1.4` and which renumbers to the next free, **defaulting the suggestion to the older claim — advisory only (audit F-5):** claim timestamps are client-asserted, and in a restore the hub's own arrival timestamp for the lost grant is gone, so no single field is authoritative here; present all available signals and let the admin decide, never auto-resolve on a timestamp. The prompt MUST state: *no nodes are lost; you are choosing which keeps the number and which moves; the moved node may have external references that need updating.* Auto-resolution is rejected because which side should keep the number (the one with more external references) is a human judgment; "make everyone reference UIDs" is rejected because it destroys the product.

Near-misses that MUST NOT trigger this: two online users with distinct numbers + restore (each re-asserts its own); two offline provisionals + restore (serialize to distinct numbers); backup taken after the grant (hub still knows the number is taken). Online creation with connectivity self-heals via retry (the partner would have been given `1.5` and never sat provisional).

## 7. Migration

- **Phase 0 — UID backfill (deterministic).** Local schema bump, auto-migrated after the NFR-2.8 open-time integrity gates and the automatic pre-mutation backup, per-step transaction. For each existing node, `uid = (its create_node event_id)` — replica-consistent, so the same node gets the same UID on every machine (a fresh random UID per machine would silently re-create the split-brain — this is mandatory). Existing `display_path`s are unchanged. Nodes with no recoverable create event (pre-sync/imported) get a locally-assigned UID; safe because such data was never shared. Exports and the tasks.json mirror carry `uid` from here on so re-imports stay consistent.
- **Phase 1 — hub pre-constraint dedup sweep.** The partial unique index cannot be added to a log already containing duplicate `(project_prefix, display_path)` create events (projects already bitten by the bug). Under the hub's existing PG advisory-lock single-flight: scan, resolve duplicates by the deterministic tiebreak, renumber losers, emit `uid`-keyed remaps, surface loudly via `sync_conflicts`. Idempotent; a no-op for clean projects. Only then add the index.
- **Phase 1.5 — version-gate the index (audit F-4).** Adding the partial unique index is itself gated: it is created only once every `sync_projects.last_seen_cli_version` is at or above the minimum version that understands renumber/remap events. Until then the renumber is emitted as a remap event (`node_renumbered`) that older CLIs **ignore gracefully** (not error), and Phase 2 dual-resolution keeps them resolving nodes by either old or new id. Adding the index before all CLIs are remap-aware would hard-error or silently diverge old CLIs that push a now-renumbered number — so it is deferred behind the gate, with a loud pre-add report.
- **Phase 2 — dual resolution.** CLIs resolve references by `display_path` and `uid`. Pre-upgrade external references (dot-path only) keep working because pre-existing settled nodes' paths don't move.
- **Phase 3 — events key on UID.** Protocol-major bump. Transitional events carry both `node_id` (display_path) and `uid`; the hub translates. A project cuts over to UID-keyed events only when all its CLIs report a compatible version (existing `sync_projects.last_seen_cli_version` gate); the hub refuses cutover until then. After cutover, `display_path` is a derived display attribute and renumber touches no events.

## 8. Agent-facing semantics (so agents implement and operate correctly)

- Agents reference nodes by `display_path` exclusively. They never need, generate, or store a `uid`.
- A `display_path` containing a UID segment is **provisional**: it is valid and resolvable, but its eventual settled number will differ. Agents MUST NOT embed a provisional ID in an immutable external artifact (git commit, PR body); tooling SHOULD warn when a provisional ID is about to be externalized (detectable from the ID shape alone).
- A fully-numeric `display_path` is safe to externalize.
- After a renumber (rare), an agent's previously-recorded `display_path` for a node may have moved; agents that persist references SHOULD re-resolve via `mtix` rather than assume permanence — the system resolves the underlying node correctly regardless.

## 9. Threat-model calibration (per `docs/SECURITY-MODEL.md`)

Sync mode assumes a mutually-trusted team (a shared hub is shared like a shared git repo); adversarial teams and hub/PG-provider compromise are out of scope. Against that contract:

- **UID uniqueness is guaranteed by construction** (it is the create event's `event_id`, already unique by PK; a duplicate push is dropped as idempotent replay), so it needs no extra enforcement and cannot be forged into a duplicate over the hub path.
- The registry referee is **liveness, not a security boundary**: a broken/hostile hub can at worst force a renumber; it cannot lose or corrupt a node (each CLI keeps its canonical local store).
- **Remap files are advisory and applied only with confirmation**; the append-only event log is canonical.
- A `uid` is an **identifier, not a secret/capability**; nothing may treat knowing it as authorization.

## 10. Decisions and the one resolved fork

- Identity: dot-path on the surface; `uid = create_node event_id` underneath. **(Decided.)**
- Internal linkage: events key on `uid` (formerly "Tier B"); renumber rewrites a display attribute only. **(Decided — this is the design, not an option; "Tier A / events key on dot-path" is rejected for its unproven cross-replica remap convergence.)**
- Restore-induced settled-vs-settled collision: **Option B** (block affected node, admin resolves, older-claim default, no data loss). **(Decided.)**
- Offline/import renumber: confirmed, not auto, with a `uid`-keyed remap file. **(Decided.)**

## 11. Consequences

**Positive:** concurrent/offline creation is collision-free; the surface stays clean dot-notation; references survive renumbering via the UID underneath; restore degrades to a cosmetic, human-resolved relabel instead of silent loss; restore-safety reduces to the event-log durability already built (NFR-2.8).

**Accepted residual:** a settled dot-path can move in the restore case (§6.1), which can break an external git reference to it; accepted as rare, surfaced (Option B), and far preferable to exposing UIDs on the surface.

## 12. Test scenarios (must become tests, incl. end-to-end against a real hub — extend `e2e/sync_collision_test.go`)

1. Two offline creates under the same parent → provisional forms, no clash; after sync both settle to distinct numbers; both nodes intact.
2. Two online creates under the same parent → referee serializes to `1.4`/`1.5`; retry-on-taken works; no provisional form.
3. Online create settles within budget; offline create is visibly provisional, cannot clash, settles on reconnect.
4. Child under a provisional parent carries the parent's UID segment; parent settles → subtree renumbers; all internal links and references still resolve.
5. Reference stability: clean settled path doesn't change in normal operation; external reference resolves; reference to a provisional (UID-bearing) form resolves via UID.
6. Renumber loses no data: both nodes survive, internal links resolve, only the display number moved; renumber is atomic (no mid-operation ambiguous resolution).
7. **Restore case (Option B), full §6.1 scenario:** detect, fail loud, block only the affected node, prompt admin with older-claim default; no node lost; after admin choice one is `1.4`, other `1.5`, both intact.
8. **Restore near-misses that MUST NOT trigger Option B:** two online distinct numbers + restore; two offline provisionals + restore; backup taken after the grant.
9. Connectivity retry: online claim of a taken number retries to next free.
10. Export/import with no hub: clashes detected, incoming provisionals renumbered, remap file emitted, confirmation required before touching the importer's existing nodes; references resolve via UID.
11. Common-case restore self-heals: hub restored, clients re-push, log reconverges, nothing lost (only §6.1 needs a human).
12. **Migration determinism (mandatory assertion):** the same node gets the *same* UID on two machines after Phase 0 (UID = create event_id), existing numbers unchanged; Phase 1 dedup sweep is loud, deterministic, single-flight-locked, idempotent, and a no-op when clean.
13. Agent semantics: a provisional ID is flagged before externalization; resolution by display_path is correct before and after a renumber.
14. Import stamp validation: an import whose incoming UID duplicates an existing node's UID is detected and rejected/renumbered, not blindly linked (defense at the import boundary, where uniqueness-by-construction does not hold).

## 13. Implementation notes

- `uid` = the node's `create_node` event `event_id`; do not mint a separate identifier.
- Store `display_path` as a derived/stored attribute keyed by `uid`; renumber updates it transactionally with subtree recomputation.
- The registry is the derived partial unique index over the append-only log; restore-safety = log durability (no separate authoritative table to protect).
- Provisional display segment = hyphenless rendering of the UID; show a short friendly form, store the full UID.
- Option B blocking is scoped to the affected node, not the whole sync stream.

## 14. Security & safety audit (design, pre-implementation)

Red-teamed against `docs/SECURITY-MODEL.md` (mutually-trusted team; adversarial team and hub/PG-provider compromise out of scope; an insider with hub write access is already trusted to create/modify nodes — accepted residual T4).

**Resolved by this design** (were serious in earlier drafts): forgeable/duplicate UID and non-deterministic migration UID (both gone because `uid = create_node event_id`, PK-unique and replica-consistent); silent split-brain on concurrent create (provisional forms are collision-free by construction; the registry serializes settling); cross-replica event-remap convergence hazard (events key on UID, so renumber rewrites a display attribute and touches no events).

**Findings folded in (all pre-implementation spec fixes):**

| # | Finding | Severity (trust-calibrated) | Where addressed |
|---|---|---|---|
| F-1 | Option B block scope must not wedge the whole sync stream | MEDIUM (safety/availability) | §6.1 — affected node queued, all others sync |
| F-2 | Renumber must atomically recompute the whole subtree | MEDIUM (consistency) | §5 |
| F-3 | Import boundary must validate incoming UID uniqueness (not guaranteed by construction off the hub path) | MEDIUM (integrity) | §6 offline/import |
| F-4 | Version-gate the uniqueness index, not just the Phase-3 cutover | MEDIUM (migration-safety) | §7 Phase 1.5 |
| F-5 | Option B "older-claim" default rests on forgeable/lost timestamps — keep advisory, never auto-resolve | LOW (robustness) | §6.1 |
| F-6 | An externalized provisional id leaks a creation timestamp (UUIDv7) | LOW (hygiene) | §8 (don't externalize) + §13 (short-form render) |

**Verdict:** no HIGH security exposure under mtix's trusted-team contract; the design resolves the prior serious findings; the remaining items are bounded MEDIUM/LOW spec clarifications, now incorporated. Design is lock-ready pending implementation.

## 15. Addendum A — restore-collision discriminator (supersedes the §6.1 trigger)

The §6.1 restore-collision (Option B) needs a way to tell a genuine restore re-grant from an ordinary concurrent-create race (which §6 / MTIX-30.7 already auto-resolves by renumbering the loser). The first implementation (MTIX-30.8, rejected in review) used **UID age** (older incoming claim ⇒ restore). That is invalid: in a normal race the loser is older ~50% of the time, so it mis-classified ordinary collisions as restores (it regressed `TestRegistry_ConcurrentPushesSameNumber`). UID-age cannot distinguish the two.

A **client "previously-settled" flag** was considered and rejected on a security review (raised by the maintainer): it puts a **forgeable, client-asserted signal on the trigger of a safety-critical path**. A compromised client (even under the trusted-team model, this is poor hygiene) could set it to fabricate restore-collisions — escalating ordinary creates into the admin-resolution queue (availability nuisance, blocked nodes) and, at worst, social-engineering an admin into renumbering a legitimate ticket (recoverable — uid is stable, no data lost — but it breaks external refs and wastes trust). Bounded by Option B's human gate (no auto-pick, F-5), but avoidable.

**Decision: a hub restore-epoch, advanced only by the operator.**
- The hub keeps a monotonic `restore_epoch` (starts 0), advanced ONLY by an explicit out-of-band operator action — `mtix sync mark-restored`, a documented step in the restore-from-backup runbook. Clients cannot advance it.
- Each `create_node`, when the hub registry accepts it, is hub-stamped with the current `restore_epoch` (hub-side, at acceptance — never client-asserted).
- A settled-vs-settled collision is classified as a RESTORE collision (→ Option B, §6.1) **only within a restore window** — the contested number is held by a create stamped in the current epoch while the incoming belongs to an earlier era. Outside a restore window (normal operation, no operator bump) every collision takes the ordinary renumber path (§6, MTIX-30.7); Option B is not reachable.

**Why this is the trust-minimizing choice:**
- The un-forgeable element is the **operator's epoch bump** — a deliberate, supervised action a client cannot manufacture. A compromised client therefore **cannot trigger Option B during normal operation**: with no restore, there is no epoch advance and the Option-B path is closed. The attack window shrinks to the operator-supervised post-restore interval.
- It **eliminates the normal-race false-positive by construction**: same-epoch concurrent creates always renumber; only cross-epoch (post-restore re-grant) collisions reach Option B — what the rejected UID-age trigger could not do.

**Unchanged:** resolution stays Option B — human-gated, no auto-pick, the older-claim default ADVISORY only (F-5); the loser renumbers via `Store.RenumberSubtree` (§5); no create event is ever deleted, so no node is lost. Block scope stays per-node (F-1).

**Scope (MTIX-30.8 v1): full** — epoch-gated detection PLUS the admin-resolve CLI (list open collisions, pick the winner, renumber the loser). The renumber primitive (§5) already exists, so the CLI is thin.
