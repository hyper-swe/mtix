// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// MTIX-107.31: on a store that syncs with a hub, the automatic import never
// drops a task whose events the local sync log holds, and the pull after it
// leaves the task on the board. A teammate's board that keeps every local
// task still imports.

// pushAll plays a successful push of every pending event of f's store and
// returns the events, as the next pull returns them.
func pushAll(t *testing.T, f *guardFixture) []*model.SyncEvent {
	t.Helper()
	ctx := context.Background()
	evs, err := f.store.ReadPendingEvents(ctx, 1000)
	require.NoError(t, err)
	require.NoError(t, f.store.WithTx(ctx, func(tx *sql.Tx) error {
		_, execErr := tx.ExecContext(ctx, `UPDATE sync_events SET sync_status = 'pushed' WHERE sync_status = 'pending'`)
		return execErr
	}))
	return evs
}

// pullInto applies evs to f's store the way a pull applies a batch.
func pullInto(t *testing.T, f *guardFixture, evs []*model.SyncEvent) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, f.store.WithTx(ctx, func(tx *sql.Tx) error {
		for _, e := range evs {
			if err := sqlite.IdempotentApply(ctx, tx, e); err != nil {
				return err
			}
		}
		return nil
	}))
}

func createTask(t *testing.T, f *guardFixture, id string, seq int) {
	t.Helper()
	at := time.Date(2026, 9, 24, 9, 0, 0, 0, time.UTC)
	require.NoError(t, f.store.CreateNode(context.Background(), &model.Node{
		ID: id, Project: "PROJ", Seq: seq, Title: "Task " + id, Status: model.StatusOpen,
		Priority: model.PriorityMedium, Weight: 1, NodeType: model.NodeTypeEpic, CreatedAt: at, UpdatedAt: at,
	}))
}

func TestAutoImport_SyncedStore_KeepsTasksTheLogHolds(t *testing.T) {
	tests := []struct {
		name string
		// board returns the tasks.json that git brings in, after a's
		// events were pushed.
		board     func(t *testing.T, a *guardFixture, older []byte) []byte
		wantErr   error
		wantTasks []string
	}{
		{"older board lacks a pushed task", func(_ *testing.T, _ *guardFixture, older []byte) []byte {
			return older
		}, service.ErrAutoImportRefused, []string{"PROJ-1", "PROJ-2", "PROJ-3"}},
		{"teammate board adds a task", func(t *testing.T, a *guardFixture, _ []byte) []byte {
			b := teammateClone(t, a)
			createTask(t, b, "PROJ-4", 4)
			require.NoError(t, b.svc.AutoExport(context.Background(), b.mtixDir))
			return b.read(t, "tasks.json")
		}, nil, []string{"PROJ-1", "PROJ-2", "PROJ-3", "PROJ-4"}},
		{"same id and title, uid dropped", func(t *testing.T, a *guardFixture, _ []byte) []byte {
			return a.teammateBoard(t, func(d *sqlite.ExportData) { setUID(d, "PROJ-3", "") })
		}, sqlite.ErrReplaceOnSyncedStore, []string{"PROJ-1", "PROJ-2", "PROJ-3"}},
		{"same id and title, uid changed", func(t *testing.T, a *guardFixture, _ []byte) []byte {
			return a.teammateBoard(t, func(d *sqlite.ExportData) { setUID(d, "PROJ-3", "01900000-0000-7000-8000-000000000abc") })
		}, sqlite.ErrReplaceOnSyncedStore, []string{"PROJ-1", "PROJ-2", "PROJ-3"}},
		{"teammate renumbers a task", func(t *testing.T, a *guardFixture, _ []byte) []byte {
			b := teammateClone(t, a)
			require.NoError(t, b.store.RenumberSubtree(context.Background(), "PROJ-3", 7))
			require.NoError(t, b.svc.AutoExport(context.Background(), b.mtixDir))
			return b.read(t, "tasks.json")
		}, nil, []string{"PROJ-1", "PROJ-2", "PROJ-7"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			a := newGuardFixture(t)
			require.NoError(t, a.svc.AutoExport(ctx, a.mtixDir))
			older := a.read(t, "tasks.json")
			createTask(t, a, "PROJ-3", 3)
			require.NoError(t, a.svc.AutoExport(ctx, a.mtixDir))
			pushed := pushAll(t, a)
			created, err := a.store.GetNode(ctx, "PROJ-3")
			require.NoError(t, err)
			require.NotEmpty(t, created.UID)

			a.pull(t, tt.board(t, a, older))
			err = a.svc.AutoImport(ctx, a.mtixDir)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err, a.notices.String())
			}
			pullInto(t, a, pushed) // the next pull returns the pushed events
			for _, id := range tt.wantTasks {
				_, getErr := a.store.GetNode(ctx, id)
				assert.NoError(t, getErr, "%s is on the board after the pull", id)
			}
			var heldByUID int
			require.NoError(t, a.store.WriteDB().QueryRowContext(ctx,
				`SELECT COUNT(*) FROM nodes WHERE uid = ?`, created.UID).Scan(&heldByUID))
			assert.Equal(t, 1, heldByUID, "the task the log creates still holds its uid")
			missing, err := a.store.MissingLoggedNodes(ctx)
			require.NoError(t, err)
			assert.Empty(t, missing, "no event in the sync log is left without its task")
		})
	}
}

// setUID gives the node id of d the uid uid.
func setUID(d *sqlite.ExportData, id, uid string) {
	for i := range d.Nodes {
		if d.Nodes[i].ID == id {
			d.Nodes[i].UID = uid
		}
	}
}
