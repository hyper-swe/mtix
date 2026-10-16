// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type wakePermissionFixture struct {
	store *sqlite.Store
	state *oplocal.State
	svc   *service.WakePermissions
	board service.PermissionBoard
	now   time.Time
}

func newWakePermissionFixture(t *testing.T) *wakePermissionFixture {
	t.Helper()
	state := testutil.OperatorState(t)
	host, err := state.HostID()
	require.NoError(t, err)
	root := t.TempDir()
	st, err := sqlite.New(filepath.Join(root, "board.db"), nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	svc := service.NewWakePermissions(service.PermissionDeps{Store: st, State: state, HostID: host, UID: "fixture-uid", Clock: func() time.Time { return now }})
	return &wakePermissionFixture{store: st, state: state, svc: svc, now: now,
		board: service.PermissionBoard{ID: "11111111-1111-4111-8111-111111111111", Checkout: root,
			Hub: oplocal.HubIdentity{Kind: "postgres", ProjectPrefix: "TEST", FirstEventHash: strings.Repeat("a", 64)}}}
}

func (f *wakePermissionFixture) register(t *testing.T, agent string) {
	t.Helper()
	_, err := f.store.RegisterSessionAtomic(t.Context(), sqlite.RegistrationKey{AgentID: agent, HostID: f.svc.HostID, UID: f.svc.UID},
		func(_ *sqlite.Registration, high int64) (*sqlite.Registration, error) {
			return &sqlite.Registration{AgentID: agent, HostID: f.svc.HostID, UID: f.svc.UID, Harness: "fixture", Origin: "manual", Epoch: 1,
				HighWater: high, RegisteredAt: f.now.Format(time.RFC3339), LastSeen: f.now.Format(time.RFC3339), RegistrarVersion: "fixture", AttachProtocol: 1}, nil
		})
	require.NoError(t, err)
}

func (f *wakePermissionFixture) apply(t *testing.T, changes ...oplocal.PermissionChange) {
	t.Helper()
	require.NoError(t, f.svc.ApplyBatch(t.Context(), f.board, oplocal.PermissionBatch{Mode: "file", Source: "fixture orchestration", Markers: []string{"CODEX_THREAD_ID"}, Changes: changes}))
}

func TestWakeable_NeverConnectedBoard_AllRegisteredIDsAndUnregisteredHeld(t *testing.T) {
	f := newWakePermissionFixture(t)
	for _, id := range []string{"first", "second"} {
		f.register(t, id)
		ok, reason, err := f.svc.Wakeable(t.Context(), f.board, id)
		require.NoError(t, err)
		assert.True(t, ok)
		assert.Equal(t, "implicit local registration", reason)
	}
	ok, reason, err := f.svc.Wakeable(t.Context(), f.board, "not-registered")
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, "not_registered", reason)
}

func TestWakeable_ExplicitDeny_WinsOnLocalAndHubAndAllowReplacesIt(t *testing.T) {
	for _, hub := range []bool{false, true} {
		t.Run(map[bool]string{false: "local", true: "hub"}[hub], func(t *testing.T) {
			f := newWakePermissionFixture(t)
			f.register(t, "first")
			if hub {
				_, err := f.store.MarkHubConnected(t.Context(), "config")
				require.NoError(t, err)
			}
			f.apply(t, oplocal.PermissionChange{AgentID: "first", Action: "deny"})
			ok, reason, err := f.svc.Wakeable(t.Context(), f.board, "first")
			require.NoError(t, err)
			assert.False(t, ok)
			assert.Equal(t, "denied", reason)
			require.NoError(t, f.svc.ApplyBatch(t.Context(), f.board, oplocal.PermissionBatch{Mode: "yes", Source: "fixture operator", Changes: []oplocal.PermissionChange{{AgentID: "first", Action: "allow"}}}))
			ok, _, err = f.svc.Wakeable(t.Context(), f.board, "first")
			require.NoError(t, err)
			assert.True(t, ok)
		})
	}
}

func TestWakeable_HubBoardUnconfirmed_HeldAndStrictDefaultOff(t *testing.T) {
	f := newWakePermissionFixture(t)
	f.register(t, "first")
	_, err := f.store.MarkHubConnected(t.Context(), "config")
	require.NoError(t, err)
	ok, reason, err := f.svc.Wakeable(t.Context(), f.board, "first")
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, "unconfirmed", reason)
	f.apply(t, oplocal.PermissionChange{AgentID: "first", Action: "allow"})
	changed := f.board
	changed.Hub.FirstEventHash = strings.Repeat("b", 64)
	ok, reason, err = f.svc.Wakeable(t.Context(), changed, "first")
	require.NoError(t, err)
	assert.True(t, ok, "default board-only grant must survive hub identity change")
	assert.Equal(t, "confirmed", reason)
	require.NoError(t, f.state.WriteJSON("wake", map[string]any{"inbox.strict_board_identity": true}))
	for _, board := range []service.PermissionBoard{changed, {ID: f.board.ID}} {
		ok, reason, err = f.svc.Wakeable(t.Context(), board, "first")
		require.NoError(t, err)
		assert.False(t, ok)
		assert.Equal(t, "unconfirmed", reason)
	}
	ok, _, err = f.svc.Wakeable(t.Context(), f.board, "first")
	require.NoError(t, err)
	assert.True(t, ok, "strict exact known identity must match")
}

func TestApplyBatch_FileMode_ValidatesAllBeforeLogGrantOrNotice(t *testing.T) {
	f := newWakePermissionFixture(t)
	f.register(t, "first")
	err := f.svc.ApplyBatch(t.Context(), f.board, oplocal.PermissionBatch{Mode: "file", Source: "fixture",
		Changes: []oplocal.PermissionChange{{AgentID: "first", Action: "allow"}, {AgentID: "\x00invalid", Action: "deny"}}})
	require.ErrorIs(t, err, model.ErrInvalidInput)
	grants, err := f.state.AgentGrants()
	require.NoError(t, err)
	assert.Empty(t, grants)
	rows, err := f.state.Confirmations(context.Background())
	require.NoError(t, err)
	assert.Empty(t, rows)
	f.apply(t, oplocal.PermissionChange{AgentID: "first", Action: "allow"})
	rows, err = f.state.Confirmations(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "file", rows[0].Mode)
	assert.Equal(t, "fixture orchestration", rows[0].Source)
	assert.Equal(t, f.now, rows[0].Time)
	assert.Equal(t, []string{"CODEX_THREAD_ID"}, rows[0].Markers)
	assert.Equal(t, "skipped_no_node", rows[0].BoardLine)
}
