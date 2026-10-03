-- MTIX-15.2 hub schema (file 005 of 7).
-- Executed by MTIX-15.3 transport under PG advisory-lock single-flight.
-- DO NOT run manually.
--
-- audit_log is an append-only table (FR-18.5). mtix itself writes no
-- audit_log row: a hub mutation is recorded in sync_events (and
-- sync_conflicts), not here, so the table stays empty unless an operator
-- inserts into it. The audit_log_immutable trigger in 006 and the
-- TRUNCATE guard in 016 refuse UPDATE, DELETE and TRUNCATE on it; the
-- table owner and a superuser can drop or disable those triggers.

CREATE TABLE IF NOT EXISTS audit_log (
    audit_id        BIGSERIAL PRIMARY KEY,
    project_prefix  TEXT NOT NULL,
    actor           TEXT NOT NULL,
    action          TEXT NOT NULL,
    target_node_id  TEXT,
    payload         JSONB,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_audit_log_project_time
    ON audit_log (project_prefix, created_at);
