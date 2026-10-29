// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MTIX-107.31: the doctor flags tasks the local sync log creates that the
// board lacks, which a pull cannot restore, and its fix is a command.

func TestAppendLoggedNodesCheck_Drift_FailsWithFixCommand(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(t *testing.T)
		wantPass   bool
		wantDetail string
		wantFix    string
	}{
		{"synced store, board holds every task", func(t *testing.T) { markEventsPushed(t) }, true, "ok", ""},
		{"synced store, task dropped", func(t *testing.T) {
			markEventsPushed(t)
			dropNodeRow(t, "TEST-2")
		}, false, "the board lacks 1 of the tasks the local sync log creates: TEST-2", loggedNodesFix},
		{"hub configured, nothing pushed, task dropped", func(t *testing.T) {
			configureHub(t)
			dropNodeRow(t, "TEST-2")
		}, false, "the board lacks 1 of the tasks the local sync log creates: TEST-2", loggedNodesFix},
		{"file relay peer that published, task dropped", func(t *testing.T) {
			_, err := app.store.WriteDB().Exec(`INSERT INTO relay_push_cursor (id, cursor, pub_epoch, next_rs) VALUES (1, 0, 1, 7)`)
			require.NoError(t, err)
			dropNodeRow(t, "TEST-2")
		}, false, "the board lacks 1 of the tasks the local sync log creates: TEST-2", loggedNodesFix},
		{"hub config key only, task dropped", func(t *testing.T) {
			_, err := app.configSvc.Set("sync.enabled", "true")
			require.NoError(t, err)
			dropNodeRow(t, "TEST-2")
		}, false, "the board lacks 1 of the tasks the local sync log creates: TEST-2", loggedNodesFix},
		{"store that does not sync", func(t *testing.T) { dropNodeRow(t, "TEST-2") },
			true, "ok (the store does not sync with a hub)", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("MTIX_SYNC_DSN", "")
			initTestApp(t)
			seedTickets(t, 2)
			tt.setup(t)

			r := appendLoggedNodesCheck(context.Background(), DoctorReport{OverallPass: true}, app.store)
			require.Len(t, r.Checks, 1)
			c := r.Checks[0]
			assert.Equal(t, loggedNodesCheckName, c.Name)
			assert.Equal(t, tt.wantPass, c.Pass)
			assert.Equal(t, tt.wantPass, r.OverallPass)
			assert.Contains(t, c.Detail, tt.wantDetail)
			assert.Equal(t, tt.wantFix, c.Fix)
		})
	}
}

// dropNodeRow deletes a node's row without an event, as a replace import
// of an earlier version did.
func dropNodeRow(t *testing.T, id string) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, app.store.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM nodes WHERE id = ?`, id)
		return err
	}))
}

// The printed fix is the safe sequence: reconcile drops what is not pushed
// yet, so the pending-0 check comes before the discard, and the steps are
// not chained into one command.
func TestLoggedNodesFix_NamesThePendingZeroCheckBeforeTheDiscard(t *testing.T) {
	pending := strings.Index(loggedNodesFix, "pending 0")
	discard := strings.Index(loggedNodesFix, "--discard-local")
	require.GreaterOrEqual(t, pending, 0, "the fix names the pending-0 check")
	assert.Less(t, pending, discard, "the check comes before the discard")
	assert.NotContains(t, loggedNodesFix, "&&", "the discard is not chained after the push")
}
