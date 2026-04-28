-- MTIX-15.2 hub schema (file 002 of 7).
-- Executed by MTIX-15.3 transport under PG advisory-lock single-flight.
-- DO NOT run manually.
--
-- sync_conflicts records every non-trivial LWW resolution that dropped a
-- value (SYNC-DESIGN section 8.3 / FR-18.12). Append-only; the
-- audit_log_immutable trigger in 006 enforces this.

CREATE TABLE IF NOT EXISTS sync_conflicts (
    conflict_id  BIGSERIAL PRIMARY KEY,
    event_id_a   TEXT NOT NULL REFERENCES sync_events(event_id),
    event_id_b   TEXT NOT NULL REFERENCES sync_events(event_id),
    node_id      TEXT NOT NULL,
    field_name   TEXT NOT NULL,
    resolution   TEXT NOT NULL CHECK (resolution IN ('lww','tombstone','manual')),
    resolved_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_by  TEXT
);

CREATE INDEX IF NOT EXISTS idx_sync_conflicts_node
    ON sync_conflicts (node_id);
CREATE INDEX IF NOT EXISTS idx_sync_conflicts_resolved_at
    ON sync_conflicts (resolved_at);
