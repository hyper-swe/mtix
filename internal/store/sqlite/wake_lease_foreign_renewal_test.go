// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

// Foreign renewal keeps ownership and the original journal boundary unchanged.

import (
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
	"time"
)

func TestAcquireWakeLease_RenewWithForeignHolder_ReturnsAlreadyClaimed(t *testing.T) {
	s, path := wakeJournalStore(t)
	first := requireWakeLease(t, s)
	require.Greater(t, first.HighWater, int64(0))
	foreign := strings.Repeat("0", 32)
	if foreign == first.Holder {
		foreign = strings.Repeat("1", 32)
	}
	later := openWakeStore(t, path, wakeTime().Add(30*time.Second))
	_, err := later.AcquireWakeLease(t.Context(), wakeKey(), foreign, 15*time.Minute)
	require.ErrorIs(t, err, model.ErrAlreadyClaimed)
	var holder, acquired, expires string
	var mark int64
	var dirty int
	require.NoError(t, newTestDB(t, path).QueryRow("SELECT holder,high_water,acquired_at,expires_at,dirty FROM wake_leases").Scan(&holder, &mark, &acquired, &expires, &dirty))
	assert.Equal(t, first.Holder, holder)
	assert.Equal(t, first.HighWater, mark)
	assert.Equal(t, first.AcquiredAt.Format(time.RFC3339Nano), acquired)
	assert.Equal(t, first.ExpiresAt.Format(time.RFC3339Nano), expires)
	assert.Equal(t, 1, dirty)
}
