// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests the supported protocol boundary through actual delivery and operator alerts.
package service_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestDispatcher_SupportedProtocolFloor_DeliversWithoutAlert(t *testing.T) {
	s, _, alerts := wakeServiceStore(t)
	require.NoError(t, s.SetMinDispatchProtocol(t.Context(), model.AttachProtocol))
	key := wakeServiceKey()
	sent := 0
	d := wakeDispatcher(t, s, alerts, func(_ context.Context, attempt model.WakeAttempt) error {
		require.Equal(t, key, attempt.Key)
		require.NotEmpty(t, attempt.Token)
		sent++
		return nil
	})
	require.NoError(t, d.Pass(t.Context(), []model.WakeKey{key}))
	require.NoError(t, d.Pass(t.Context(), nil))
	require.Equal(t, 1, sent)
	rows, err := alerts.Recent(t.Context(), 0)
	require.NoError(t, err)
	require.Empty(t, rows)
	stats, err := s.WakeLedgerStats(t.Context())
	require.NoError(t, err)
	require.Equal(t, model.WakeLedgerStats{Rows: 1}, stats)
	floor, err := s.MinDispatchProtocol(t.Context())
	require.NoError(t, err)
	require.Equal(t, model.AttachProtocol, floor)
}
