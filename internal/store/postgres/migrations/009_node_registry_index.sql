-- MTIX-30.4 hub schema (file 009).
-- Executed by the transport under PG advisory-lock single-flight,
-- auto-applied in lexical order via migrations.Files(). DO NOT run manually.
--
-- The node-number REGISTRY (ADR-003 §6) is a DERIVED partial unique index
-- over the append-only sync_events log — NOT a separate authoritative
-- table. There is therefore no extra state to protect on restore:
-- restore-safety reduces to event-log durability (ADR-003 §11, §13).
--
-- It enforces "first create_node for a (project_prefix, display_path)
-- wins": a second, distinct create event for an already-registered number
-- is rejected at push and surfaced as a renumber-required outcome
-- (push_pull.go), so the claimer retries the next free number. Per
-- ADR-003 §9 / docs/SECURITY-MODEL.md this is a LIVENESS mechanism, not a
-- security boundary: it can at worst force a renumber; it can never lose
-- or corrupt a node (each CLI keeps its canonical local store).
--
-- node_id holds the dot-notation display_path (SYNC-DESIGN §3.1); the
-- index key is (project_prefix, node_id). The WHERE clause makes the
-- index PARTIAL — it constrains create_node rows only. Non-create events
-- (update_field, transition_status, comment, ...) legitimately repeat a
-- node_id and MUST NOT be constrained.
--
-- MIGRATION ORDERING (ADR-003 §7, mandatory): this index CANNOT be added
-- to a log that already contains duplicate (project_prefix, node_id)
-- create events — projects already bitten by MTIX-28. Phase 1 (the
-- pre-constraint dedup sweep) MUST run and resolve any such duplicates
-- BEFORE this index is created; otherwise this statement hard-errors with
-- a unique violation. Phase 1.5 additionally version-gates the add so
-- older, non-remap-aware CLIs cannot diverge. CREATE UNIQUE INDEX IF NOT
-- EXISTS keeps the migration itself idempotent for clean projects.

CREATE UNIQUE INDEX IF NOT EXISTS sync_events_node_registry_uidx
    ON sync_events (project_prefix, node_id)
    WHERE op_type = 'create_node';
