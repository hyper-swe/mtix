// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func (f *historyFixture) insertEventRow(t *testing.T, entryID, key, event string) {
	t.Helper()
	_, err := f.db.Exec(`INSERT INTO task_history (entry_id, task_uid, ts, actor, origin, kind, field, old_value, new_value, event_id)
		VALUES (?, ?, ?, 'a', 'hub', 'change', 'status', NULL, NULL, ?)`,
		entryID, key, time.Now().UTC().Format(time.RFC3339Nano), event)
	require.NoError(t, err)
}

func (f *historyFixture) rowsKeyed(t *testing.T, key string) int {
	t.Helper()
	var n int
	require.NoError(t, f.db.QueryRow(`SELECT COUNT(*) FROM task_history WHERE task_uid = ?`, key).Scan(&n))
	return n
}

// TestTaskHistoryRekey_BackfillWithADuplicateEvent_DoesNotWedge: the same
// (event_id, field) under the id and under the uid is one entry; the backfill
// that runs on every migrate must still succeed, now and on every later run.
func TestTaskHistoryRekey_BackfillWithADuplicateEvent_DoesNotWedge(t *testing.T) {
	f := newHistoryFixture(t)
	seedCascadeNodes(t, f.s, ccNode(ccRoot, model.StatusOpen))
	// A legacy task: no uid, which the backfill takes from its create event.
	var uid string
	require.NoError(t, f.db.QueryRow(`SELECT event_id FROM sync_events WHERE node_id = ? AND op_type = 'create_node'`, ccRoot).Scan(&uid))
	_, err := f.db.Exec(`UPDATE nodes SET uid = NULL WHERE id = ?`, ccRoot)
	require.NoError(t, err)
	_, err = f.db.Exec(`DELETE FROM task_history`)
	require.NoError(t, err)
	f.insertEventRow(t, "e1", ccRoot, "EV1")
	f.insertEventRow(t, "e2", uid, "EV1")

	require.NoError(t, f.s.BackfillUIDs(context.Background()))
	require.NoError(t, f.s.BackfillUIDs(context.Background()), "and the next run")
	assert.Equal(t, 1, f.rowsKeyed(t, uid)+f.rowsKeyed(t, ccRoot), "one entry per event and field remains")
}

// TestTaskHistoryRekey_BackfillLeavesOtherKeysAlone: only the nodes the
// backfill filled move; an orphaned id-keyed row of a removed task stays where
// it is and is not adopted by whichever task holds that id now.
func TestTaskHistoryRekey_BackfillLeavesOtherKeysAlone(t *testing.T) {
	f := newHistoryFixture(t)
	seedCascadeNodes(t, f.s, ccNode(ccRoot, model.StatusOpen)) // has a uid already
	f.insertEventRow(t, "orphan", ccRoot, "EV9")

	require.NoError(t, f.s.BackfillUIDs(context.Background()))

	assert.Equal(t, 1, f.rowsKeyed(t, ccRoot), "the orphaned row keeps its key")
}

// TestTaskHistoryRekey_RenumberOntoAKeyHoldingTheSameEvent_DoesNotFail: a
// renumber moves id-keyed history to the new id even when that key already
// holds the same event's entry.
func TestTaskHistoryRekey_RenumberOntoAKeyHoldingTheSameEvent_DoesNotFail(t *testing.T) {
	f := newHistoryFixture(t)
	seedCascadeNodes(t, f.s, ccNode(ccRoot, model.StatusOpen))
	_, err := f.db.Exec(`DELETE FROM task_history`)
	require.NoError(t, err)
	f.insertEventRow(t, "e1", ccRoot, "EV1")
	f.insertEventRow(t, "e2", "PROJ-5", "EV1")

	require.NoError(t, f.s.RenumberSubtree(context.Background(), ccRoot, 5))

	assert.Equal(t, 1, f.rowsKeyed(t, "PROJ-5"))
	assert.Zero(t, f.rowsKeyed(t, ccRoot))
}
