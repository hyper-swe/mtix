// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// collidingPair finds two ids whose digests share the first n hex characters.
func collidingPair(n int) (string, string) {
	seen := map[string]string{}
	for i := 0; ; i++ {
		id := fmt.Sprintf("pair-%d", i)
		key := QuarantineHandleFull(id)[:n]
		if other, ok := seen[key]; ok {
			return other, id
		}
		seen[key] = id
	}
}

func commonPrefixLen(a, b string) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}

func TestQuarantineHandles_CollidingBase_ExtendToTheShortestUniquePrefix(t *testing.T) {
	a, b := collidingPair(6)
	ids := []string{"solo", a, b}
	handles := quarantineHandles(ids, 6)
	lcp := commonPrefixLen(QuarantineHandleFull(a), QuarantineHandleFull(b))
	assert.Len(t, handles[0], 6, "an unshared id keeps the base length")
	assert.Len(t, handles[1], lcp+1, "colliding ids extend by exactly one past the shared prefix")
	assert.Len(t, handles[2], lcp+1)
	assert.NotEqual(t, handles[1], handles[2])
	for i, id := range ids {
		assert.True(t, strings.HasPrefix(QuarantineHandleFull(id), handles[i]))
	}
	assert.Equal(t, []string{QuarantineHandle("x")}, QuarantineHandles([]string{"x"}))
}

func TestMatchQuarantineHandle_AmbiguousPrefix_NamesTheExactSeparatingLength(t *testing.T) {
	a, b := collidingPair(QuarantineHandleMin + 2)
	lcp := commonPrefixLen(QuarantineHandleFull(a), QuarantineHandleFull(b))
	require.GreaterOrEqual(t, lcp, QuarantineHandleMin+2, "the pair shares more than the queried 8 characters")
	_, err := MatchQuarantineHandle([]string{a, b}, QuarantineHandleFull(a)[:QuarantineHandleMin])
	require.ErrorIs(t, err, model.ErrInvalidInput)
	assert.Contains(t, err.Error(), fmt.Sprintf("at least %d hex characters", lcp+1))
}

func TestMatchQuarantineHandle_UpperCaseAndInteriorSubstring(t *testing.T) {
	ids := []string{"evt-a", "evt-b"}
	got, err := MatchQuarantineHandle(ids, strings.ToUpper(QuarantineHandle("evt-b")))
	require.NoError(t, err)
	assert.Equal(t, "evt-b", got)
	// Eight hex characters taken from the middle of a digest are not a prefix.
	interior := QuarantineHandleFull("evt-a")[2 : 2+QuarantineHandleMin]
	if !strings.HasPrefix(QuarantineHandleFull("evt-a"), interior) && !strings.HasPrefix(QuarantineHandleFull("evt-b"), interior) {
		_, err = MatchQuarantineHandle(ids, interior)
		require.ErrorIs(t, err, model.ErrNotFound)
	}
}

func TestListQuarantined_ForcedCollisionAtSmallBase_PrintsDisambiguatedHandles(t *testing.T) {
	a, b := collidingPair(6)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := sqlite.New(filepath.Join(t.TempDir(), "mtix.db"), logger)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	clock := func() time.Time { return time.Date(2026, 3, 14, 12, 0, 0, 0, time.UTC) }
	svc := NewSyncService(st, logger, clock)
	require.NoError(t, st.WithTx(t.Context(), func(tx *sql.Tx) error {
		for _, id := range []string{a, b, "unshared"} {
			q := sqlite.QuarantinedEvent{EventID: id, Source: "pull", RawEvent: "{}", Reason: "r", FirstSeen: "2026-03-14T10:00:00Z", LastAttempt: "2026-03-14T10:00:00Z"}
			if err := sqlite.QuarantineEvent(t.Context(), tx, q); err != nil {
				return err
			}
		}
		return nil
	}))
	rows, err := svc.listQuarantined(t.Context(), 6)
	require.NoError(t, err)
	require.Len(t, rows, 3)
	lcp := commonPrefixLen(QuarantineHandleFull(a), QuarantineHandleFull(b))
	byID := map[string]string{}
	for _, r := range rows {
		byID[r.EventID] = r.Handle
	}
	assert.Len(t, byID[a], lcp+1)
	assert.Len(t, byID[b], lcp+1)
	assert.Len(t, byID["unshared"], 6)
	// The list and the doctor call site must print the same handle.
	ids := []string{a, b, "unshared"}
	for _, id := range ids {
		assert.Equal(t, byID[id], quarantineHandleAmong(ids, id, 6), id)
	}
	// And the default path keeps the 12 character base.
	def, err := svc.ListQuarantined(t.Context())
	require.NoError(t, err)
	for _, r := range def {
		assert.Len(t, r.Handle, QuarantineHandleLen)
	}
}
