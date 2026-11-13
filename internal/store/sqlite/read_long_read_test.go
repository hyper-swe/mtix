// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// longReadRows sizes a recursive count that runs for tens of milliseconds.
const longReadRows = 400000

// minLongRead is the least time the long read must take for the test to mean
// anything.
const minLongRead = 15 * time.Millisecond

// TestReadPool_LongReadWithoutCancel_IsNotCut proves the shield sets no time
// bound: a first step that runs for a long time on an uncancelled context
// completes with its value, and the detached context carries no deadline
// (MTIX-250; the earlier 30 s bound failed a long integrity check).
func TestReadPool_LongReadWithoutCancel_IsNotCut(t *testing.T) {
	st, err := New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	start := time.Now()
	var n int
	require.NoError(t, st.readDB.QueryRowContext(t.Context(),
		`WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c WHERE x < ?) SELECT COUNT(*) FROM c`, longReadRows).Scan(&n))
	require.Equal(t, longReadRows, n)
	require.Greater(t, time.Since(start), minLongRead,
		"the read ran longer than the cancel delay the route tests use")

	dctx, release, err := detachedRead(t.Context())
	require.NoError(t, err)
	defer release()
	_, hasDeadline := dctx.Deadline()
	require.False(t, hasDeadline, "the shield adds no time bound of its own")
	require.NoError(t, dctx.Err())
}
