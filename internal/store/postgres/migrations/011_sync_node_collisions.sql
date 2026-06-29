-- MTIX-30.8 hub schema (file 011).
-- Executed by the transport under PG advisory-lock single-flight,
-- auto-applied in lexical order via migrations.Files(). DO NOT run manually.
--
-- sync_node_collisions is the durable queue of RESTORE collisions
-- (ADR-003 §6.1, Addendum A §15) — the only settled-vs-settled number
-- collision that must NOT auto-renumber. It is detected at hub push
-- validation (transport/registry.go) and recorded here so an operator can
-- resolve it with `mtix sync collisions list|resolve`.
--
-- WHY A COLLISION IS NOT A RENUMBER (ADR-003 §15): an ordinary concurrent
-- create race auto-resolves by renumbering the loser (first-writer-wins,
-- §6 / MTIX-30.7). A RESTORE collision is the rare case where a hub handed
-- a number out twice across a restore boundary; which node keeps the number
-- is a HUMAN judgment (it may have external references), so the affected
-- node's create is BLOCKED (queued here) rather than silently renumbered
-- (Option B). Block scope is per-node (audit F-1): every OTHER event in the
-- same push still lands; one collision never wedges the team's sync stream.
--
-- DETECTION IS EPOCH-GATED (ADR-003 §15, the un-forgeable gate): a collision
-- is recorded here ONLY when the number is held by a create stamped in an
-- epoch EARLIER than the current hub restore_epoch (the two contesting
-- creates straddle an operator restore-bump — a cross-epoch re-grant).
-- Same-epoch races never reach this table; they renumber (§6). The epoch is
-- advanced ONLY by the operator (`mtix sync mark-restored`), so a client
-- cannot manufacture a row here during normal operation (§15 threat model).
--
-- NO NODE IS LOST (ADR-003 §9): the blocked incoming create still lives in
-- the pusher's canonical local store; this table only records the contest.
-- The older-claim default is ADVISORY only (audit F-5) — claim timestamps
-- are client-asserted and partly lost on restore, so resolution never
-- auto-picks on a timestamp.

CREATE TABLE IF NOT EXISTS sync_node_collisions (
    collision_id    BIGSERIAL   PRIMARY KEY,
    project_prefix  TEXT        NOT NULL,
    -- display_path is the contested number both creates claim (SYNC-DESIGN §3.1).
    display_path    TEXT        NOT NULL,

    -- HELD side: the create_node that currently holds the number on the hub.
    -- It survived the restore and is stamped in an EARLIER epoch (the
    -- cross-epoch fingerprint). It IS on the hub, so it FKs sync_events.
    held_event_id       TEXT    NOT NULL REFERENCES sync_events(event_id),
    held_uid            TEXT    NOT NULL,
    held_epoch          BIGINT  NOT NULL,
    held_wall_clock_ts  BIGINT  NOT NULL,

    -- INCOMING side: the BLOCKED create. It is NOT inserted into sync_events
    -- (block scope, F-1), so it intentionally has NO foreign key — the row
    -- records the contest, the node itself stays in the pusher's local store.
    incoming_event_id       TEXT    NOT NULL,
    incoming_uid            TEXT    NOT NULL,
    incoming_wall_clock_ts  BIGINT  NOT NULL,

    -- detected_epoch is the current restore_epoch at detection time; it is
    -- strictly greater than held_epoch (the cross-epoch gate, §15).
    detected_epoch  BIGINT      NOT NULL,

    -- Resolution (Option B, human-gated, §6.1). status flips open->resolved
    -- when an admin picks a winner; the loser renumbers via
    -- Store.RenumberSubtree (MTIX-30.5). No create event is ever deleted.
    status          TEXT        NOT NULL DEFAULT 'open'
                                CHECK (status IN ('open', 'resolved')),
    winner_event_id TEXT,
    loser_new_path  TEXT,
    resolved_by     TEXT,
    detected_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at     TIMESTAMPTZ
);

-- Idempotent re-push of the same blocked create must not pile up duplicate
-- open rows: one open collision per blocked incoming create.
CREATE UNIQUE INDEX IF NOT EXISTS sync_node_collisions_incoming_uidx
    ON sync_node_collisions (incoming_event_id);

-- List support: `mtix sync collisions list` scans open rows per project.
CREATE INDEX IF NOT EXISTS idx_sync_node_collisions_open
    ON sync_node_collisions (project_prefix)
    WHERE status = 'open';
