// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func registerForStale(t *testing.T, s *sqlite.Store, epoch int64) {
	t.Helper()
	_, err := s.RegisterSessionAtomic(context.Background(), keyFor("w"), func(*sqlite.Registration, int64) (*sqlite.Registration, error) {
		return &sqlite.Registration{AgentID: "w", HostID: "host-a", UID: "501", Harness: "codex", Origin: "manual", Epoch: epoch,
			RegisteredAt: "2026-10-12T00:00:00Z", LastSeen: "2026-10-12T00:00:00Z", RegistrarVersion: "t", AttachProtocol: 1}, nil
	})
	require.NoError(t, err)
}

func staleSince(t *testing.T, s *sqlite.Store) string {
	t.Helper()
	rows, err := s.ListRegistrations(context.Background(), "host-a", "501")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	return rows[0].StaleSince
}

func TestMarkRegistrationStale_FreshRow_SetsMarkerOnce(t *testing.T) {
	s := newRegistrationStore(t)
	registerForStale(t, s, 1)
	require.Empty(t, staleSince(t, s))
	n, err := s.MarkRegistrationStale(context.Background(), keyFor("w"), 1, "2026-10-28T00:00:00Z")
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	require.Equal(t, "2026-10-28T00:00:00Z", staleSince(t, s))
	// A later scan must not move the first-seen-dead time.
	n, err = s.MarkRegistrationStale(context.Background(), keyFor("w"), 1, "2026-10-29T00:00:00Z")
	require.NoError(t, err)
	require.Zero(t, n)
	require.Equal(t, "2026-10-28T00:00:00Z", staleSince(t, s))
}

func TestMarkRegistrationStale_OtherEpoch_LeavesRowUntouched(t *testing.T) {
	s := newRegistrationStore(t)
	registerForStale(t, s, 2)
	n, err := s.MarkRegistrationStale(context.Background(), keyFor("w"), 1, "2026-10-28T00:00:00Z")
	require.NoError(t, err)
	require.Zero(t, n, "a scanner holding an old epoch must not mark a re-registered session")
	require.Empty(t, staleSince(t, s))
}

func TestMarkRegistrationStale_ReRegister_ClearsMarker(t *testing.T) {
	s := newRegistrationStore(t)
	registerForStale(t, s, 1)
	_, err := s.MarkRegistrationStale(context.Background(), keyFor("w"), 1, "2026-10-28T00:00:00Z")
	require.NoError(t, err)
	registerForStale(t, s, 2)
	require.Empty(t, staleSince(t, s))
}

func TestMarkRegistrationStale_ClosedStore_ReturnsError(t *testing.T) {
	s := newRegistrationStore(t)
	require.NoError(t, s.Close())
	_, err := s.MarkRegistrationStale(context.Background(), keyFor("w"), 1, "x")
	require.Error(t, err)
}
