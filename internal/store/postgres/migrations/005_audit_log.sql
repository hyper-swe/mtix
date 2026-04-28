-- MTIX-15.2 hub schema (file 005 of 7).
-- Executed by MTIX-15.3 transport under PG advisory-lock single-flight.
-- DO NOT run manually.
--
-- audit_log is the append-only mutation record per FR-18.5. Every
-- mutation that lands on the hub also writes an audit_log row in the
-- same transaction. The audit_log_immutable trigger in 006 enforces
-- append-only at the trigger layer.

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
