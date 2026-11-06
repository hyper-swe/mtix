// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
)

// F16 (MTIX-112.18.1 r2): ClaimWake (pi wait, Claude hook) writes a ledger row
// without charging the budget, so the read that returns the event charges it.
func TestInboxRead_ClaimWakeRowsAreUncharged_ReadChargesAndHolds(t *testing.T) {
	f := newWakeBudgetFixture(t, "1")
	b := f.board(t, 2)
	for _, key := range b.keys {
		_, won, err := b.store.ClaimWake(t.Context(), key, time.Minute)
		require.NoError(t, err)
		require.True(t, won)
	}
	svc := service.NewInboxService(b.store).WithInboxPolicy(budgetedPolicy(f))
	res, err := svc.InboxRead(t.Context(), "budget-worker", service.InboxReadOptions{})
	require.NoError(t, err)
	assert.Len(t, res.Events, 1, "budget of 1 admits one claimed event")
	assert.Equal(t, 1, res.Held.Count, "the second claimed event is held")
	assert.Equal(t, int64(1), credits(t, f, "budget-worker"), "exactly one credit recorded")
}

// A dispatcher-charged event read later stays charged once, next to a claimed one.
func TestInboxRead_DispatcherChargedAndClaimedMix_ChargedOnce(t *testing.T) {
	f := newWakeBudgetFixture(t, "2")
	b := f.board(t, 2)
	require.NoError(t, b.dispatcher(t, false, nil).Pass(t.Context(), b.keys[:1]))
	require.Equal(t, int64(1), credits(t, f, "budget-worker"))
	_, won, err := b.store.ClaimWake(t.Context(), b.keys[1], time.Minute)
	require.NoError(t, err)
	require.True(t, won)

	svc := service.NewInboxService(b.store).WithInboxPolicy(budgetedPolicy(f))
	res, err := svc.InboxRead(t.Context(), "budget-worker", service.InboxReadOptions{})
	require.NoError(t, err)
	assert.Len(t, res.Events, 2)
	assert.Zero(t, res.Held.Count)
	assert.Equal(t, int64(2), credits(t, f, "budget-worker"), "one dispatcher credit plus one read credit")
}
