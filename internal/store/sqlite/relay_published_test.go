// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func publishedTestRow(t *testing.T, s *sqlite.Store) sqlite.RelayJournalEvent {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, s.CreateNode(t.Context(), makeRootNode("PROJ-1", "PROJ", "first", now)))
	rows, err := s.ReadRelayJournalSince(t.Context(), 0, 10)
	require.NoError(t, err)
	require.NotEmpty(t, rows)
	return rows[0]
}

// A store that published before the mark existed has a cursor row and no mark.
func TestIsRelayPublished_CursorWithoutMark(t *testing.T) {
	ctx := t.Context()
	s := newTestStore(t)
	row := publishedTestRow(t, s)
	require.NoError(t, s.AdvanceRelayPushCursor(ctx, row.Seq, 2))
	_, err := s.WriteDB().ExecContext(ctx, `DELETE FROM meta WHERE key = 'meta.relay.published_hwm'`)
	require.NoError(t, err)
	got, err := s.IsRelayPublished(ctx, row.Event.EventID)
	require.NoError(t, err)
	require.True(t, got)
}

// DiscardLocal empties the journal and its rowid space, so the mark goes too.
func TestDiscardLocal_ClearsRelayPublishedMark(t *testing.T) {
	ctx := t.Context()
	s := newTestStore(t)
	row := publishedTestRow(t, s)
	require.NoError(t, s.MarkRelayFraming(ctx, row.Seq))
	require.NoError(t, sqlite.DiscardLocal(ctx, s, discardMtixDir(t)))
	var n int
	require.NoError(t, s.WriteDB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM meta WHERE key = 'meta.relay.published_hwm'`).Scan(&n))
	require.Zero(t, n)
}

// The renumber that guards the publish mark holds a create the relay carries,
// in the same transaction that would move it, and renumbers one it does not.
func TestRenumberUnlessRelayPublished(t *testing.T) {
	ctx := t.Context()
	s := newTestStore(t)
	row := publishedTestRow(t, s)
	require.NoError(t, s.MarkRelayFraming(ctx, row.Seq))
	_, err := s.RenumberUnlessRelayPublished(ctx, row.Event.EventID)
	require.ErrorIs(t, err, sqlite.ErrRelayPublishedCreate)
	n, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	require.Equal(t, row.Event.EventID, n.UID)

	s2 := newTestStore(t)
	row2 := publishedTestRow(t, s2)
	newID, err := s2.RenumberUnlessRelayPublished(ctx, row2.Event.EventID)
	require.NoError(t, err)
	require.NotEqual(t, "PROJ-1", newID)
}
