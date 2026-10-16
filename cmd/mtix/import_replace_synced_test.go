// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// MTIX-107.31: mtix import --mode replace on a store that syncs with a hub
// is refused before the typed confirmation, and changes nothing. A replace
// would drop tasks and values whose events the local sync log holds, and a
// pull acknowledges those events instead of applying them, so it could
// never restore them.

// markEventsPushed plays a successful push: every pending event is accepted
// by the hub. It returns the events, as the next pull returns them.
func markEventsPushed(t *testing.T) []*model.SyncEvent {
	t.Helper()
	ctx := context.Background()
	evs, err := app.store.ReadPendingEvents(ctx, 1000)
	require.NoError(t, err)
	require.NoError(t, app.store.WithTx(ctx, func(tx *sql.Tx) error {
		_, execErr := tx.ExecContext(ctx, `UPDATE sync_events SET sync_status = 'pushed' WHERE sync_status = 'pending'`)
		return execErr
	}))
	return evs
}

// pullHeldEvents applies evs the way a pull applies a batch.
func pullHeldEvents(t *testing.T, evs []*model.SyncEvent) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, app.store.WithTx(ctx, func(tx *sql.Tx) error {
		for _, e := range evs {
			if err := sqlite.IdempotentApply(ctx, tx, e); err != nil {
				return err
			}
		}
		return nil
	}))
}

// configureHub sets a hub DSN for the test, with nothing pushed yet.
func configureHub(t *testing.T) []*model.SyncEvent {
	t.Helper()
	t.Setenv("MTIX_SYNC_DSN", "postgres://hub.invalid/mtix")
	return nil
}

func TestRunImport_ReplaceOnSyncedStore_RefusesAndKeepsTickets(t *testing.T) {
	tests := []struct {
		name    string
		sync    func(t *testing.T) []*model.SyncEvent
		refused bool
	}{
		{"events pushed to a hub", markEventsPushed, true},
		{"hub configured, nothing pushed yet", configureHub, true},
		{"no hub", func(*testing.T) []*model.SyncEvent { return nil }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("MTIX_SYNC_DSN", "")
			initTestApp(t)
			seedTickets(t, 2)
			path := writeExportOf(t)
			seedTickets(t, 1) // a ticket the file does not have
			pulled := tt.sync(t)
			typeTicketCount(t) // the operator would confirm the delete

			err := runImport(path, importFlags{mode: "replace"})
			if !tt.refused {
				require.NoError(t, err)
				assert.Equal(t, 2, allNodeCount(t), "a store without a hub takes the file whole")
				return
			}
			if err == nil {
				pullHeldEvents(t, pulled)
				require.Equal(t, 3, allNodeCount(t), "the replace dropped a ticket the sync log holds, and the pull did not restore it")
			}
			require.ErrorIs(t, err, sqlite.ErrReplaceOnSyncedStore)
			assert.Contains(t, err.Error(), "mtix sync reconcile --discard-local", "the refusal names the recovery")
			assert.Equal(t, 3, allNodeCount(t), "a refused replace changes nothing")
			assert.Empty(t, preDestroySnapshots(t, "import-replace"), "the refusal comes before the snapshot")

			pullHeldEvents(t, pulled)
			assert.Equal(t, 3, allNodeCount(t), "every ticket is still on the board after the pull")
		})
	}
}

// TestRunImport_ReplaceIntoEmptyStoreWithHub_Proceeds keeps the restore into
// a fresh data directory working on a project that syncs: the replace
// deletes nothing.
func TestRunImport_ReplaceIntoEmptyStoreWithHub_Proceeds(t *testing.T) {
	t.Setenv("MTIX_SYNC_DSN", "")
	initTestApp(t)
	seedTickets(t, 2)
	path := writeExportOf(t)
	initTestApp(t) // a fresh, empty store
	configureHub(t)
	answerDestructive(t, "", false)

	require.NoError(t, runImport(path, importFlags{mode: "replace"}))
	assert.Equal(t, 2, allNodeCount(t))
}
