// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package sqlite_test

import (
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func TestRecover_InboxOperationAndArrivalOrder_PreservesOnlyOrderedMail(t *testing.T) {
	source, raw := retentionStore(t)
	addressThree(t, source, "recipient")
	for i, id := range []string{"zz-later", "aa-later", "not-a-comment"} {
		op := "comment"
		if i == 2 {
			op = "update_field"
		}
		_, err := raw.Exec(`INSERT INTO sync_events(event_id,project_prefix,node_id,op_type,payload,wall_clock_ts,lamport_clock,vector_clock,author_id,author_machine_hash,created_at) VALUES(?, 'PROJ','PROJ-1',?,json_object('to','recipient','body',?),0,1,'{}','sender','','2026-11-05T00:00:00Z')`, id, op, fmt.Sprint(i))
		require.NoError(t, err)
	}
	before, err := source.InboxList(t.Context(), "recipient")
	require.NoError(t, err)
	require.Len(t, before, 5)
	path, _, _ := transportBackupPage(t, source, "sync_events")
	result, err := sqlite.Recover(t.Context(), path, filepath.Join(t.TempDir(), "absent.json"), "test", sqlite.RecoverOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	require.NoError(t, err)
	require.NotNil(t, result.Export.InboxJournal)
	require.Len(t, result.Export.InboxJournal.Events, len(before))
	for i, event := range result.Export.InboxJournal.Events {
		require.Equal(t, before[i].EventID, event.EventID)
	}
	require.False(t, result.InboxIncomplete)
}

func TestRecover_SparseInboxRowids_SkipsHealthyGaps(t *testing.T) {
	source, raw := retentionStore(t)
	addressThree(t, source, "recipient")
	_, err := raw.Exec(`UPDATE sync_events SET rowid=1000000000 WHERE rowid=(SELECT MAX(rowid) FROM sync_events)`)
	require.NoError(t, err)
	path, _, _ := transportBackupPage(t, source, "sync_events")
	result, err := sqlite.Recover(t.Context(), path, filepath.Join(t.TempDir(), "absent.json"), "test", sqlite.RecoverOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	require.NoError(t, err)
	require.NotNil(t, result.Export.InboxJournal)
	require.Len(t, result.Export.InboxJournal.Events, 3)
	require.False(t, result.InboxIncomplete)
}
