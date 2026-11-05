// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package sqlite_test

import (
	"database/sql"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestImportReconcile_NoncommentInboxCollision_RejectsBeforeBackup(t *testing.T) {
	source := newTestStore(t)
	addressThree(t, source, "recipient")
	file := transportExport(t, source)
	dest, raw := retentionStore(t)
	_, err := dest.Import(t.Context(), file, sqlite.ImportModeMerge, false)
	require.NoError(t, err)
	_, err = raw.Exec(`UPDATE sync_events SET op_type='set_prompt' WHERE event_id=?`, file.InboxJournal.Events[0].EventID)
	require.NoError(t, err)
	backups := 0
	_, _, err = dest.ImportReconcile(t.Context(), file, sqlite.ImportReconcileOptions{Mode: sqlite.ImportModeMerge, BeforeWrite: func() error { backups++; return nil }})
	require.ErrorIs(t, err, model.ErrConflict)
	require.Zero(t, backups)
}

func TestRestoredInbox_NoncommentPullCollision_RollsBack(t *testing.T) {
	source := newTestStore(t)
	addressThree(t, source, "recipient")
	file := transportExport(t, source)
	dest, raw := retentionStore(t)
	_, err := dest.Import(t.Context(), file, sqlite.ImportModeMerge, false)
	require.NoError(t, err)
	events, err := source.ReadPendingEvents(t.Context(), 100)
	require.NoError(t, err)
	var event *model.SyncEvent
	for _, e := range events {
		if e.OpType == model.OpComment {
			event = e
			break
		}
	}
	require.NotNil(t, event)
	event.OpType = model.OpSetPrompt
	before, err := dest.ExportWithInbox(t.Context(), "", "")
	require.NoError(t, err)
	err = dest.WithTx(t.Context(), func(tx *sql.Tx) error { return dest.IdempotentApply(t.Context(), tx, event) })
	require.ErrorIs(t, err, model.ErrConflict)
	after, err := dest.ExportWithInbox(t.Context(), "", "")
	require.NoError(t, err)
	require.Equal(t, before.Checksum, after.Checksum)
	require.Equal(t, before.InboxJournal, after.InboxJournal)
	var marked int
	require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM inbox_restored_events`).Scan(&marked))
	require.Equal(t, 3, marked)
}
