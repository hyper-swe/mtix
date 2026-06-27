-- MTIX-30.10 hub schema (file 012).
-- Executed by the transport under PG advisory-lock single-flight,
-- auto-applied in lexical order via migrations.Files(). DO NOT run manually.
--
-- node_renumber_remaps is the durable, uid-keyed remap ledger produced by
-- the Phase 1 pre-constraint dedup sweep (ADR-003 §7 Phase 1). When a
-- project already contains duplicate (project_prefix, display_path)
-- create_node events — projects bitten by MTIX-28 before the partial
-- unique index existed — the sweep resolves the duplicate
-- DETERMINISTICALLY (first create / lowest event_id wins) and records the
-- LOSER's renumber here, keyed on the loser's stable uid (ADR-003 §2).
--
-- Why a SEPARATE ledger and not an UPDATE of the create row: the
-- sync_events log is APPEND-ONLY (ADR-003 §13) and has no UPDATE trigger;
-- the sweep MUST NOT rewrite existing create rows. Recording the remap in
-- its own table (a) keeps the log immutable, (b) gives older,
-- non-remap-aware CLIs something they ignore gracefully (they never read
-- this table), and (c) makes the sweep crash-resume safe: the deterministic
-- winner plus the recorded remap mean a re-run converges to the same state
-- without ever double-renumbering a loser (the uid PK rejects a duplicate).
--
-- The companion loud surfacing is a sync_conflicts row (resolution
-- 'manual', field_name 'display_path') written in the same transaction, so
-- operators see the renumber via the existing `mtix sync conflicts` path.
--
-- Per ADR-003 §9 / docs/SECURITY-MODEL.md the sweep is a LIVENESS
-- mechanism, not a security boundary: it can at worst move a display
-- number; it never loses or corrupts a node.

CREATE TABLE IF NOT EXISTS node_renumber_remaps (
    -- uid is the loser node's stable identity (its create_node event_id,
    -- ADR-003 §2). One renumber per node ⇒ uid is the PRIMARY KEY, which
    -- is exactly what makes a sweep re-run idempotent: re-recording the
    -- same loser is an ON CONFLICT DO NOTHING no-op.
    uid             TEXT        PRIMARY KEY,
    project_prefix  TEXT        NOT NULL,
    -- old_display_path is the contested number the loser tried to keep;
    -- new_display_path is empty until a CLI claims the next free number
    -- (the sweep records the renumber-REQUIRED fact; the actual new path
    -- is minted by the owning CLI on its next push, ADR-003 §6/§9).
    old_display_path TEXT       NOT NULL,
    new_display_path TEXT       NOT NULL DEFAULT '',
    -- loser_event_id / winner_event_id are the two contesting create_node
    -- events; winner kept the number (lowest event_id), loser renumbers.
    loser_event_id   TEXT       NOT NULL REFERENCES sync_events(event_id),
    winner_event_id  TEXT       NOT NULL REFERENCES sync_events(event_id),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Per-project scan support (sweep reports group remaps by project).
CREATE INDEX IF NOT EXISTS idx_node_renumber_remaps_project
    ON node_renumber_remaps (project_prefix);
