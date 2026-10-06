// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// Push validation uses the reference clock before checking pool availability.
func TestPushEventsResult_FixedClock_RejectsFutureBeforePoolError(t *testing.T) {
	p := &transport.Pool{}
	fixed := time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)
	p.SetClock(func() time.Time { return fixed })
	events := presenceBatch()[:1]
	events[0].WallClockTS = fixed.Add(25 * time.Hour).UnixMilli()
	_, err := p.PushEventsResult(context.Background(), events)
	require.ErrorContains(t, err, "validate")
	require.NotContains(t, err.Error(), "pool not open")
	events[0].WallClockTS = fixed.UnixMilli()
	_, err = p.PushEventsResult(context.Background(), events)
	require.ErrorContains(t, err, "pool not open")
}

func TestPushEventsResult_DefaultNilZeroAndEmpty_Safe(t *testing.T) {
	for _, p := range []*transport.Pool{nil, {}} {
		p.SetClock(nil)
		result, err := p.PushEventsResult(context.Background(), nil)
		require.NoError(t, err)
		require.Empty(t, result.Accepted())
		events := presenceBatch()[:1]
		events[0].WallClockTS = time.Now().Add(25 * time.Hour).UnixMilli()
		_, err = p.PushEventsResult(context.Background(), events)
		require.ErrorContains(t, err, "validate")
		events[0].WallClockTS = time.Now().UnixMilli()
		_, err = p.PushEventsResult(context.Background(), events)
		require.ErrorContains(t, err, "pool not open")
	}
}

func TestPushEventsResult_SetClockNil_RestoresDefault(t *testing.T) {
	p := &transport.Pool{}
	p.SetClock(func() time.Time { return time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC) })
	p.SetClock(nil)
	events := presenceBatch()[:1]
	events[0].WallClockTS = time.Now().UnixMilli()
	_, err := p.PushEventsResult(context.Background(), events)
	require.ErrorContains(t, err, "pool not open")
}
