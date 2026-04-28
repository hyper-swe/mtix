-- MTIX-15.2 hub schema (file 003 of 7).
-- Executed by MTIX-15.3 transport under PG advisory-lock single-flight.
-- DO NOT run manually.
--
-- sync_projects is the divergent-history detection backbone per
-- FR-18.13 / SYNC-DESIGN section 10.1. One row per project; the first
-- writer creates it on mtix sync init and every subsequent CLI verifies
-- first_event_hash before clone or push.

CREATE TABLE IF NOT EXISTS sync_projects (
    project_prefix         TEXT PRIMARY KEY,
    first_event_hash       TEXT NOT NULL,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    schema_version         INTEGER NOT NULL DEFAULT 1,
    -- last_seen_cli_version supports the FR-18.14 advisory drift check
    -- exposed by mtix sync doctor in MTIX-15.7.
    last_seen_cli_version  TEXT
);
