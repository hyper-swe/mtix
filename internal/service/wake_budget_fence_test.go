// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

type unaccountedWakeFence struct{ *sqlite.Store }

func (s unaccountedWakeFence) ClaimWakeBudget(ctx context.Context, key model.WakeKey, _ time.Duration,
	_ func(model.WakeBudgetProposal) (model.WakeBudgetDecision, error),
) (model.WakeBudgetResult, error) {
	source, err := s.WakeBudgetSource(ctx)
	if err != nil {
		return model.WakeBudgetResult{}, err
	}
	return model.WakeBudgetResult{Snapshot: model.WakeBudgetSnapshot{Source: source},
		Attempt: model.WakeAttempt{Key: key, Token: "unaccounted-fence"}, Won: true}, nil
}

func TestWakeBudget_FenceWithoutProtectedIntent_NeverSends(t *testing.T) {
	f := newWakeBudgetFixture(t, "1")
	b := f.board(t, 1)
	err := b.dispatcherWithStore(t, unaccountedWakeFence{b.store}, false, nil).Pass(t.Context(), b.keys)
	require.ErrorIs(t, err, model.ErrConflict)
	require.Zero(t, f.sent.Load(), "a syntactically valid fence still needs durable protected admission")
}
