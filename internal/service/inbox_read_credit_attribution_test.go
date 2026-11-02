// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

// addInboxEvent appends one event from sender to the board's recipient and
// returns its wake key.
func addInboxEvent(t *testing.T, b *wakeBudgetBoard, agent, sender string) model.WakeKey {
	t.Helper()
	f := b.fixture
	clock := func() time.Time { return f.now }
	nodes := service.NewNodeService(b.store, &service.NoopBroadcaster{}, &service.StaticConfig{}, slog.Default(), clock)
	prompts := service.NewPromptService(b.store, &service.NoopBroadcaster{}, slog.Default(), clock)
	node, err := nodes.CreateNode(t.Context(), &service.CreateNodeRequest{Project: "TEST", Title: "extra event", Creator: sender})
	require.NoError(t, err)
	require.NoError(t, prompts.AddAnnotation(t.Context(), node.ID, "extra", sender, agent))
	entries, err := b.store.InboxList(t.Context(), agent)
	require.NoError(t, err)
	last := entries[len(entries)-1]
	return model.WakeKey{AgentID: agent, HostID: f.host, UID: f.uid, EventSeq: last.Seq, Epoch: 1}
}

// F15 (MTIX-112.18.1 r2): a read credit belongs to the sender of the event it
// charges, so reading alice's events does not spend bob's per-sender allowance.
func TestInboxRead_CreditsCarryEventSender_OtherSenderStillWakes(t *testing.T) {
	f := newWakeBudgetFixture(t, "10")
	senderLimit(t, f, "1")
	b := f.boardFrom(t, 1, "budget-worker", "alice")
	svc := service.NewInboxService(b.store).WithInboxPolicy(budgetedPolicy(f))
	res, err := svc.InboxRead(t.Context(), "budget-worker", service.InboxReadOptions{})
	require.NoError(t, err)
	require.Len(t, res.Events, 1)
	require.Equal(t, int64(1), credits(t, f, "budget-worker"))

	bobKey := addInboxEvent(t, b, "budget-worker", "bob")
	require.NoError(t, b.dispatcher(t, false, nil).Pass(t.Context(), []model.WakeKey{bobKey}))
	assert.Equal(t, int64(1), f.sent.Load(), "bob never woke the agent, so alice's read credit is not his")
}

// The same credit still counts against alice: her second wake in the hour is held.
func TestInboxRead_CreditsCarryEventSender_SameSenderHeld(t *testing.T) {
	f := newWakeBudgetFixture(t, "10")
	senderLimit(t, f, "1")
	b := f.boardFrom(t, 1, "budget-worker", "alice")
	svc := service.NewInboxService(b.store).WithInboxPolicy(budgetedPolicy(f))
	_, err := svc.InboxRead(t.Context(), "budget-worker", service.InboxReadOptions{})
	require.NoError(t, err)

	aliceKey := addInboxEvent(t, b, "budget-worker", "alice")
	require.NoError(t, b.dispatcher(t, false, nil).Pass(t.Context(), []model.WakeKey{aliceKey}))
	assert.Zero(t, f.sent.Load(), "alice already used her per-sender allowance by being read")
}
