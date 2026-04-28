-- MTIX-15.2 hub schema (file 001 of 7).
-- Executed by MTIX-15.3 transport under PG advisory-lock single-flight.
-- DO NOT run manually — apply via the mtix CLI (mtix sync init).
--
-- sync_events is the hub's canonical replication log per FR-18.6 /
-- SYNC-DESIGN section 3.1. The local SQLite mirror has the same shape.

CREATE TABLE IF NOT EXISTS sync_events (
    event_id            TEXT PRIMARY KEY,
    project_prefix      TEXT NOT NULL,
    node_id             TEXT NOT NULL,
    op_type             TEXT NOT NULL CHECK (op_type IN (
        'create_node','update_field','transition_status',
        'claim','unclaim','defer',
        'comment','link_dep','unlink_dep',
        'delete','set_acceptance','set_prompt'
    )),
    payload             JSONB NOT NULL,
    wall_clock_ts       BIGINT NOT NULL,
    lamport_clock       BIGINT NOT NULL,
    vector_clock        JSONB NOT NULL,
    author_id           TEXT NOT NULL,
    author_machine_hash TEXT NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- retained_until is reserved for v2 compaction per FR-18.26.
    -- v1 leaves it NULL on every insert; the column is here so v2 can
    -- backfill without a destructive migration.
    retained_until      TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_sync_events_project_lamport
    ON sync_events (project_prefix, lamport_clock);
CREATE INDEX IF NOT EXISTS idx_sync_events_node
    ON sync_events (node_id);
CREATE INDEX IF NOT EXISTS idx_sync_events_retained
    ON sync_events (retained_until)
    WHERE retained_until IS NOT NULL;
