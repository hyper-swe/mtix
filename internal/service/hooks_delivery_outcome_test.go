// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests that an internal delivery skip never becomes persisted hook state.
package service

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHooksDelivery_ReplacedAggregateNeverReachesLedger(t *testing.T) {
	ctx := context.Background()
	d := newEncodingDispatcher(t, new(bytes.Buffer))
	nodes := NewNodeService(d.store, nil, nil, d.logger, time.Now)
	_, err := nodes.CreateNode(ctx, &CreateNodeRequest{Project: "TEST", Title: "event", Creator: "worker"})
	require.NoError(t, err)
	event, ok, err := d.store.ReadJournalEventAt(ctx, 1)
	require.NoError(t, err)
	require.True(t, ok)
	won, matched, err := d.store.ClaimHookDispatchEvent(ctx, "aggregate", event, time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	require.True(t, matched)
	d.recordOutcome(ctx, "aggregate", event, "skipped-replaced")
	var outcome string
	require.NoError(t, d.store.ReadDB().QueryRow(`SELECT outcome FROM hook_dispatch_ledger WHERE hook_name='aggregate'`).Scan(&outcome))
	assert.Equal(t, sqlite.OutcomeClaimed, outcome)
	assert.Zero(t, d.HookIdentityFenceMisses(), "a private skip makes no outcome write attempt")
}
