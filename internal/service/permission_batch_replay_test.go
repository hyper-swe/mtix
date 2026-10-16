// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplyBatch_FileIDReplay_DerivedTimeAndRegistrationDoNotChangeCallerIntent(t *testing.T) {
	cases := []struct {
		name    string
		future  bool
		advance bool
	}{
		{"clock_advance", false, true},
		{"registration", true, false},
		{"clock_and_registration", true, true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			f := newWakePermissionFixture(t)
			f.svc.Clock = func() time.Time { return f.now }
			if !test.future {
				f.register(t, "first")
			}
			permissionRoot(t, f, "TEST-1")
			batch := filePermissionReplayBatch()
			require.NoError(t, f.svc.ApplyBatch(t.Context(), f.board, batch))
			rows, err := f.state.Confirmations(t.Context())
			require.NoError(t, err)
			require.Len(t, rows, 1)
			original := rows[0]
			if test.advance {
				f.now = f.now.Add(time.Hour)
			}
			if test.future {
				f.register(t, "first")
			}
			assert.False(t, batch.Changes[0].PendingRegistration, "server-derived flags never rewrite caller input")
			assert.True(t, batch.Time.IsZero(), "the same original caller request has no server timestamp")
			require.NoError(t, f.svc.ApplyBatch(t.Context(), f.board, batch), "identical file batch id and caller intent must be idempotent after derived state changes")
			assertFilePermissionReplay(t, f, original)
		})
	}
}

func filePermissionReplayBatch() oplocal.PermissionBatch {
	return oplocal.PermissionBatch{ID: "0123456789abcdef0123456789abcdef", Mode: "file", Source: "fixture caller",
		Markers: []string{"CODEX_THREAD_ID"}, Changes: []oplocal.PermissionChange{{AgentID: "first", Action: "allow"}}}
}

func assertFilePermissionReplay(t *testing.T, f *wakePermissionFixture, original oplocal.Confirmation) {
	t.Helper()
	rows, err := f.state.Confirmations(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, original, rows[0], "replay keeps immutable accepted time, projection and audit metadata")
	grants, err := f.state.AgentGrants()
	require.NoError(t, err)
	require.Len(t, grants, 1)
	assert.Equal(t, original.Time, grants[0].ConfirmedAt)
	assert.Equal(t, original.PendingRegistration, grants[0].PendingRegistration)
	node, err := f.store.GetNode(t.Context(), "TEST-1")
	require.NoError(t, err)
	require.Len(t, node.Annotations, 1)
	assertWireNotice(t, node.Annotations[0], permissionAcceptedPublicationID(t, f, original.ID))
	var total int
	require.NoError(t, f.store.QueryRow(t.Context(), `SELECT COUNT(*) FROM sync_events WHERE op_type = ?`, model.OpComment).Scan(&total))
	assert.Equal(t, 1, total)
}

func TestApplyBatch_FileIDReplay_ChangedCallerIntentStillRefused(t *testing.T) {
	for _, field := range []string{"board", "action", "mode", "source", "markers"} {
		t.Run(field, func(t *testing.T) {
			f := newWakePermissionFixture(t)
			f.register(t, "first")
			permissionRoot(t, f, "TEST-1")
			original := filePermissionReplayBatch()
			require.NoError(t, f.svc.ApplyBatch(t.Context(), f.board, original))
			rows, err := f.state.Confirmations(t.Context())
			require.NoError(t, err)
			require.Len(t, rows, 1)
			changed, board := filePermissionReplayBatch(), f.board
			switch field {
			case "board":
				board.ID = "22222222-2222-4222-8222-222222222222"
			case "action":
				changed.Changes[0].Action = "deny"
			case "mode":
				changed.Mode = "yes"
			case "source":
				changed.Source = "different fixture caller"
			case "markers":
				changed.Markers = []string{"PI_FUTURE"}
			}
			err = f.svc.ApplyBatch(t.Context(), board, changed)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "permission batch identity conflicts")
			assertFilePermissionReplay(t, f, rows[0])
		})
	}
}
