-- MTIX-30.14 hub schema (file 008).
-- Executed by the transport under PG advisory-lock single-flight,
-- auto-applied in lexical order via migrations.Files(). DO NOT run manually.
--
-- sync_project_clients is the real version-negotiation gate backing
-- ADR-003 §7 Phase 1.5/3 ("a project cuts over only when all its CLIs
-- report a compatible version"). The pre-existing
-- sync_projects.last_seen_cli_version column is a single last-writer
-- value and CANNOT express "EVERY CLI is compatible"; this per-client
-- table can. One row per (project, machine), upserted on sync init and
-- on every push with the calling CLI's build version and last-seen time.
--
-- ProjectAllClientsAtLeast(...) reads MIN(cli_version) across the active
-- rows for a project (active = last_seen_at within a window, see the Go
-- transport) to decide whether the gate is open.
--
-- Per ADR-003 §9 / docs/SECURITY-MODEL.md this is a liveness mechanism,
-- not a security boundary: the gate can only hold a migration back, it
-- never loses or corrupts a node.

CREATE TABLE IF NOT EXISTS sync_project_clients (
    project_prefix  TEXT        NOT NULL,
    machine_hash    TEXT        NOT NULL,
    cli_version     TEXT        NOT NULL,
    last_seen_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (project_prefix, machine_hash)
);

-- Supports the gate's per-project scan (filter by prefix, then aggregate
-- over cli_version / last_seen_at).
CREATE INDEX IF NOT EXISTS sync_project_clients_prefix_seen_idx
    ON sync_project_clients (project_prefix, last_seen_at);
