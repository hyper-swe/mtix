# Deferred-Finding Dispositions — pre-release review (MTIX-22.2)

Per docs/RELEASE-CHECKLIST.md §1: every finding deferred since the last
tag is re-reviewed here before the next release. Source: the four items
deferred from the independent post-MTIX-15.10 review.

| # | Finding | Severity | Disposition |
|---|---------|----------|-------------|
| 1 | VC-collision audit-visibility gap: CLIs sharing the default `author_id="cli"` emit equal vector clocks, so hub-side `sync_conflicts` is never populated for those pairs (convergence unaffected — LWW still orders by lamport/wall-clock/machine-hash) | MEDIUM (doc-only) | **FIXED.** Documented in `docs/SYNC-DESIGN.md` §8.3 as an intentional design tradeoff with an explicit do-not-"fix" warning (re-keying VCs by machine hash would change LWW semantics). Operational mitigation (distinct author IDs per CLI) cross-referenced to MTIX-24, which remains open for surfacing `--author-id`. |
| 2 | Hub pool MaxConns 8→5 | NONE | **CLEAN — no action.** Already shipped during MTIX-15; 10 devs × 5 conns = 50 sits comfortably within managed-Postgres connection defaults. Recorded here so the deferral trail closes. |
| 3 | Test-harness drift: sync benchmark ignored the `conflicts` return from `PushEvents` and uses batch size 500 vs production's `pushBatchSize=100` | LOW | **FIXED (assert) / DOCUMENTED (batch size).** `benchmarks/sync_throughput_test.go` now requires `conflicts` empty (synthetic single-author data must never conflict; nonzero means harness or hub semantics drifted). The 500-event batch size is retained deliberately — the benchmark measures hub throughput ceilings, not CLI batching policy — with an inline comment explaining the divergence and when to rerun at 100. The e2e harness (`e2e/sync_e2e_test.go`) already captures and returns conflict counts; no change needed there. |
| 4 | 100K-node memory test asserts HeapAlloc < 50 MB after 2×GC; flaky-CI risk under GC pressure | MEDIUM (CI flakiness risk) | **ACCEPTED — keep 50 MB.** Per the original review guidance: do not preemptively loosen; the ceiling is met comfortably on dev hardware and the test is already `-short`-skipped. Trigger to act: the first false failure in CI → loosen to 75 MB with a comment naming GC pressure as the cause. No such failure has occurred to date. |

Review outcome: all four findings either fixed or dispositioned with a
named revisit trigger. Nothing carries silently across the release
boundary.
