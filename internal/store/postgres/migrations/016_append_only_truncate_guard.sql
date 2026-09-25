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
-- that executes this migration's append_only_no_truncate, compared by OID. CREATE TRIGGER takes a lock that
-- blocks every writer of the table, so a re-run on a hub whose guards are
-- in place must not take it again (F-44): nothing is dropped and
-- re-created then. A trigger of a guard's name that executes another
-- function is dropped and the guard created in its place, inside the one
-- transaction that runs every migration, so `mtix sync init` replaces it
-- with no moment unguarded (MTIX-95.7). `mtix sync harden` checks that
-- every guard exists and is enabled, and restores a missing one by
-- running this file.

-- The guard function and the guards are created in current_schema(), the
-- first schema on the search_path. Refuse, before creating anything, when
-- a guarded table is in another schema: a guard there would execute a
-- function outside the hub, and the whole transaction rolls back
-- (MTIX-95.7).
DO $$
DECLARE
    tables_schema text;
BEGIN
    SELECT n.nspname INTO tables_schema
    FROM unnest(ARRAY['audit_log', 'sync_conflicts', 'sync_events']) AS g(tbl)
    JOIN pg_class c ON c.oid = to_regclass(g.tbl)
    JOIN pg_namespace n ON n.oid = c.relnamespace
    WHERE n.nspname IS DISTINCT FROM current_schema()
    LIMIT 1;
    IF tables_schema IS NOT NULL THEN
        RAISE EXCEPTION 'the sync tables are in schema %, but the first schema on the search_path is %; set the search_path so that % comes first, then run the command again',
            quote_ident(tables_schema), COALESCE(quote_ident(current_schema()), '(none)'), quote_ident(tables_schema);
    END IF;
END
$$;

CREATE OR REPLACE FUNCTION append_only_no_truncate()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'append-only table %: TRUNCATE forbidden (FR-18.5)', TG_TABLE_NAME;
END;
$$ LANGUAGE plpgsql;

DO $$
DECLARE
    -- The guard function this migration created above, in the current
    -- schema, by OID: a function of the same name in another schema is
    -- another function (MTIX-95.7).
    guard_fn oid := pg_catalog.to_regprocedure(
        pg_catalog.quote_ident(pg_catalog.current_schema()) || '.append_only_no_truncate()');
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_trigger t
                   WHERE t.tgrelid = 'audit_log'::regclass
                     AND t.tgname = 'audit_log_no_truncate'
                     AND t.tgfoid = guard_fn) THEN
        DROP TRIGGER IF EXISTS audit_log_no_truncate ON audit_log;
        CREATE TRIGGER audit_log_no_truncate
            BEFORE TRUNCATE ON audit_log
            FOR EACH STATEMENT EXECUTE FUNCTION append_only_no_truncate();
    END IF;

    IF NOT EXISTS (SELECT 1 FROM pg_trigger t
                   WHERE t.tgrelid = 'sync_conflicts'::regclass
                     AND t.tgname = 'sync_conflicts_no_truncate'
                     AND t.tgfoid = guard_fn) THEN
        DROP TRIGGER IF EXISTS sync_conflicts_no_truncate ON sync_conflicts;
        CREATE TRIGGER sync_conflicts_no_truncate
            BEFORE TRUNCATE ON sync_conflicts
            FOR EACH STATEMENT EXECUTE FUNCTION append_only_no_truncate();
    END IF;

    IF NOT EXISTS (SELECT 1 FROM pg_trigger t
                   WHERE t.tgrelid = 'sync_events'::regclass
                     AND t.tgname = 'sync_events_no_truncate'
                     AND t.tgfoid = guard_fn) THEN
        DROP TRIGGER IF EXISTS sync_events_no_truncate ON sync_events;
        CREATE TRIGGER sync_events_no_truncate
            BEFORE TRUNCATE ON sync_events
            FOR EACH STATEMENT EXECUTE FUNCTION append_only_no_truncate();
    END IF;
END
$$;
