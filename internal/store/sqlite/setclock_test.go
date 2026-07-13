// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSetClock injects a deterministic clock so timestamps are reproducible.
func TestSetClock(t *testing.T) {
	s := newTestStore(t)
	fixed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	s.SetClock(func() time.Time { return fixed })

	insertJournalEvent(t, s, "e1", "create_node", `{}`)
	tail, err := s.JournalTail(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(1), tail, "the store operates normally under an injected clock")
}
