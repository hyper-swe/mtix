// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"database/sql"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestImport_RestoredInbox_DoesNotImplyHubContact(t *testing.T) {
	source, raw := retentionStore(t)
	addressThree(t, source, "recipient")
	_, err := raw.Exec(`UPDATE sync_events SET payload=json_set(payload,'$.reply_by','2026-12-01T00:00:00Z') WHERE op_type='comment'`)
	require.NoError(t, err)
	file := transportExport(t, source)
	dest := newTestStore(t)
	_, err = dest.Import(t.Context(), file, sqlite.ImportModeMerge, false)
	require.NoError(t, err)
	synced, err := dest.HasSyncedWithHub(t.Context())
	require.NoError(t, err)
	assert.False(t, synced, "restore is not hub contact")
	inferred, err := dest.AppliedEventsAboveFloor(t.Context())
	require.NoError(t, err)
	assert.False(t, inferred, "restore must not cause hub mark inference")
	count, err := dest.CountSyncEvents(t.Context())
	require.NoError(t, err)
	assert.Zero(t, count, "restored mail cannot block first backfill")
	foreign, err := dest.PermissionAddressedAgents(t.Context())
	require.NoError(t, err)
	assert.Empty(t, foreign, "restore is not foreign permission evidence")
	replies, err := dest.ReplyByEvents(t.Context(), 0, 100)
	require.NoError(t, err)
	require.Len(t, replies, 3)
	for _, reply := range replies {
		assert.True(t, reply.Local, "restore must not require hub contact")
	}
	_, err = dest.Backfill(t.Context(), false)
	require.NoError(t, err)
}

func TestImport_RestoredInbox_RelayExcludesOnlyRestoredRows(t *testing.T) {
	source := newTestStore(t)
	addressThree(t, source, "recipient")
	dest := newTestStore(t)
	addressTo(t, dest, "local-recipient", "PROJ-9")
	before, err := dest.ReadRelayJournalSince(t.Context(), 0, 100)
	require.NoError(t, err)
	require.NotEmpty(t, before)
	_, err = dest.Import(t.Context(), transportExport(t, source), sqlite.ImportModeMerge, false)
	require.NoError(t, err)
	after, err := dest.ReadRelayJournalSince(t.Context(), 0, 100)
	require.NoError(t, err)
	require.Equal(t, before, after, "restore neither publishes mail nor skips unrelated local work")
	addressTo(t, dest, "local-recipient", "PROJ-10")
	later, err := dest.ReadRelayJournalSince(t.Context(), before[len(before)-1].Seq, 100)
	require.NoError(t, err)
	require.NotEmpty(t, later)
	for _, event := range later {
		require.Equal(t, "PROJ-10", event.Event.NodeID)
	}
}

func TestImport_SameEventMirroredBySync_AcceptsStorageTimeDifference(t *testing.T) {
	source := newTestStore(t)
	source.SetClock(func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) })
	addressThree(t, source, "recipient")
	events, err := source.ReadPendingEvents(t.Context(), 100)
	require.NoError(t, err)
	require.NotEmpty(t, events)
	dest := newTestStore(t)
	dest.SetClock(func() time.Time { return time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC) })
	for _, event := range events {
		require.NoError(t, dest.WithTx(t.Context(), func(tx *sql.Tx) error { return dest.IdempotentApply(t.Context(), tx, event) }))
	}
	_, err = dest.Import(t.Context(), transportExport(t, source), sqlite.ImportModeMerge, false)
	require.NoError(t, err)
	got, err := dest.InboxList(t.Context(), "recipient")
	require.NoError(t, err)
	require.Len(t, got, 3)
}
