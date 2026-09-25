-- MTIX-95.1 hub schema (file 016).
-- Executed by the transport under PG advisory-lock single-flight,
-- auto-applied in lexical order via migrations.Files(). DO NOT run manually.
-- (014 and 015 are reserved by MTIX-95.5 and MTIX-95.4.)
--
-- Each append-only table also has a statement-level TRUNCATE guard: a
-- BEFORE TRUNCATE ... FOR EACH STATEMENT trigger that raises, next to the
-- row triggers of 006 that refuse UPDATE and DELETE (FR-18.5). A
-- TRUNCATE ... CASCADE fires the guard of every table it would empty.
--
-- Each guard is created only when pg_trigger lacks a trigger of its name
-- that executes append_only_no_truncate. CREATE TRIGGER takes a lock that
-- blocks every writer of the table, so a re-run on a hub whose guards are
-- in place must not take it again (F-44): nothing is dropped and
-- re-created then. A trigger of a guard's name that executes another
-- function is dropped and the guard created in its place, inside the one
-- transaction that runs every migration, so `mtix sync init` replaces it
-- with no moment unguarded (MTIX-95.7). `mtix sync harden` checks that
-- every guard exists and is enabled, and restores a missing one by
-- running this file.

CREATE OR REPLACE FUNCTION append_only_no_truncate()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'append-only table %: TRUNCATE forbidden (FR-18.5)', TG_TABLE_NAME;
END;
$$ LANGUAGE plpgsql;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_trigger t
                   JOIN pg_proc p ON p.oid = t.tgfoid
                   WHERE t.tgrelid = 'audit_log'::regclass
                     AND t.tgname = 'audit_log_no_truncate'
                     AND p.proname = 'append_only_no_truncate') THEN
        DROP TRIGGER IF EXISTS audit_log_no_truncate ON audit_log;
        CREATE TRIGGER audit_log_no_truncate
            BEFORE TRUNCATE ON audit_log
            FOR EACH STATEMENT EXECUTE FUNCTION append_only_no_truncate();
    END IF;

    IF NOT EXISTS (SELECT 1 FROM pg_trigger t
                   JOIN pg_proc p ON p.oid = t.tgfoid
                   WHERE t.tgrelid = 'sync_conflicts'::regclass
                     AND t.tgname = 'sync_conflicts_no_truncate'
                     AND p.proname = 'append_only_no_truncate') THEN
        DROP TRIGGER IF EXISTS sync_conflicts_no_truncate ON sync_conflicts;
        CREATE TRIGGER sync_conflicts_no_truncate
            BEFORE TRUNCATE ON sync_conflicts
            FOR EACH STATEMENT EXECUTE FUNCTION append_only_no_truncate();
    END IF;

    IF NOT EXISTS (SELECT 1 FROM pg_trigger t
                   JOIN pg_proc p ON p.oid = t.tgfoid
                   WHERE t.tgrelid = 'sync_events'::regclass
                     AND t.tgname = 'sync_events_no_truncate'
                     AND p.proname = 'append_only_no_truncate') THEN
        DROP TRIGGER IF EXISTS sync_events_no_truncate ON sync_events;
        CREATE TRIGGER sync_events_no_truncate
            BEFORE TRUNCATE ON sync_events
            FOR EACH STATEMENT EXECUTE FUNCTION append_only_no_truncate();
    END IF;
END
$$;
