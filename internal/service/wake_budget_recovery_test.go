// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

type interruptedBudgetStore struct {
	*sqlite.Store
	afterCommit bool
	cause       error
}

func (s interruptedBudgetStore) ClaimWakeBudget(ctx context.Context, key model.WakeKey, lease time.Duration,
	admit func(model.WakeBudgetProposal) (model.WakeBudgetDecision, error),
) (model.WakeBudgetResult, error) {
	if !s.afterCommit {
		return s.Store.ClaimWakeBudget(ctx, key, lease, func(p model.WakeBudgetProposal) (model.WakeBudgetDecision, error) {
			d, err := admit(p)
			if err == nil && d.Allow {
				err = s.cause
			}
			return d, err
		})
	}
	r, err := s.Store.ClaimWakeBudget(ctx, key, lease, admit)
	if err == nil && r.Won {
		return model.WakeBudgetResult{}, s.cause
	}
	return r, err
}

func TestWakeBudget_InterruptedAcceptance_ChargedWithoutSend(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "rollback after pending checkpoint", true: "commit result lost"}[committed], func(t *testing.T) {
			f := newWakeBudgetFixture(t, "1")
			b := f.board(t, 2)
			cause := errors.New("acceptance interrupted")
			backend := interruptedBudgetStore{Store: b.store, afterCommit: committed, cause: cause}
			err := b.dispatcherWithStore(t, backend, false, nil).Pass(t.Context(), b.keys[:1])
			require.ErrorIs(t, err, cause)
			require.Zero(t, f.sent.Load())
			mirror, present, err := f.state.ReadWakeBudget(b.keys[0].Recipient())
			require.NoError(t, err)
			require.True(t, present)
			source, err := b.store.WakeBudgetSource(t.Context())
			require.NoError(t, err)
			require.NotEmpty(t, mirror.Sources[source].Pending)
			require.NoError(t, b.store.Close())
			b.open(t)
			require.NoError(t, b.dispatcher(t, false, nil).Pass(t.Context(), b.keys[1:]))
			require.Zero(t, f.sent.Load(), "restart cannot refund an uncertain accepted attempt")
			wantRows := int64(0)
			if committed {
				wantRows = 1
			}
			require.Equal(t, wantRows, budgetLedgerRows(t, b))
		})
	}
}

func TestWakeBudget_RollingHour_ExactBoundaryAndClockRollback(t *testing.T) {
	f := newWakeBudgetFixture(t, "1")
	b := f.board(t, 3)
	require.NoError(t, b.dispatcher(t, true, nil).Pass(t.Context(), b.keys[:1]))
	f.now = f.now.Add(time.Hour - time.Nanosecond)
	require.NoError(t, b.dispatcher(t, true, nil).Pass(t.Context(), b.keys[1:2]))
	require.Equal(t, int64(1), f.sent.Load())
	f.now = f.now.Add(time.Nanosecond)
	require.NoError(t, b.dispatcher(t, true, nil).Pass(t.Context(), b.keys[1:2]))
	require.Equal(t, int64(2), f.sent.Load())
	f.now = f.now.Add(-2 * time.Hour)
	require.NoError(t, b.dispatcher(t, true, nil).Pass(t.Context(), b.keys[2:]))
	require.Equal(t, int64(2), f.sent.Load(), "moving the clock backwards never returns a rolling-hour credit")
}
