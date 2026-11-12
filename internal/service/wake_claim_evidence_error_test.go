// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests that a failed delivery-evidence read is surfaced as a warning while the reclaimed
// event is still delivered again (MTIX-112.40.13.3).
package service_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

type noopClaimLedger struct{}

func (noopClaimLedger) ClaimWake(context.Context, model.WakeKey, time.Duration) (model.WakeAttempt, bool, error) {
	return model.WakeAttempt{}, false, nil
}
func (noopClaimLedger) RecordWakeResult(context.Context, model.WakeAttempt, model.WakeResult) error {
	return nil
}
func (noopClaimLedger) UpgradeWakeConfirmed(context.Context, model.WakeKey) error { return nil }

func TestWakeClaimerResolve_EvidenceReadError_WarnsWithKeyAndStillDelivers(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	evidence := func(context.Context, model.WakeKey) (bool, error) { return false, errors.New("evidence store down") }
	c, err := service.NewWakeClaimer(noopClaimLedger{}, time.Minute, evidence)
	require.NoError(t, err)
	c = c.WithLogger(logger)
	key := model.WakeKey{AgentID: "agent-x", HostID: "host-y", UID: "uid-z", EventSeq: 42, Epoch: 3}

	attempt, deliver, err := c.Resolve(t.Context(), model.WakeAttempt{Key: key, Reclaimed: true})

	require.NoError(t, err)
	require.True(t, deliver, "unknown evidence still delivers: never lost")
	require.Equal(t, key, attempt.Key)
	out := buf.String()
	require.Contains(t, out, "level=WARN")
	require.Contains(t, out, "evidence store down")
	require.Contains(t, out, "agent-x")
	require.Contains(t, out, "42")
}

func TestWakeClaimerResolve_EvidenceNone_DoesNotWarn(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	evidence := func(context.Context, model.WakeKey) (bool, error) { return false, nil }
	c, err := service.NewWakeClaimer(noopClaimLedger{}, time.Minute, evidence)
	require.NoError(t, err)
	_, deliver, err := c.WithLogger(logger).Resolve(t.Context(), model.WakeAttempt{Reclaimed: true})
	require.NoError(t, err)
	require.True(t, deliver)
	require.Empty(t, buf.String())
}
