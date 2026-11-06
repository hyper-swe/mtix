// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// FR-19.1 / MTIX-112.2.1: retiring outbound mail and replacing the board are atomic.
func TestImport_RetainedMailRetirementFailure_RollsBackReplacement(t *testing.T) {
	for _, tc := range []struct{ name, trigger string }{
		{"held copy", `CREATE TRIGGER reject_retire BEFORE DELETE ON sync_quarantine BEGIN SELECT RAISE(ABORT,'retirement fault'); END`},
		{"provenance", `CREATE TRIGGER reject_retire BEFORE INSERT ON inbox_restored_events BEGIN SELECT RAISE(ABORT,'retirement fault'); END`},
		{"status", `CREATE TRIGGER reject_retire BEFORE UPDATE OF sync_status ON sync_events BEGIN SELECT RAISE(ABORT,'retirement fault'); END`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dest, raw := retentionStore(t)
			addressThree(t, dest, "recipient")
			_, err := raw.Exec(`INSERT INTO sync_quarantine(event_id,source,raw_event,reason,first_seen,last_attempt)
 SELECT event_id,'push','{}','held','2026-11-05T00:00:00Z','2026-11-05T00:00:00Z' FROM sync_events WHERE op_type='comment' AND node_id='PROJ-2'`)
			require.NoError(t, err)
			file := withoutExportNode(transportExport(t, dest), "PROJ-2")
			require.NoError(t, sqlite.RecomputeExportChecksum(file))
			_, err = raw.Exec(tc.trigger)
			require.NoError(t, err)
			_, err = dest.Import(t.Context(), file, sqlite.ImportModeReplace, false)
			require.ErrorContains(t, err, "retirement fault")
			_, err = dest.GetNode(t.Context(), "PROJ-2")
			require.NoError(t, err)
			var count int
			require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM inbox_restored_events`).Scan(&count))
			require.Zero(t, count)
			require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM sync_quarantine WHERE source='push'`).Scan(&count))
			require.Equal(t, 1, count)
			require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM sync_events WHERE node_id='PROJ-2' AND op_type='comment' AND sync_status='pending'`).Scan(&count))
			require.Equal(t, 1, count)
		})
	}
}
