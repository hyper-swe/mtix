// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// A held creation whose task no node's uid finds (MTIX-95.12 run 2, review
// r1 S1 and S2). A merge import can give a task the file's uid
// (MTIX-95.31.6, 95.31.9), and a creation queued before events carried
// uids has none, its task's uid being the event's own id. The task's later
// changes and its subtree must still be held while its creation is.

// adoptFileUID gives nodeID the file's uid through a real merge import
// (MTIX-95.31.6, 95.31.9): this store's board, with nodeID under a marked
// backfill uid, is merged back. The two carry the same creation time and
// one uid was assigned later, so the merge treats them as the same task and
// adopts the file's uid. It returns that uid.
func adoptFileUID(t *testing.T, nodeID string) string {
	t.Helper()
	ctx := context.Background()
	data, err := app.store.Export(ctx, "", "")
	require.NoError(t, err)
	uid, err := model.NewBackfillUID()
	require.NoError(t, err)
	found := false
	for i := range data.Nodes {
		if data.Nodes[i].ID == nodeID {
			data.Nodes[i].UID, found = uid, true
		}
	}
	require.True(t, found, "%s is on the board", nodeID)
	board, err := json.Marshal(data)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "board.json")
	require.NoError(t, os.WriteFile(path, board, 0o600))
	require.NoError(t, runImport(path, importFlags{mode: "merge", recomputeChecksum: true}))
	node, err := app.store.GetNode(ctx, nodeID)
	require.NoError(t, err)
	require.Equal(t, uid, node.UID, "the merge adopted the file's uid")
	return uid
}

// queueWithoutUID makes nodeID's creation one queued before events carried
// a uid: its event's uid is NULL, and the task's uid is the event's own id,
// as the pre-v3 backfill sets it (backfillUIDsFromCreateEvents). It returns
// the creation's event id.
func queueWithoutUID(t *testing.T, nodeID string) string {
	t.Helper()
	ctx := context.Background()
	id := eventIDFor(t, nodeID, model.OpCreateNode)
	_, err := app.store.WriteDB().ExecContext(ctx, `UPDATE sync_events SET uid = NULL WHERE event_id = ?`, id)
	require.NoError(t, err)
	_, err = app.store.WriteDB().ExecContext(ctx, `UPDATE nodes SET uid = ? WHERE id = ?`, id, nodeID)
	require.NoError(t, err)
	return id
}

// TestPushLoop_HeldCreationWhoseUIDNoNodeHas_TaskAndSubtreeHeld is the
// review r1 S1 and S2 probe: a size-held creation of TEST-1 whose task no
// node's uid finds, because a merge import adopted the file's uid for it,
// or because the creation was queued without a uid. An edit made before
// that, and an edit, a comment, a child creation and the child's edit made
// after it, are all held, over two pushes, whether the creation was first
// held before or in the same push; an edit of another task is pushed. When
// the task is then deleted with its subtree (review r2 S2), the delete is
// held too: a soft-deleted task still counts as the task found by the
// number its creation names.
func TestPushLoop_HeldCreationWhoseUIDNoNodeHas_TaskAndSubtreeHeld(t *testing.T) {
	adopt := func(t *testing.T) { adoptFileUID(t, "TEST-1") }
	queue := func(t *testing.T) { queueWithoutUID(t, "TEST-1") }
	tests := []struct {
		name       string
		pushFirst  bool // a push holds the creation before its task loses its uid
		loseUID    func(t *testing.T)
		deleteTask bool // mtix delete TEST-1 --cascade after the other changes
	}{
		{"uid adopted by a merge, creation held before", true, adopt, false},
		{"uid adopted by a merge, creation held in the same push", false, adopt, false},
		{"uid adopted by a merge, task deleted, creation held before", true, adopt, true},
		{"uid adopted by a merge, task deleted, creation held in the same push", false, adopt, true},
		{"creation queued without a uid, held before", true, queue, false},
		{"creation queued without a uid, held in the same push", false, queue, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			require.NoError(t, runCreate("held", "", "", 3, "", overWireCap(), "", "", ""))
			require.NoError(t, runCreate("other", "", "", 3, "", "", "", "", ""))
			require.NoError(t, runUpdate("TEST-1", "edit before", "", "", "", 0, "", ""))
			root := eventIDFor(t, "TEST-1", model.OpCreateNode)
			before := eventIDFor(t, "TEST-1", model.OpUpdateField)
			hub := newFakePushHub()
			if tt.pushFirst {
				require.NoError(t, pushWith(t, hub))
			}
			tt.loseUID(t)
			require.NoError(t, runUpdate("TEST-1", "edit after", "", "", "", 0, "", ""))
			require.NoError(t, runComment("TEST-1", "comment after", ""))
			require.NoError(t, runCreate("child", "TEST-1", "", 3, "", "", "", "", ""))
			require.NoError(t, runUpdate("TEST-1.1", "child edit", "", "", "", 0, "", ""))
			require.NoError(t, runUpdate("TEST-2", "unrelated edit", "", "", "", 0, "", ""))
			child := eventIDFor(t, "TEST-1.1", model.OpCreateNode)
			if tt.deleteTask {
				require.NoError(t, runDelete("TEST-1", true))
			}

			for push := 1; push <= 2; push++ {
				require.NoError(t, pushWith(t, hub), "push %d", push)
				requireHeldDependent(t, hub, "TEST-1", model.OpUpdateField, root)
				requireHeldDependent(t, hub, "TEST-1", model.OpComment, root)
				requireHeldDependent(t, hub, "TEST-1.1", model.OpCreateNode, root)
				requireHeldDependent(t, hub, "TEST-1.1", model.OpUpdateField, child)
				_, held := quarantined(t)[before]
				require.True(t, held, "the edit made before is held")
				require.False(t, hub.sent(before))
				require.False(t, hub.sent(root), "the creation stays held")
				if tt.deleteTask {
					requireHeldDependent(t, hub, "TEST-1", model.OpDelete, root)
				}
			}
			requireSent(t, hub, "TEST-2", model.OpUpdateField)
		})
	}
}
