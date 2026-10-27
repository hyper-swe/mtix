// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"fmt"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func concurrentBroadcastIDs(count int) []string {
	ids := make([]string, count)
	for i := range ids {
		ids[i] = fmt.Sprintf("PROJ-%d", i)
	}
	return ids
}

func drainConcurrentBroadcastIDs(t *testing.T, ch <-chan service.Event) []string {
	t.Helper()
	var ids []string
	for {
		select {
		case event, open := <-ch:
			require.True(t, open, "subscriber must stay open below the buffer limit")
			ids = append(ids, event.NodeID)
		default:
			return ids
		}
	}
}

// Called inside a synctest bubble: all sleeps below advance virtual time.
func assertDebouncerMaxDelayWindow(t *testing.T, d *service.ExportDebouncer, count *atomic.Int64, maxDelay time.Duration, flush int64) {
	t.Helper()
	start := time.Now()
	d.Trigger()
	tick := maxDelay / 10 // 30ms, always inside the configured 100ms quiet period
	for i := 0; i < 9; i++ {
		time.Sleep(tick)
		d.Trigger()
	}
	time.Sleep(maxDelay - 9*tick - time.Nanosecond)
	synctest.Wait()
	assert.Equal(t, flush-1, count.Load(), "continuous writes must suppress the quiet-period export")

	time.Sleep(time.Nanosecond)
	synctest.Wait()
	assert.Equal(t, flush, count.Load(), "export must finish exactly at the max-delay boundary")
	assert.Equal(t, maxDelay, time.Since(start))
}
