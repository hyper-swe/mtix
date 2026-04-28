-- MTIX-15.2 hub schema (file 007 of 7).
-- Executed by MTIX-15.3 transport under PG advisory-lock single-flight.
-- DO NOT run manually.
--
-- This file is documentation only. The advisory lock acquisition lives
-- in MTIX-15.3's Go transport code:
--
--   tx.Exec("SELECT pg_advisory_xact_lock(hashtext('mtix_sync_migration'))")
--
-- The lock is released automatically at COMMIT/ROLLBACK so a crashed
-- migration never strands the lock. The chaos test in MTIX-15.3 proves
-- that a SIGKILL between two ALTER TABLE statements leaves the schema
-- recoverable — the next CLI re-runs the whole migration.
--
-- The hashtext input MUST stay 'mtix_sync_migration' verbatim so all
-- mtix CLIs hash to the same advisory-lock id regardless of build. Any
-- change here is a SYNC_VERSION_MISMATCH-class breaking change.

-- Intentionally no executable SQL in this file.
SELECT 1;
