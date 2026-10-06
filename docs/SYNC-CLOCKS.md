# Sync clocks for embedding and tests

SQLite `Store.SetClock(func() time.Time)` provides the instance clock used for sync replay apply timestamps, including clone, pull, quarantine retry, relay ingest, own-event acknowledgement, and derived dependency changes. Call it before using the store; changing the clock concurrently with operations is unsupported. Passing nil restores the default UTC wall clock.

Use `Store.IdempotentApply(ctx, tx, event)` inside an existing transaction to replay with that store's clock. The existing package function `sqlite.IdempotentApply(ctx, tx, event)` remains compatible and uses the default wall clock. No transaction registry or global clock is involved.

Transport `Pool.SetClock(func() time.Time)` sets the reference time for push validation. Configure it once before pushes; concurrent changes are unsupported. Nil restores the wall clock. Nil and zero pools still validate nonempty batches before reporting that the pool is not open; empty batches succeed.

An injected clock changes apply-time bookkeeping (`applied_at`, mirrored `created_at`, conflict `resolved_at`, and replay `updated_at` where it uses apply time). An event's authored `wall_clock_ts` still determines comment time and terminal workflow time when representable, and still participates in the original LWW tie-break. Unrepresentable event times use the injected apply-time fallback. Lamport clocks, vector clocks, deduplication, validation limits, quarantine savepoints and event ordering retain their existing semantics.

These are embedding and deterministic-test seams. CLI users do not need new flags or configuration.
