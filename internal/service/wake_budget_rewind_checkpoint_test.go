// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

type cancelAfterBudgetCommit struct {
	*sqlite.Store
	cancel context.CancelFunc
}

func (s cancelAfterBudgetCommit) ClaimWakeBudget(ctx context.Context, key model.WakeKey, lease time.Duration,
	admit func(model.WakeBudgetProposal) (model.WakeBudgetDecision, error),
) (model.WakeBudgetResult, error) {
	r, err := s.Store.ClaimWakeBudget(ctx, key, lease, admit)
	if err == nil {
		s.cancel()
	}
	return r, err
}

func rewoundWakeBudgetBoard(t *testing.T) *wakeBudgetBoard {
	t.Helper()
	f := newWakeBudgetFixture(t, "2")
	b := f.board(t, 3)
	require.NoError(t, b.dispatcher(t, true, nil).Pass(t.Context(), b.keys[:1]))
	require.NoError(t, b.store.Close())
	earlier, err := os.ReadFile(b.path)
	require.NoError(t, err)
	b.open(t)
	require.NoError(t, b.dispatcher(t, true, nil).Pass(t.Context(), b.keys[1:2]))
	require.NoError(t, b.store.Close())
	require.NoError(t, os.WriteFile(b.path, earlier, 0o600))
	b.open(t)
	return b
}

func TestBudgetCounter_RewindDiagnosticSurvivesPostCommitCancellation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		index int
	}{{"held", 2}, {"nonclaimable", 0}} {
		t.Run(tc.name, func(t *testing.T) {
			b := rewoundWakeBudgetBoard(t)
			f := b.fixture
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			backend := cancelAfterBudgetCommit{Store: b.store, cancel: cancel}
			err := b.dispatcherWithStore(t, backend, true, nil).Pass(ctx, b.keys[tc.index:tc.index+1])
			require.ErrorIs(t, err, context.Canceled)
			counters, err := b.store.WakeBudgetCounters(t.Context())
			require.NoError(t, err)
			require.Equal(t, int64(2), counters[0].Total, "the interrupted pass already healed the store floor")
			mirror, _, err := f.state.ReadWakeBudget(b.keys[0].Recipient())
			require.NoError(t, err)
			require.Positive(t, mirror.Rewinds, "the healed counter must not erase the only durable rewind evidence")
			require.Equal(t, f.now, mirror.LastRewindAt)
			row := service.WakeBudgetDoctorCheck(b.store, f.state).Run(t.Context())
			require.Equal(t, service.DoctorWarn, row.Status)
			require.Equal(t, 1, row.Data["recorded_rewind_recipients"])
			require.Equal(t, int64(2), f.sent.Load())
		})
	}
}
