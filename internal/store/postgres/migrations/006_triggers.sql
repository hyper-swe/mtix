-- MTIX-15.2 hub schema (file 006 of 7).
-- Executed by MTIX-15.3 transport under PG advisory-lock single-flight.
-- DO NOT run manually.
--
-- audit_log_immutable enforces FR-18.5 at the trigger layer: UPDATE and
-- DELETE on audit_log and sync_conflicts raise an exception. A PG
-- superuser can disable triggers — that residual risk is documented as
-- threat T5 in SYNC-DESIGN section 7.3 and mitigated by archiving
-- audit_log to immutable cold storage for safety-critical adopters.

CREATE OR REPLACE FUNCTION audit_log_immutable()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'append-only table; UPDATE/DELETE forbidden (FR-18.5)';
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS audit_log_no_update ON audit_log;
CREATE TRIGGER audit_log_no_update
    BEFORE UPDATE ON audit_log
    FOR EACH ROW EXECUTE FUNCTION audit_log_immutable();

DROP TRIGGER IF EXISTS audit_log_no_delete ON audit_log;
CREATE TRIGGER audit_log_no_delete
    BEFORE DELETE ON audit_log
    FOR EACH ROW EXECUTE FUNCTION audit_log_immutable();

DROP TRIGGER IF EXISTS sync_conflicts_no_update ON sync_conflicts;
CREATE TRIGGER sync_conflicts_no_update
    BEFORE UPDATE ON sync_conflicts
    FOR EACH ROW EXECUTE FUNCTION audit_log_immutable();

DROP TRIGGER IF EXISTS sync_conflicts_no_delete ON sync_conflicts;
CREATE TRIGGER sync_conflicts_no_delete
    BEFORE DELETE ON sync_conflicts
    FOR EACH ROW EXECUTE FUNCTION audit_log_immutable();
