-- MTIX-95.1.7 hub schema (file 017).
-- Executed by the transport under PG advisory-lock single-flight,
-- auto-applied in lexical order via migrations.Files(). DO NOT run manually.
-- (014 and 015 are reserved by MTIX-95.5 and MTIX-95.4.)
--
-- The hub decides every value of the restore-collision discriminator
-- (ADR-003 §15, Addendum A):
--
-- 1. hub_stamp_restore_epoch, fired BEFORE INSERT on every sync_events row
--    (trigger sync_events_stamp_restore_epoch), sets restore_epoch to the
--    epoch of the sync_hub_state singleton, whatever value the inserting
--    statement carries.
-- 2. record_restore_collision records one restore collision in
--    sync_node_collisions. It takes only the incoming create's identity
--    (project, number, event id, uid, wall clock) and reads everything
--    else from the hub: the create that holds the number, the epoch it was
--    stamped with and the current epoch. It records the collision only when
--    that held create is a different node stamped in an epoch earlier than
--    the current one, and returns whether a collision for the incoming
--    create is on record. A syncing role needs EXECUTE on it, and SELECT on
--    sync_node_collisions to list (UPDATE to resolve), nothing more.
--
-- Both functions run as their owner, the owner of the sync tables, who
-- ran this migration. Each is created with the search_path fixed to
-- pg_catalog and pg_temp, then the DO block below fixes it to the schema
-- this migration runs in, followed by pg_temp last, so a table of the same
-- name in a session's temporary schema is never read, and the hub may live
-- in a schema other than public. Migrate refuses before this file runs
-- when that schema is not the sync tables' schema (checkSchemaFirst, and
-- the check at the top of 016). Each function is created executable by its
-- owner alone: PUBLIC's EXECUTE is revoked only while the function still
-- has the built-in default privileges, so a re-run changes no privilege.
-- A trigger fires without EXECUTE on its function.
--
-- The stamp trigger is created only when pg_trigger lacks a trigger of its
-- name that executes this migration's function, compared by OID, as for
-- the TRUNCATE guards of 016: CREATE TRIGGER takes a lock that blocks every
-- writer of sync_events, so a re-run on a hub that has it takes none (F-44).

CREATE OR REPLACE FUNCTION hub_stamp_restore_epoch()
RETURNS TRIGGER
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    NEW.restore_epoch := COALESCE(
        (SELECT s.restore_epoch FROM sync_hub_state s WHERE s.id), 0);
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION record_restore_collision(
    p_project_prefix TEXT,
    p_display_path TEXT,
    p_incoming_event_id TEXT,
    p_incoming_uid TEXT,
    p_incoming_wall_clock_ts BIGINT)
RETURNS BOOLEAN
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    -- The incoming create's effective uid: its uid, or its own event id
    -- for a create that carries none (ADR-003 §2).
    v_incoming_uid    TEXT := COALESCE(NULLIF(p_incoming_uid, ''), p_incoming_event_id);
    v_current_epoch   BIGINT;
BEGIN
    SELECT s.restore_epoch INTO v_current_epoch FROM sync_hub_state s WHERE s.id;

    -- The create that holds the number, when it is another node stamped in
    -- an earlier epoch than the current one. A re-sent blocked create keeps
    -- its one row (incoming_event_id is unique).
    INSERT INTO sync_node_collisions
        (project_prefix, display_path,
         held_event_id, held_uid, held_epoch, held_wall_clock_ts,
         incoming_event_id, incoming_uid, incoming_wall_clock_ts,
         detected_epoch, status)
    SELECT h.project_prefix, h.node_id,
           h.event_id, COALESCE(NULLIF(h.uid, ''), h.event_id), h.restore_epoch, h.wall_clock_ts,
           p_incoming_event_id, v_incoming_uid, p_incoming_wall_clock_ts,
           v_current_epoch, 'open'
      FROM sync_events h
     WHERE h.project_prefix = p_project_prefix
       AND h.node_id = p_display_path
       AND h.op_type = 'create_node'
       AND h.event_id <> p_incoming_event_id
       AND COALESCE(NULLIF(h.uid, ''), h.event_id) <> v_incoming_uid
       AND h.restore_epoch < v_current_epoch
     ORDER BY h.restore_epoch, h.event_id
     LIMIT 1
    ON CONFLICT (incoming_event_id) DO NOTHING;

    RETURN EXISTS (SELECT 1 FROM sync_node_collisions c
                    WHERE c.incoming_event_id = p_incoming_event_id);
END;
$$;

-- Fix each function's search_path to this schema, then pg_temp, and make
-- a newly created function executable by its owner alone. Identifiers are
-- quoted server-side (%I); the signatures are this file's constants.
DO $$
DECLARE
    hub_schema TEXT := pg_catalog.current_schema();
    fn         TEXT;
BEGIN
    FOREACH fn IN ARRAY ARRAY['hub_stamp_restore_epoch()',
                              'record_restore_collision(text, text, text, text, bigint)'] LOOP
        EXECUTE pg_catalog.format('ALTER FUNCTION %I.%s SET search_path = %I, pg_temp',
                                  hub_schema, fn, hub_schema);
        IF (SELECT p.proacl IS NULL FROM pg_catalog.pg_proc p
             WHERE p.oid = pg_catalog.to_regprocedure(pg_catalog.quote_ident(hub_schema) || '.' || fn)) THEN
            EXECUTE pg_catalog.format('REVOKE ALL ON FUNCTION %I.%s FROM PUBLIC', hub_schema, fn);
        END IF;
    END LOOP;
END
$$;

DO $$
DECLARE
    -- The stamp function this migration created above, in the current
    -- schema, by OID: a function of the same name in another schema is
    -- another function.
    stamp_fn oid := pg_catalog.to_regprocedure(
        pg_catalog.quote_ident(pg_catalog.current_schema()) || '.hub_stamp_restore_epoch()');
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_trigger t
                   WHERE t.tgrelid = 'sync_events'::regclass
                     AND t.tgname = 'sync_events_stamp_restore_epoch'
                     AND t.tgfoid = stamp_fn) THEN
        DROP TRIGGER IF EXISTS sync_events_stamp_restore_epoch ON sync_events;
        CREATE TRIGGER sync_events_stamp_restore_epoch
            BEFORE INSERT ON sync_events
            FOR EACH ROW EXECUTE FUNCTION hub_stamp_restore_epoch();
    END IF;
END
$$;
