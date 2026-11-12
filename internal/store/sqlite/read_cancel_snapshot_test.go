// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestReadPaths_CancelledMidQuery_LeaveNoStaleSnapshot runs each inbox read
// with a context cancelled at a varying instant, on a reader pool of ONE
// connection so the next read reuses it, and asserts the next read sees a
// fresh write (a stale WAL snapshot would hide it) (MTIX-112.3).
func TestReadPaths_CancelledMidQuery_LeaveNoStaleSnapshot(t *testing.T) {
	st, err := New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	st.readDB.SetMaxOpenConns(1)
	reads := map[string]func(context.Context){
		"InboxCount":  func(c context.Context) { _, _, _ = st.InboxCount(c, "w") },
		"inboxList":   func(c context.Context) { _, _ = st.inboxList(c, "w") },
		"InboxParked": func(c context.Context) { _, _ = st.InboxParked(c, "w") },
	}
	for name, read := range reads {
		t.Run(name, func(t *testing.T) {
			for i := 0; i < 300; i++ {
				ctx, cancel := context.WithCancel(t.Context())
				go func() { time.Sleep(time.Duration(i%40) * 5 * time.Microsecond); cancel() }()
				read(ctx)
				cancel()
				key := fmt.Sprintf("%s-%d", name, i)
				_, err := st.writeDB.ExecContext(t.Context(), `INSERT INTO meta(key,value) VALUES(?,?)`, key, "v")
				require.NoError(t, err)
				rctx, rc := context.WithTimeout(t.Context(), 2*time.Second)
				var n int
				err = st.readDB.QueryRowContext(rctx, `SELECT COUNT(*) FROM meta WHERE key=?`, key).Scan(&n)
				rc()
				require.NoError(t, err, "iter %d", i)
				require.Equal(t, 1, n, "stale snapshot after cancelled %s, iter %d", name, i)
			}
		})
	}
}
