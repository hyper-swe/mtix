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
-- Each guard is created only when pg_trigger lacks it. CREATE TRIGGER
-- takes a lock that blocks every writer of the table, so a re-run must not
-- take it again (F-44); nothing is dropped and re-created. `mtix sync
-- harden` checks that every guard exists and is enabled, and restores a
-- missing one by running this file.

CREATE OR REPLACE FUNCTION append_only_no_truncate()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'append-only table %: TRUNCATE forbidden (FR-18.5)', TG_TABLE_NAME;
END;
$$ LANGUAGE plpgsql;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_trigger
                   WHERE tgrelid = 'audit_log'::regclass
                     AND tgname = 'audit_log_no_truncate') THEN
        CREATE TRIGGER audit_log_no_truncate
            BEFORE TRUNCATE ON audit_log
            FOR EACH STATEMENT EXECUTE FUNCTION append_only_no_truncate();
    END IF;

    IF NOT EXISTS (SELECT 1 FROM pg_trigger
                   WHERE tgrelid = 'sync_conflicts'::regclass
                     AND tgname = 'sync_conflicts_no_truncate') THEN
        CREATE TRIGGER sync_conflicts_no_truncate
            BEFORE TRUNCATE ON sync_conflicts
            FOR EACH STATEMENT EXECUTE FUNCTION append_only_no_truncate();
    END IF;

    IF NOT EXISTS (SELECT 1 FROM pg_trigger
                   WHERE tgrelid = 'sync_events'::regclass
                     AND tgname = 'sync_events_no_truncate') THEN
        CREATE TRIGGER sync_events_no_truncate
            BEFORE TRUNCATE ON sync_events
            FOR EACH STATEMENT EXECUTE FUNCTION append_only_no_truncate();
    END IF;
END
$$;
