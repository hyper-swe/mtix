// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	driver "modernc.org/sqlite"
)

// faultSchemaSQL builds a deferred foreign key whose violation surfaces only
// at COMMIT, the failure MTIX-198 recovers from. Constant test DDL.
const faultSchemaSQL = `
CREATE TABLE fault_parent (id INTEGER PRIMARY KEY);
CREATE TABLE fault_child (p INTEGER REFERENCES fault_parent(id) DEFERRABLE INITIALLY DEFERRED);
`

// armFault installs the deferred-FK tables and a trigger that inserts the
// violating child row whenever the trigger's statement runs.
func armFault(t *testing.T, s *Store, trigger string) {
	t.Helper()
	_, err := s.writeDB.ExecContext(context.Background(), faultSchemaSQL+trigger)
	require.NoError(t, err)
}

// requireWriterRecovered asserts the next write succeeds and the writer
// connection holds no open transaction (NFR-2.1, MTIX-198).
func requireWriterRecovered(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DROP TRIGGER IF EXISTS fault_trg`)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM fault_child`)
		return err
	}), "the next write after a failed commit must succeed")
	conn, err := s.writeDB.Conn(ctx)
	require.NoError(t, err)
	defer func() { require.NoError(t, conn.Close()) }()
	_, err = conn.ExecContext(ctx, "ROLLBACK")
	require.ErrorContains(t, err, "no transaction is active", "no transaction may remain open")
}

type commitFaultCase struct {
	name string
	arm  func(t *testing.T, s *Store)
	run  func(s *Store) error
}

// commitFaultCases lists every helper that commits on the writer connection.
func commitFaultCases() []commitFaultCase {
	ctx := context.Background()
	violate := `INSERT INTO fault_child(p) VALUES (999)`
	return []commitFaultCase{
		{"withWriteTx", func(t *testing.T, s *Store) { armFault(t, s, "") },
			func(s *Store) error {
				return s.withWriteTx(ctx, func(tx *sql.Tx) error { _, err := tx.ExecContext(ctx, violate); return err }, false)
			}},
		{"WithTx", func(t *testing.T, s *Store) { armFault(t, s, "") },
			func(s *Store) error {
				return s.WithTx(ctx, func(tx *sql.Tx) error { _, err := tx.ExecContext(ctx, violate); return err })
			}},
		{"withSchemaTx", func(t *testing.T, s *Store) { armFault(t, s, "") },
			func(s *Store) error {
				return s.withSchemaTx(ctx, func(tx *sql.Tx) error { _, err := tx.ExecContext(ctx, violate); return err })
			}},
		{"claimVCPruneNotice", func(t *testing.T, s *Store) {
			armFault(t, s, `CREATE TRIGGER fault_trg AFTER INSERT ON meta WHEN NEW.key = '`+vcPrunedKey+`'
				BEGIN `+violate+`; END;`)
		}, func(s *Store) error { _, err := s.claimVCPruneNotice(ctx, "2026-10-26T00:00:00Z"); return err }},
		{"backfillUIDsFromCreateEvents", armBackfillFault, func(s *Store) error { return s.backfillUIDsFromCreateEvents(ctx) }},
		{"widenQuarantineSource", armWidenFault, func(s *Store) error { return s.widenQuarantineSource(ctx) }},
	}
}

// TestCommitFailure_EachWriterHelper_RecoversWriter injects a commit-time
// foreign-key failure into every helper that commits on the writer connection
// and checks each leaves the writer usable (MTIX-198).
func TestCommitFailure_EachWriterHelper_RecoversWriter(t *testing.T) {
	ctx := context.Background()
	tests := commitFaultCases()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, _, _ := bufferedStore(t)
			tt.arm(t, s)
			_, err := s.writeDB.ExecContext(ctx, `CREATE TEMP TABLE conn_marker (x INTEGER)`)
			require.NoError(t, err)
			err = tt.run(s)
			require.ErrorContains(t, err, "FOREIGN KEY", "the injected commit failure must be reported")
			var se *driver.Error
			require.True(t, errors.As(err, &se), "the commit error must stay in the wrapped chain")
			require.Equal(t, 787, se.Code(), "SQLITE_CONSTRAINT_FOREIGNKEY")
			_, err = s.writeDB.ExecContext(ctx, `SELECT x FROM conn_marker`)
			require.NoError(t, err, "a clean rollback must keep the connection, not discard it")
			requireWriterRecovered(t, s)
		})
	}
}

// armBackfillFault leaves one uid-less node with a create event, and a
// trigger that violates the deferred key when the backfill updates it.
func armBackfillFault(t *testing.T, s *Store) {
	t.Helper()
	createNodes(t, s, 1, 1)
	_, err := s.writeDB.ExecContext(context.Background(), `
UPDATE nodes SET uid = '' WHERE id = 'PROJ-1';
INSERT INTO sync_events (event_id, project_prefix, node_id, op_type, payload, wall_clock_ts,
  lamport_clock, vector_clock, author_id, author_machine_hash, created_at)
VALUES ('ev-1', 'PROJ', 'PROJ-1', 'create_node', '{}', 1, 1, '{}', 'a', 'h', '2026-10-26T00:00:00Z');`)
	require.NoError(t, err)
	armFault(t, s, `CREATE TRIGGER fault_trg AFTER UPDATE ON nodes
		BEGIN INSERT INTO fault_child(p) VALUES (999); END;`)
}

// armWidenFault restores the pre-push quarantine table with one row that a
// deferred key references, so the rebuild's DROP leaves the key violated.
func armWidenFault(t *testing.T, s *Store) {
	t.Helper()
	_, err := s.writeDB.ExecContext(context.Background(), `
DROP TABLE sync_quarantine;
CREATE TABLE sync_quarantine (
    event_id TEXT PRIMARY KEY,
    source TEXT NOT NULL CHECK (source IN ('pull', 'sweep')),
    raw_event TEXT NOT NULL, reason TEXT NOT NULL, first_seen TEXT NOT NULL,
    last_attempt TEXT NOT NULL, attempts INTEGER NOT NULL DEFAULT 1,
    cli_version TEXT NOT NULL DEFAULT '');
INSERT INTO sync_quarantine (event_id, source, raw_event, reason, first_seen, last_attempt)
VALUES ('e1', 'pull', '{}', 'r', 't', 't');
CREATE TABLE fault_parent (id INTEGER PRIMARY KEY);
CREATE TABLE fault_child (p TEXT REFERENCES sync_quarantine(event_id) DEFERRABLE INITIALLY DEFERRED);
INSERT INTO fault_child(p) VALUES ('e1');`)
	require.NoError(t, err)
}
