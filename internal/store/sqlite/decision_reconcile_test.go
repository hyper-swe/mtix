// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

// SeedDecisionDeleteTestMachineHash avoids host interface enumeration in
// decision delete tests, which exercise the journal with an existing identity.
func SeedDecisionDeleteTestMachineHash(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.ExecContext(t.Context(),
		"UPDATE meta SET value = ? WHERE key = ?", "0123456789abcdef", "meta.sync.machine_hash")
	require.NoError(t, err)
}

// A missing metadata row requests a full rebuild under ADR-008 section 3.3.
func TestRenameTo_InvalidatesDecisionIndexAtomically(t *testing.T) {
	for _, fail := range []bool{false, true} {
		s, db, dir := reconcileTestStore(t)
		SeedDecisionDeleteTestMachineHash(t, db)
		seedTree(t, s)
		require.NoError(t, s.DeleteNode(t.Context(), "MTIX-2", false, "alice"))
		require.NoError(t, s.DeleteNode(t.Context(), "MTIX-1.2", false, "alice"))
		_, err := db.Exec(`UPDATE sync_events SET payload='{}' WHERE node_id='MTIX-1.2' AND op_type='delete'`)
		require.NoError(t, err)
		_, err = db.Exec("CREATE TABLE decision_index_meta(id INTEGER PRIMARY KEY); INSERT INTO decision_index_meta VALUES(1)")
		require.NoError(t, err)
		if fail {
			_, err = db.Exec("CREATE TRIGGER fail_reindex BEFORE DELETE ON decision_index_meta BEGIN SELECT RAISE(ABORT,'reindex refused'); END")
			require.NoError(t, err)
		}
		_, err = RenameTo(t.Context(), s, dir, "NEW")
		var count int
		require.NoError(t, db.QueryRow("SELECT count(*) FROM decision_index_meta").Scan(&count))
		if fail {
			require.ErrorContains(t, err, "reindex refused")
			require.Equal(t, 1, count)
			require.Contains(t, readNodeIDs(t, db), "MTIX-1")
		} else {
			require.NoError(t, err)
			require.Zero(t, count)
			var legacy, current string
			require.NoError(t, db.QueryRow("SELECT payload FROM sync_events WHERE node_id='NEW-1.2' AND op_type='delete'").Scan(&legacy))
			require.NoError(t, db.QueryRow("SELECT payload FROM sync_events WHERE node_id='NEW-2' AND op_type='delete'").Scan(&current))
			require.JSONEq(t, "{}", legacy)
			require.JSONEq(t, `{"cascade":false,"path":"MTIX-2"}`, current)
		}
	}
}
