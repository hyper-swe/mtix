// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The cursor marks of one agent are bounded; the lowest cursors are evicted
// first, and the eviction of one agent never touches another's marks.
func TestCursorMarks_BeyondBound_EvictsLowestCursorsFirst(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "marks.db"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	now := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return now })
	advance := func(agent string, cursor int) {
		require.NoError(t, s.WithTx(t.Context(), func(tx *sql.Tx) error {
			_, err := tx.ExecContext(t.Context(), `INSERT INTO agent_inbox_cursor(agent_id,cursor) VALUES(?,?)
				ON CONFLICT(agent_id) DO UPDATE SET cursor=excluded.cursor`, agent, cursor)
			require.NoError(t, err)
			return s.writeCursorMark(t.Context(), tx, agent)
		}))
	}
	advance("other", 5)
	for c := 1; c <= MaxCursorMarks; c++ {
		advance("a", c)
	}
	count := func(agent string) (n, lowest int) {
		require.NoError(t, s.WriteDB().QueryRow(`SELECT COUNT(*), MIN(cursor) FROM agent_inbox_cursor_marks WHERE agent_id=?`, agent).Scan(&n, &lowest))
		return n, lowest
	}
	n, lowest := count("a")
	require.Equal(t, MaxCursorMarks, n)
	require.Equal(t, 1, lowest, "at the bound nothing is evicted")
	advance("a", MaxCursorMarks+1)
	n, lowest = count("a")
	require.Equal(t, MaxCursorMarks, n)
	require.Equal(t, 2, lowest, "the one lowest cursor goes first")
	advance("a", MaxCursorMarks+3)
	n, lowest = count("a")
	require.Equal(t, MaxCursorMarks, n)
	require.Equal(t, 3, lowest)
	n, _ = count("other")
	require.Equal(t, 1, n)
}
