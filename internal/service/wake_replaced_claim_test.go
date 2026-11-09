// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// replacedWakeClaim models the store's explicit non-attempt result independently
// of a later identity check: the dispatcher must never cover an unattempted event.
type replacedWakeClaim struct{ *sqlite.Store }

func (replacedWakeClaim) ClaimWakeBudget(context.Context, model.WakeKey, time.Duration,
	func(model.WakeBudgetProposal) (model.WakeBudgetDecision, error),
) (model.WakeBudgetResult, error) {
	return model.WakeBudgetResult{Replaced: true}, nil
}

func TestWakeDispatch_ReplacedClaim_DoesNotAdvanceRegistration(t *testing.T) {
	f := newWakeReplaceFixture(t, 1, false)
	f.d = f.dispatcherOver(replacedWakeClaim{Store: f.store})
	require.NoError(t, f.scanner(always).Scan(t.Context()))
	assert.Equal(t, f.oldSends, f.adapter.seqs())
	mark, held := f.mark()
	assert.Equal(t, f.survivorTail, mark, "a replaced claim is a non-attempt even if a later read would find the same event")
	assert.False(t, held)
}
