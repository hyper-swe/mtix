// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Only events above the registration's high-water mark wake a pi session: the backlog from
// before registration and acknowledged events stay in the inbox.
func TestAttachWaitPi_HighWaterMark_OnlyEventsAboveWake(t *testing.T) {
	p := newPiFixture(t)
	below, at, above := p.addEvent("sender"), p.addEvent("sender"), p.addEvent("sender")
	_, err := p.store.WriteDB().ExecContext(t.Context(),
		"UPDATE agent_registrations SET high_water=? WHERE agent_id=?", at, p.key.AgentID)
	require.NoError(t, err)
	require.Less(t, below, at)

	out, err := p.run(t.Context(), piSession)
	require.NoError(t, err)
	assert.Contains(t, out, `"seq":`+itoa(above))
	assert.Equal(t, 1, p.ledgerRows(), "the events at and below the mark are never claimed")
	assert.Equal(t, 1, p.pws.claimCalls())
}

// A registration of another harness that carries the same session handle is not a pi row.
func TestAttachWaitPi_OtherHarnessSameHandle_NotClaimed(t *testing.T) {
	p := newPiFixture(t)
	p.addEvent("sender")
	_, err := p.store.WriteDB().ExecContext(t.Context(),
		"UPDATE agent_registrations SET harness='claude-code' WHERE agent_id=?", p.key.AgentID)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	out, err := p.run(ctx, piSession)
	require.Error(t, err)
	assert.Empty(t, out)
	assert.Zero(t, p.pws.claimCalls())
	assert.Zero(t, p.ledgerRows())
}

// The ledger records the pi route with the sent outcome.
func TestAttachWaitPi_Claim_RecordsPiRoute(t *testing.T) {
	p := newPiFixture(t)
	seq := p.addEvent("sender")
	_, err := p.run(t.Context(), piSession)
	require.NoError(t, err)
	var route string
	require.NoError(t, p.store.ReadDB().QueryRowContext(t.Context(),
		"SELECT route FROM wake_ledger WHERE event_seq=?", seq).Scan(&route))
	assert.Equal(t, "pi", route)
}
