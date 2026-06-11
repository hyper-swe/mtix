# ADR-002: Redundant Storage Layer — Event Journal and Content-Addressed Bodies

**Status:** Accepted
**Decision:** Do NOT add a local append-only event journal or content-addressed body store now. The shipped redundancy stack (NFR-2.8) is the architecture of record; revisit triggers are defined below.
**Context ticket:** MTIX-26.9, from the 2026-05-19 data-loss incident RCA (docs/incidents/2026-05-19-sqlite-corruption-rca.md).

---

## 1. Context

The incident RCA proposed two additional redundancy mechanisms beyond what has since shipped:

- **(a) Append-only event journal** — every mutation appends a JSON line to `.mtix/events.jsonl`; the SQLite database becomes a derived index, reconstructible by replay.
- **(b) Content-addressed body storage** — prompts/descriptions live as `.mtix/objects/<sha>.txt` with the database holding pointers, so payloads survive catalog loss as plain text.

Both were proposed when mtix had **zero** automatic redundancy. That is no longer the state of the world. The NFR-2.8 stack now provides, on every interface:

| Layer | Mechanism | Failure domain it covers |
|---|---|---|
| Refusal | free-space pre-flight, fail-stop latch, integrity gates at open | stops corruption from happening or spreading |
| Mirror | tasks.json auto-export on every committed transaction (CLI PostRun + on-commit debouncer for MCP/serve/daemon), atomic temp+rename | full logical copy, seconds-fresh, different file + code path than SQLite |
| Snapshots | automatic rolling verified backups (`VACUUM INTO` + quick_check, daily, keep 7) | point-in-time copies, survive single-file damage |
| Salvage | `mtix recover` (per-row reads, mirror merge, placeholder parents), `import --recompute-checksum` | extraction when the above still wasn't enough |
| Evidence | fault-injection suite gating every CI build; traceability matrix | keeps all of the above true over time |

The question for this ADR: do (a) and/or (b) add enough marginal protection over that stack to justify their costs?

## 2. Option (a): local append-only event journal

**Marginal protection.** The journal's unique property versus the mirror is *history*: the mirror is last-state-wins, a journal is every-state-ever. For pure durability of current state it adds little — the mirror is already written per mutation from a different code path. Its real value is point-in-time reconstruction and audit-grade history.

**Costs.**
- Write amplification: one fsync'd append per mutation on top of the WAL commit and the debounced mirror write — on the same volume, in the same failure domain as everything else local. On a full disk (the incident's trigger) the journal fails exactly when the database does.
- A third crash-consistency surface: torn JSON lines, partial appends, rotation/compaction GC — each needing the same fault-injection rigor as the store, or the journal becomes the thing that lies during recovery.
- Replay machinery is a parallel import implementation that must stay semantically identical to the live mutation path forever (schema migrations included), or replay silently diverges.

**The decisive observation:** mtix already *has* an event journal — the FR-18 sync layer emits an ordered, hash-chained event stream (`sync_events`) consumed by the BYO Postgres hub. A hub-connected project gets event-sourced history **in a different failure domain** (another machine), which is strictly stronger than a journal on the same disk. Solo users get the same property cheaply by committing `tasks.json` to git (documented in the recovery runbook), which turns git history into the journal.

**Decision:** rejected for the local filesystem. The durable-history need is met by (sync hub) ∪ (git-committed mirror), both off-volume.

## 3. Option (b): content-addressed body storage

**Marginal protection.** Bodies survive catalog shredding as readable text. In the incident, 33 rows were unreadable; their *titles and bodies* would have survived as objects.

**Costs.**
- It is a storage-engine change: every node read becomes catalog lookup + object read (two failure modes per read); writes need object-then-row ordering discipline; deletion needs reference-counted GC; `verify`/`export`/`import`/`recover`/sync all grow object awareness.
- It re-introduces the multi-file consistency problems SQLite was chosen to avoid (ADR-001) — now between SQLite and a homegrown object store.
- The protection is redundant with the mirror: tasks.json already contains every body in plain text, updated per mutation. Object storage only wins when the catalog *and* the mirror are both lost while objects survive — a corner the rolling backups also cover.

**Decision:** rejected. The mirror is the content-survival mechanism; it is simpler, already shipped, and human-readable.

## 4. Revisit triggers

Reopen this decision if any of these occur:

1. A field incident where data was lost *despite* the NFR-2.8 stack functioning as designed (not merely disabled).
2. The mirror becomes a bottleneck (projects large enough that per-mutation export is throttled to the point of staleness windows > minutes), weakening the content-survival argument in §3.
3. The sync layer gains a local-only mode where its event stream is persisted but no hub is configured — at that point a local journal is nearly free and §2's cost argument collapses.

## 5. Consequences

- No new storage surfaces; engineering effort stays on hardening the shipped stack (fault-injection breadth, recovery ergonomics).
- The recovery runbook's "commit tasks.json to git" guidance is the sanctioned answer for audit-grade history without a hub; USERMANUAL documents it.
- Hub-connected teams should treat the hub as their off-machine journal; that is an explicit property of the FR-18 design, not an accident.
