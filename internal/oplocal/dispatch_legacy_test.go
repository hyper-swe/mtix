//go:build !windows

// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestDispatchBoards_LegacyListingPreserved(t *testing.T) {
	s, loc := fixture(t), laneFixture(t)
	require.NoError(t, s.ListBoard(t.Context(), loc.BoardKey, loc.Root))
	require.NoError(t, s.WriteJSON("boards", DispatchBoards{}))
	legacy := []ListedBoard{{Key: loc.BoardKey, Path: loc.Root}}
	require.NoError(t, s.WriteJSON("listed-boards", legacy))
	listed, err := s.ListedBoards(t.Context())
	require.NoError(t, err)
	require.Equal(t, legacy, listed)
	registry, err := s.DispatchBoards(t.Context())
	require.NoError(t, err)
	require.Equal(t, legacy, registry.Listed)
	require.NoError(t, s.ListBoard(t.Context(), loc.BoardKey, loc.Root))
	listed, err = s.ListedBoards(t.Context())
	require.NoError(t, err)
	require.Equal(t, legacy, listed)
	require.Empty(t, registry.Enrolled)
}

func TestListBoardInstance_FallbackRekey_PreservesLegacyAndBinding(t *testing.T) {
	s, loc := fixture(t), laneFixture(t)
	instance := "22222222-2222-4222-8222-222222222222"
	original := ListedBoard{Key: instance, Path: loc.Root, StoreInstanceID: instance}
	require.NoError(t, s.ListBoardInstance(t.Context(), original))
	require.NoError(t, s.WriteJSON("listed-boards", []ListedBoard{{Key: instance, Path: loc.Root}}))
	audit := EnrollmentAudit{Path: loc.Root, Action: "enroll", Mode: "yes", Source: "test", At: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	entry := EnrolledBoard{Path: loc.Root, BoardID: instance, StoreInstanceID: instance}
	require.NoError(t, s.SetEnrollment(t.Context(), &entry, audit))
	updated := ListedBoard{Key: loc.BoardKey, Path: loc.Root, StoreInstanceID: instance}
	require.NoError(t, s.ListBoardInstance(t.Context(), updated))
	rows, err := s.ListedBoards(t.Context())
	require.NoError(t, err)
	require.Equal(t, []ListedBoard{updated}, rows)
	registry, err := s.DispatchBoards(t.Context())
	require.NoError(t, err)
	require.Len(t, registry.Enrolled, 1)
	require.Equal(t, loc.BoardKey, registry.Enrolled[0].BoardID)
	require.Equal(t, instance, registry.Enrolled[0].StoreInstanceID)
	require.Equal(t, []EnrollmentAudit{audit}, registry.History)
	changed := updated
	changed.Key = "33333333-3333-4333-8333-333333333333"
	require.Error(t, s.ListBoardInstance(t.Context(), changed))
	changed = updated
	changed.StoreInstanceID = "44444444-4444-4444-8444-444444444444"
	require.Error(t, s.ListBoardInstance(t.Context(), changed))
	rows, err = s.ListedBoards(t.Context())
	require.NoError(t, err)
	require.Equal(t, []ListedBoard{updated}, rows)
}

func TestSetEnrollment_MismatchedInstance_Refuses(t *testing.T) {
	s, loc := fixture(t), laneFixture(t)
	instance := "22222222-2222-4222-8222-222222222222"
	require.NoError(t, s.ListBoardInstance(t.Context(), ListedBoard{Key: loc.BoardKey, Path: loc.Root, StoreInstanceID: instance}))
	audit := EnrollmentAudit{Path: loc.Root, Action: "enroll", Mode: "yes", Source: "test", At: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	entry := EnrolledBoard{Path: loc.Root, BoardID: loc.BoardKey, StoreInstanceID: instance}
	require.NoError(t, s.SetEnrollment(t.Context(), &entry, audit))
	for _, wrong := range []string{"", "33333333-3333-4333-8333-333333333333"} {
		entry.StoreInstanceID = wrong
		require.Error(t, s.SetEnrollment(t.Context(), &entry, audit))
	}
	registry, err := s.DispatchBoards(t.Context())
	require.NoError(t, err)
	require.Len(t, registry.Enrolled, 1)
	require.Equal(t, instance, registry.Enrolled[0].StoreInstanceID)
}
