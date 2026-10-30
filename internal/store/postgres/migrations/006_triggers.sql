-- MTIX-15.2 hub schema (file 006 of 7).
-- Executed by MTIX-15.3 transport under PG advisory-lock single-flight.
-- DO NOT run manually.
--
-- audit_log_immutable enforces FR-18.5 at the trigger layer: UPDATE and
-- DELETE on audit_log and sync_conflicts raise an exception. The table
-- owner or a PG superuser can drop or disable triggers — that residual
-- risk is documented as threat T5 in SYNC-DESIGN section 7.3 and
-- mitigated by archiving the hub to immutable cold storage for
-- safety-critical adopters.

CREATE OR REPLACE FUNCTION audit_log_immutable()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'append-only table; UPDATE/DELETE forbidden (FR-18.5)';
END;
$$ LANGUAGE plpgsql;

-- MTIX-107.52: DROP TRIGGER takes ACCESS EXCLUSIVE even on a current hub.
-- Skip only an enabled trigger with the exact released behavior. A missing,
-- disabled, conditional, column-specific or otherwise changed trigger is
-- replaced just as before; its function is still refreshed above.
-- tgtype bitmasks: 19 = ROW | BEFORE | UPDATE; 11 = ROW | BEFORE | DELETE.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_catalog.pg_trigger
        WHERE tgrelid = 'audit_log'::regclass AND tgname = 'audit_log_no_update'
          AND tgfoid = 'audit_log_immutable()'::regprocedure AND tgtype = 19
          AND tgenabled = 'O' AND NOT tgisinternal AND tgconstraint = 0
          AND NOT tgdeferrable AND NOT tginitdeferred AND tgnargs = 0
          AND tgattr = ''::int2vector AND tgqual IS NULL
          AND tgoldtable IS NULL AND tgnewtable IS NULL
    ) THEN
        DROP TRIGGER IF EXISTS audit_log_no_update ON audit_log;
        CREATE TRIGGER audit_log_no_update
            BEFORE UPDATE ON audit_log
            FOR EACH ROW EXECUTE FUNCTION audit_log_immutable();
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM pg_catalog.pg_trigger
        WHERE tgrelid = 'audit_log'::regclass AND tgname = 'audit_log_no_delete'
          AND tgfoid = 'audit_log_immutable()'::regprocedure AND tgtype = 11
          AND tgenabled = 'O' AND NOT tgisinternal AND tgconstraint = 0
          AND NOT tgdeferrable AND NOT tginitdeferred AND tgnargs = 0
          AND tgattr = ''::int2vector AND tgqual IS NULL
          AND tgoldtable IS NULL AND tgnewtable IS NULL
    ) THEN
        DROP TRIGGER IF EXISTS audit_log_no_delete ON audit_log;
        CREATE TRIGGER audit_log_no_delete
            BEFORE DELETE ON audit_log
            FOR EACH ROW EXECUTE FUNCTION audit_log_immutable();
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM pg_catalog.pg_trigger
        WHERE tgrelid = 'sync_conflicts'::regclass AND tgname = 'sync_conflicts_no_update'
          AND tgfoid = 'audit_log_immutable()'::regprocedure AND tgtype = 19
          AND tgenabled = 'O' AND NOT tgisinternal AND tgconstraint = 0
          AND NOT tgdeferrable AND NOT tginitdeferred AND tgnargs = 0
          AND tgattr = ''::int2vector AND tgqual IS NULL
          AND tgoldtable IS NULL AND tgnewtable IS NULL
    ) THEN
        DROP TRIGGER IF EXISTS sync_conflicts_no_update ON sync_conflicts;
        CREATE TRIGGER sync_conflicts_no_update
            BEFORE UPDATE ON sync_conflicts
            FOR EACH ROW EXECUTE FUNCTION audit_log_immutable();
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM pg_catalog.pg_trigger
        WHERE tgrelid = 'sync_conflicts'::regclass AND tgname = 'sync_conflicts_no_delete'
          AND tgfoid = 'audit_log_immutable()'::regprocedure AND tgtype = 11
          AND tgenabled = 'O' AND NOT tgisinternal AND tgconstraint = 0
          AND NOT tgdeferrable AND NOT tginitdeferred AND tgnargs = 0
          AND tgattr = ''::int2vector AND tgqual IS NULL
          AND tgoldtable IS NULL AND tgnewtable IS NULL
    ) THEN
        DROP TRIGGER IF EXISTS sync_conflicts_no_delete ON sync_conflicts;
        CREATE TRIGGER sync_conflicts_no_delete
            BEFORE DELETE ON sync_conflicts
            FOR EACH ROW EXECUTE FUNCTION audit_log_immutable();
    END IF;

END
$$;
