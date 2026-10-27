// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Boundary checks exercise an actual sequence of wake-triggered addressed sends.
package service_test

import (
	"context"
	"encoding/json"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/testutil"
	"github.com/stretchr/testify/require"
	"strconv"
	"testing"
)

func TestHopDepth_33HopChain_Held(t *testing.T)  { runHopChain(t, 33, true) }
func TestHopDepth_32HopChain_Woken(t *testing.T) { runHopChain(t, 32, false) }

func runHopChain(t *testing.T, last int, held bool) {
	t.Helper()
	t.Setenv("MTIX_WAKE_HOP", "")
	prompts, nodes, store, _ := newTestPromptService(t)
	node, err := nodes.CreateNode(t.Context(), &service.CreateNodeRequest{Project: "TEST", Title: "hop chain", Creator: "sender"})
	require.NoError(t, err)
	alerts := service.NewAlerts(operatorAlertsFixture(t), fixedClock(node.CreatedAt))
	state := hopDepthOperatorState(t)
	sent := 0
	dispatcher, err := service.NewWakeDispatcher(service.WakeDispatcherOptions{Store: store, Alerts: alerts, BudgetState: state, Settings: state, Send: func(context.Context, model.WakeAttempt) error { sent++; return nil }})
	require.NoError(t, err)
	for depth := 0; depth <= last; depth++ {
		require.NoError(t, prompts.AddAnnotation(t.Context(), node.ID, "chain event", "sender", "worker"))
		entries, err := store.InboxList(t.Context(), "worker")
		require.NoError(t, err)
		entry := entries[len(entries)-1]
		events, err := store.ReadPendingEvents(t.Context(), 100)
		require.NoError(t, err)
		var payload struct {
			Hop int64 `json:"hop"`
		}
		require.NoError(t, json.Unmarshal(events[len(events)-1].Payload, &payload))
		require.Equal(t, int64(depth), payload.Hop)
		key := model.WakeKey{AgentID: "worker", HostID: "host", UID: "uid", EventSeq: entry.Seq, Epoch: 1}
		require.NoError(t, dispatcher.Pass(t.Context(), []model.WakeKey{key}))
		if depth <= 32 {
			require.Equal(t, depth+1, sent)
		} else {
			require.Equal(t, 33, sent)
		}
		t.Setenv("MTIX_WAKE_HOP", strconv.FormatInt(payload.Hop, 10))
	}
	rows, err := alerts.Recent(t.Context(), 0)
	require.NoError(t, err)
	if held {
		require.Len(t, rows, 1)
		require.Equal(t, "wake.hop-depth", rows[0].Class)
		require.Contains(t, rows[0].Fix, "mtix config set wake.max_hop_depth 33")
	} else {
		require.Empty(t, rows)
	}
	var count int
	require.NoError(t, store.ReadDB().QueryRowContext(t.Context(), "SELECT count(*) FROM wake_ledger").Scan(&count))
	require.Equal(t, 33, count)
	entries, err := store.InboxList(t.Context(), "worker")
	require.NoError(t, err)
	require.Len(t, entries, last+1, "holding or waking never acknowledges events")
}

func hopDepthOperatorState(t *testing.T) *oplocal.State {
	t.Helper()
	state := testutil.OperatorState(t)
	// This chain isolates depth; independent backoff tests exercise the default 5.
	threshold := "1000"
	require.NoError(t, state.UpdateWakeSetting(t.Context(), oplocal.WakeSettingUpdate{Key: "wake.backoff_after_unacked", Value: &threshold}, nil))
	return state
}
