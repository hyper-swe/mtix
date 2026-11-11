// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// MTIX-112.3: a released 0.5.4 store has none of the delivery tables and a
// read-only open cannot create them, so the readers must answer empty or
// unknown, never an error.
func TestDeliveryReaders_ReadOnlyOpenWithoutTables_ReturnUnknownNotError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	s, err := New(path, slog.Default())
	require.NoError(t, err)
	_, err = s.WriteDB().ExecContext(t.Context(), `INSERT INTO agent_inbox_cursor(agent_id,cursor) VALUES('a',5)`)
	require.NoError(t, err)
	for _, drop := range []string{
		`DROP TABLE agent_inbox_reads`,
		`DROP TABLE agent_inbox_cursor_marks`,
		`DROP TABLE agent_inbox_waiters`,
	} {
		_, err = s.WriteDB().ExecContext(t.Context(), drop)
		require.NoError(t, err)
	}
	require.NoError(t, s.Close())
	ro, err := openReadOnlyStore(t.Context(), path, slog.Default(), defaultOpenRetry(), "macfuse")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ro.Close() })

	info, err := ro.InboxAckInfo(t.Context(), "a", 3)
	require.NoError(t, err)
	require.Equal(t, AckInfo{Kind: AckWatermark}, info, "covered by the watermark, time unknown")
	info, err = ro.InboxAckInfo(t.Context(), "a", 9)
	require.NoError(t, err)
	require.Equal(t, AckNone, info.Kind)
	last, err := ro.InboxLastRead(t.Context(), "a")
	require.NoError(t, err)
	require.Nil(t, last)
	parked, err := ro.InboxParked(t.Context(), "a")
	require.NoError(t, err)
	require.False(t, parked.Parked)
	var count int
	require.NoError(t, ro.QueryRow(t.Context(), `SELECT COUNT(*) FROM sqlite_master WHERE name LIKE 'agent_inbox_waiters'`).Scan(&count))
	require.Zero(t, count, "reads cannot create the missing tables")
}

// MTIX-112.3: InboxAckInfo's reads share one snapshot, so a write between its
// queries cannot mix two states.
func TestInboxAckInfo_ConcurrentAckBetweenReads_SeesOneSnapshot(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "snap.db"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	s.ackInfoAfterFirstRead = func(ctx context.Context) {
		// A cumulative ack lands after the ack-row read and before the cursor read.
		_, werr := s.WriteDB().ExecContext(ctx, `INSERT INTO agent_inbox_cursor(agent_id,cursor) VALUES('a',5)`)
		require.NoError(t, werr)
	}
	info, err := s.InboxAckInfo(t.Context(), "a", 3)
	require.NoError(t, err)
	require.Equal(t, AckNone, info.Kind, "the snapshot predates the concurrent ack")
	s.ackInfoAfterFirstRead = nil
	info, err = s.InboxAckInfo(t.Context(), "a", 3)
	require.NoError(t, err)
	require.Equal(t, AckWatermark, info.Kind, "a later read sees it")
}
