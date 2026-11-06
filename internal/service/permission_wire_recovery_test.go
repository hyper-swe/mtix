// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplyBatch_PendingPublication_PeerSnapshotReopenRecoversSameWireID(t *testing.T) {
	cases := []struct {
		name     string
		mode     sqlite.ImportMode
		resolve  bool
		wireTime bool
		legacy   bool
	}{
		{"replace", sqlite.ImportModeReplace, false, true, false},
		{"merge_keeps_local", sqlite.ImportModeMerge, false, false, false},
		{"merge_resolved_peer", sqlite.ImportModeMerge, true, true, false},
		{"replace_legacy", sqlite.ImportModeReplace, false, true, true},
		{"merge_keeps_local_legacy", sqlite.ImportModeMerge, false, false, true},
		{"merge_resolved_peer_legacy", sqlite.ImportModeMerge, true, true, true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			f := newWakePermissionFixture(t)
			f.now = f.now.Add(123456789 * time.Nanosecond)
			f.svc.Clock = func() time.Time { return f.now }
			f.register(t, "first")
			permissionRoot(t, f, "TEST-1")
			f.svc.Store = &permissionFaultStore{PermissionStore: f.store, after: true, fail: true}
			batch := oplocal.PermissionBatch{ID: "0123456789abcdef0123456789abcdef", Mode: "file", Source: "fixture", Changes: []oplocal.PermissionChange{{AgentID: "first", Action: "deny"}}}
			require.Error(t, f.svc.ApplyBatch(t.Context(), f.board, batch))
			assertPendingWireBatch(t, f, batch.ID)
			publicationID := permissionAcceptedPublicationID(t, f, batch.ID)
			peer, wireTime := ordinaryPermissionPeer(t, f, publicationID, test.legacy)
			if test.resolve {
				require.NoError(t, peer.ResolveAnnotation(t.Context(), "TEST-1", publicationID, true))
			}
			data := permissionSnapshotBytes(t, peer)
			_, err := f.store.Import(t.Context(), data, test.mode, false)
			require.NoError(t, err)
			reopenPermissionSource(t, f)
			node, err := f.store.GetNode(t.Context(), "TEST-1")
			require.NoError(t, err)
			require.Len(t, node.Annotations, 1)
			wantTime := f.now
			if test.wireTime {
				wantTime = wireTime
			}
			assert.Equal(t, wantTime, node.Annotations[0].CreatedAt)
			assert.Equal(t, test.resolve, node.Annotations[0].Resolved)
			assertPendingWireBatch(t, f, batch.ID)
			require.NoError(t, f.svc.ApplyBatch(t.Context(), f.board, batch), "recovery must accept the existing publication reconstructed by ordinary sync")
			assertRecoveredPermission(t, f, batch.ID)
			assert.Equal(t, 1, permissionWireEventCount(t, f.store, publicationID))
		})
	}
}

func ordinaryPermissionPeer(t *testing.T, f *wakePermissionFixture, id string, legacy bool) (*sqlite.Store, time.Time) {
	t.Helper()
	peer, err := sqlite.New(filepath.Join(t.TempDir(), "peer.db"), nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, peer.Close()) })
	events, err := f.store.ReadPendingEvents(t.Context(), 100)
	require.NoError(t, err)
	require.Len(t, events, 2, "the actual emitter produced one create and one ordinary comment")
	var wireTime time.Time
	for _, event := range events {
		if event.EventID == id {
			assert.Equal(t, model.OpComment, event.OpType)
			wireTime = permissionEventTimestamp(t, event, legacy)
		}
		require.NoError(t, peer.WithTx(t.Context(), func(tx *sql.Tx) error { return peer.IdempotentApply(t.Context(), tx, event) }))
	}
	node, err := peer.GetNode(t.Context(), "TEST-1")
	require.NoError(t, err)
	require.Len(t, node.Annotations, 1)
	assertWireNotice(t, node.Annotations[0], id)
	assert.Equal(t, "mtix-permissions", node.Annotations[0].Author)
	assert.Empty(t, node.Annotations[0].Addressee)
	assert.Equal(t, wireTime, node.Annotations[0].CreatedAt)
	if legacy {
		assert.False(t, f.now.Equal(wireTime), "legacy wire reconstruction differs from the original batch timestamp")
	} else {
		assert.Equal(t, f.now, wireTime, "new annotation metadata preserves the original timestamp")
	}
	return peer, wireTime
}

// Keep the historical fallback scenarios: only remove the additive metadata
// to model an old writer. A new event carries its original annotation time.
func permissionEventTimestamp(t *testing.T, event *model.SyncEvent, legacy bool) time.Time {
	t.Helper()
	var payload model.CommentPayload
	require.NoError(t, json.Unmarshal(event.Payload, &payload))
	require.NotNil(t, payload.Annotation, "the current emitter must carry annotation metadata")
	if !legacy {
		return payload.Annotation.CreatedAt
	}
	var old map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(event.Payload, &old))
	delete(old, "annotation")
	var err error
	event.Payload, err = json.Marshal(old)
	require.NoError(t, err)
	return time.UnixMilli(event.WallClockTS).UTC()
}

func permissionSnapshotBytes(t *testing.T, peer *sqlite.Store) *sqlite.ExportData {
	t.Helper()
	exported, err := peer.Export(t.Context(), "TEST", "fixture-current")
	require.NoError(t, err)
	raw, err := json.Marshal(exported)
	require.NoError(t, err)
	data, err := sqlite.DecodeExportData(bytes.NewReader(raw))
	require.NoError(t, err)
	require.NoError(t, sqlite.ValidateExport(data))
	return data
}

func reopenPermissionSource(t *testing.T, f *wakePermissionFixture) {
	t.Helper()
	require.NoError(t, f.store.Close())
	reopened, err := sqlite.New(filepath.Join(f.board.Checkout, "board.db"), nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	f.store = reopened
	f.svc.Store = reopened
}

func assertPendingWireBatch(t *testing.T, f *wakePermissionFixture, id string) {
	t.Helper()
	rows, err := f.state.Confirmations(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, id, rows[0].ID)
	assert.Equal(t, f.now, rows[0].Time)
	assert.Equal(t, "deny", rows[0].Action)
	assert.True(t, rows[0].Pending)
	assert.Equal(t, "pending", rows[0].BoardLine)
	node, err := f.store.GetNode(t.Context(), "TEST-1")
	require.NoError(t, err)
	require.Len(t, node.Annotations, 1)
	publicationID := permissionAcceptedPublicationID(t, f, id)
	assertWireNotice(t, node.Annotations[0], publicationID)
	assert.Equal(t, 1, permissionWireEventCount(t, f.store, publicationID))
	var total int
	require.NoError(t, f.store.QueryRow(t.Context(), `SELECT COUNT(*) FROM sync_events WHERE op_type = ?`, model.OpComment).Scan(&total))
	assert.Equal(t, 1, total)
}

func permissionWireEventCount(t *testing.T, st *sqlite.Store, id string) int {
	t.Helper()
	var count int
	require.NoError(t, st.QueryRow(t.Context(), `SELECT COUNT(*) FROM sync_events WHERE event_id = ? AND op_type = ?`, id, model.OpComment).Scan(&count))
	return count
}

func assertWireNotice(t *testing.T, annotation model.Annotation, id string) {
	t.Helper()
	assert.Equal(t, id, annotation.ID)
	assert.Equal(t, service.PermissionNotice, annotation.Text)
	assert.Equal(t, "mtix-permissions", annotation.Author)
	assert.Empty(t, annotation.Addressee)
}
