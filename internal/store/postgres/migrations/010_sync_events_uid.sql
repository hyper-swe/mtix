-- MTIX-30.6 hub schema (file 010).
-- Executed by the transport under PG advisory-lock single-flight,
-- auto-applied in lexical order via migrations.Files(). DO NOT run manually.
--
-- Adds the durable node-identity column `uid` to the hub's sync_events log
-- for the DUAL-CARRY transition (ADR-003 §3, §7 Phase 3). Events carry the
-- target node's uid alongside node_id (the dot-notation display_path) so a
-- future renumber rewrites a display attribute and touches ZERO events
-- (ADR-003 §3, §10): the uid is stable, node_id moves.
--
-- For a create_node event the uid equals the event's own event_id
-- (self-anchor, ADR-003 §2); for every other op it is the target node's
-- uid. The column is NULLABLE on purpose: an older, pre-30.6 CLI pushes
-- events with no uid, and apply falls back to node_id for those (ADR-003
-- §7 Phase 3). A project only switches to uid-AUTHORITATIVE keying once
-- every active client is at/above sync.UIDKeyedMinVersion (the existing
-- sync_project_clients version gate) — this column merely makes the
-- transition data available; it does not force cutover.
--
-- ADD COLUMN IF NOT EXISTS keeps the migration idempotent for hubs that
-- already ran it. No backfill of historical rows is performed: legacy rows
-- keep uid NULL and resolve by node_id, so there is no data motion.

ALTER TABLE sync_events ADD COLUMN IF NOT EXISTS uid TEXT;

-- Lookup support for uid-keyed resolution (ADR-003 §3). Partial: only
-- uid-bearing rows participate, mirroring the SQLite mirror index.
CREATE INDEX IF NOT EXISTS idx_sync_events_uid
    ON sync_events (uid)
    WHERE uid IS NOT NULL AND uid <> '';
