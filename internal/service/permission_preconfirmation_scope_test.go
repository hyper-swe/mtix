// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"path/filepath"
	"testing"
	"time"
)

func TestWakeable_Preconfirmation_ForeignRegistrationCannotActivate(t *testing.T) {
	for _, scope := range []string{"host", "uid", "board"} {
		t.Run(scope, func(t *testing.T) {
			f := newWakePermissionFixture(t)
			f.apply(t, oplocal.PermissionChange{AgentID: "future-fixture", Action: "allow"})
			st, host, uid := f.store, f.svc.HostID, f.svc.UID
			if scope == "host" {
				host = "foreign-fixture-host"
			}
			if scope == "uid" {
				uid = "foreign-fixture-uid"
			}
			if scope == "board" {
				var err error
				st, err = sqlite.New(filepath.Join(t.TempDir(), "other.db"), nil)
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, st.Close()) })
			}
			registerPermissionScope(t, st, host, uid)
			ok, reason, err := f.svc.Wakeable(t.Context(), f.board, "future-fixture")
			require.NoError(t, err)
			assert.False(t, ok)
			assert.Equal(t, "not_registered", reason)
			pending, err := f.svc.PendingRegistrations(t.Context(), f.board)
			require.NoError(t, err)
			assert.Equal(t, []string{"future-fixture"}, pending)
		})
	}
}

func registerPermissionScope(t *testing.T, st *sqlite.Store, host, uid string) {
	t.Helper()
	stamp := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC).Format(time.RFC3339)
	_, err := st.RegisterSessionAtomic(t.Context(), sqlite.RegistrationKey{AgentID: "future-fixture", HostID: host, UID: uid},
		func(_ *sqlite.Registration, high int64) (*sqlite.Registration, error) {
			return &sqlite.Registration{AgentID: "future-fixture", HostID: host, UID: uid, Harness: "fixture", Origin: "manual", Epoch: 1,
				HighWater: high, RegisteredAt: stamp, LastSeen: stamp, RegistrarVersion: "fixture", AttachProtocol: 1}, nil
		})
	require.NoError(t, err)
}

func TestWakeable_Preconfirmation_ExactKnownHubActivatesAndDifferentBoardDoesNot(t *testing.T) {
	f := newWakePermissionFixture(t)
	_, err := f.store.MarkHubConnected(t.Context(), "config")
	require.NoError(t, err)
	f.apply(t, oplocal.PermissionChange{AgentID: "future-fixture", Action: "allow"})
	f.register(t, "future-fixture")
	ok, reason, err := f.svc.Wakeable(t.Context(), f.board, "future-fixture")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "confirmed", reason)
	other := service.PermissionBoard{ID: "22222222-2222-4222-8222-222222222222", Hub: f.board.Hub}
	ok, reason, err = f.svc.Wakeable(t.Context(), other, "future-fixture")
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, "unconfirmed", reason)
}
