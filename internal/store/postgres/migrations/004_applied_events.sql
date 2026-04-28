-- MTIX-15.2 hub schema (file 004 of 7).
-- Executed by MTIX-15.3 transport under PG advisory-lock single-flight.
-- DO NOT run manually.
--
-- applied_events is the FR-18.9 idempotent dedupe table. The hub apply
-- engine (MTIX-15.4) checks here before applying any event so duplicates
-- are no-ops. The local SQLite mirror has the same shape (created by
-- MTIX-15.2.2's schema migration).

CREATE TABLE IF NOT EXISTS applied_events (
    event_id           TEXT PRIMARY KEY,
    applied_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    applied_by_lamport BIGINT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_applied_events_lamport
    ON applied_events (applied_by_lamport);
