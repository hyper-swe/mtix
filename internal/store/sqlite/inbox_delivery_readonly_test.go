// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
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

// writeAfterFirstRead runs a write on the writer connection right after the
// first query of the snapshot it wraps, standing in for a concurrent ack.
type writeAfterFirstRead struct {
	rowQuerier
	after func()
	fired bool
}

func (w *writeAfterFirstRead) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	row := w.rowQuerier.QueryRowContext(ctx, q, args...)
	if !w.fired {
		w.fired = true
		w.after()
	}
	return row
}

// MTIX-112.3: InboxAckInfo's reads share one snapshot, so a write between its
// queries cannot mix two states. Reading the cursor outside the snapshot (for
// example on the pool) would see the concurrent ack.
func TestInboxAckInfo_ConcurrentAckBetweenReads_SeesOneSnapshot(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "snap.db"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	var w *writeAfterFirstRead
	wrap := func(q rowQuerier) rowQuerier {
		w = &writeAfterFirstRead{rowQuerier: q, after: func() {
			// A cumulative ack lands after the ack-row read and before the cursor read.
			_, werr := s.WriteDB().ExecContext(t.Context(), `INSERT INTO agent_inbox_cursor(agent_id,cursor) VALUES('a',5)`)
			require.NoError(t, werr)
		}}
		return w
	}
	info, err := ackInfoSnapshot(t.Context(), s.readDB, "a", 3, wrap)
	require.NoError(t, err)
	require.NotNil(t, w)
	require.True(t, w.fired, "the interleaved write ran between the reads")
	require.Equal(t, AckNone, info.Kind, "the snapshot predates the concurrent ack")
	info, err = s.InboxAckInfo(t.Context(), "a", 3)
	require.NoError(t, err)
	require.Equal(t, AckWatermark, info.Kind, "a later read sees it")
}
