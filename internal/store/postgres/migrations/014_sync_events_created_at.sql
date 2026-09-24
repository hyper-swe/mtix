-- MTIX-95.5 hub schema (file 014).
-- Executed by the transport under PG advisory-lock single-flight,
-- auto-applied in lexical order via migrations.Files(). DO NOT run manually;
-- the hub owner applies it with `mtix sync init`.
--
-- Index for the late-event sweep of `mtix sync pull` (ADR-006 D5).
--
-- Pull asks the hub for lamport_clock > cursor. An offline writer's later
-- push carries Lamport clocks below busier peers' cursors, so those peers
-- never receive it through the cursor. After its cursor loop, every pull
-- therefore lists the ids of the hub events created since its previous
-- sweep (hub time, minus a 15-minute overlap) and fetches the ones it does
-- not hold:
--
--     WHERE created_at >= $1 AND (created_at, event_id) > ($1, $2)
--     ORDER BY created_at, event_id
--
-- This index serves that query from the window's start instead of scanning
-- the whole log on every pull. Until it exists (a hub whose owner has not
-- yet re-run `mtix sync init` after upgrading) the sweep still works, with
-- one sequential scan of sync_events per pull.
--
-- Additive and idempotent: IF NOT EXISTS makes a re-run a no-op, and no
-- column, constraint or row is touched.

CREATE INDEX IF NOT EXISTS idx_sync_events_created_at
    ON sync_events (created_at);
