-- MTIX-30.8 hub schema (file 013).
-- Executed by the transport under PG advisory-lock single-flight,
-- auto-applied in lexical order via migrations.Files(). DO NOT run manually.
-- (012 is taken by node_renumber_remaps; this ticket reserves 011 + 013.)
--
-- The hub RESTORE-EPOCH (ADR-003 §15, Addendum A) — the un-forgeable gate
-- for the restore-collision discriminator (Option B, §6.1).
--
-- restore_epoch is a single monotonic counter, advanced ONLY by an explicit
-- operator action (`mtix sync mark-restored`, a documented restore-from-backup
-- runbook step). CLIENTS CANNOT ADVANCE IT: no push path writes it; the only
-- mutation is the operator command. This is what makes the discriminator
-- trust-minimizing (§15) — a compromised client cannot manufacture a restore
-- window during normal operation.
--
-- It lives in its own singleton table (one row, pinned by a BOOLEAN primary
-- key fixed at TRUE) rather than a column so the value is hub-global, not
-- per-event or per-project. Like the rest of the mtix-owned schema it is
-- captured by `mtix sync backup`/restore, so a restore resets the epoch to
-- the backed-up value; the operator's mark-restored then bumps it ABOVE every
-- surviving create's stamp, which is precisely what opens the restore window.

CREATE TABLE IF NOT EXISTS sync_hub_state (
    -- Singleton guard: only one row may exist (id is always TRUE).
    id            BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (id),
    restore_epoch BIGINT  NOT NULL DEFAULT 0
);

-- Seed the singleton at epoch 0 (the no-restore-ever baseline). Idempotent:
-- a hub that already ran this keeps its (possibly advanced) epoch.
INSERT INTO sync_hub_state (id, restore_epoch)
VALUES (TRUE, 0)
ON CONFLICT (id) DO NOTHING;

-- The per-create EPOCH STAMP (ADR-003 §15): every event is stamped, at hub
-- registry acceptance, with the restore_epoch current at that moment —
-- hub-side, NEVER client-asserted. The detector compares the HELD create's
-- stamp against the current epoch: a strictly-earlier stamp means the hold
-- predates the most recent operator restore-bump (a cross-epoch re-grant =>
-- Option B); an equal stamp is a same-epoch race => ordinary renumber (§6).
--
-- DEFAULT 0 backfills legacy rows to the no-restore baseline, so pre-30.8
-- creates read as epoch 0 and never spuriously trip the cross-epoch gate.
ALTER TABLE sync_events ADD COLUMN IF NOT EXISTS restore_epoch BIGINT NOT NULL DEFAULT 0;
