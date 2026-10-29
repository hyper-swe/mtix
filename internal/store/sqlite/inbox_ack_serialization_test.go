// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type inboxAckEffect struct {
	before, after int64
	err           error
}

func TestInboxAckCoverage_QueuedWritersReturnOnlyTheirOwnEffect(t *testing.T) {
	s, _ := budgetDataStore(t)
	_, _, seq := addressThree(t, s, "worker")
	require.Equal(t, 1, s.WriteDB().Stats().MaxOpenConnections)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	held, err := s.WriteDB().Conn(ctx)
	require.NoError(t, err)
	release := sync.OnceFunc(func() { assert.NoError(t, held.Close()) })
	results := make(chan inboxAckEffect, 2)
	var workers sync.WaitGroup
	defer func() { cancel(); release(); workers.Wait() }()
	priorWaits := s.WriteDB().Stats().WaitCount
	for range 2 {
		workers.Go(func() {
			before, after, err := s.InboxAckCoverage(ctx, "worker", seq, false)
			results <- inboxAckEffect{before, after, err}
		})
	}
	// Both calls must be blocked on the writer pool before either can write.
	// An unsafe outside-transaction before-read has already happened by then.
	require.Eventually(t, func() bool { return s.WriteDB().Stats().WaitCount >= priorWaits+2 }, 5*time.Second, time.Millisecond)
	release()
	workers.Wait()
	effects := 0
	for range 2 {
		r := <-results
		require.NoError(t, r.err)
		require.GreaterOrEqual(t, r.after, r.before)
		if r.after > r.before {
			effects++
		}
	}
	require.Equal(t, 1, effects, "only the transaction that inserted the ACK owns a new effect")
}
