// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// The hook-only stalled report on the sender's mtix inbox status, human and --json, and
// in the wake.listener.stalled doctor row (MTIX-112.40.12, FR-20). Rows are seeded straight
// into the ledger; no live session is involved.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

// seedStalledEvent addresses one real inbox event to worker and seeds a sent wake for it
// that is older than the confirm window, with no waiter lease: the hook-only stall.
func seedStalledEvent(t *testing.T) int64 {
	t.Helper()
	id := wakeStatusApp(t)
	seedWakeRegistration(t, "worker")
	seedWakeDispatcher(t)
	app.jsonOutput = true
	captureStdout(t, func() { require.NoError(t, runComment(id, "ping", "worker")) })
	events, err := app.store.InboxList(t.Context(), "worker")
	require.NoError(t, err)
	require.Len(t, events, 1)
	seedWakeRow(t, "worker", events[0].Seq, model.WakeResult{Outcome: model.WakeSent, Route: "hook-waiter"}, 5*time.Minute)
	return events[0].Seq
}

func TestInboxStatus_HookOnlyStall_ShowsStalledInJSON(t *testing.T) {
	seq := seedStalledEvent(t)
	got := inboxStatusJSON(t, "--agent", "worker")
	current := got["current"].(map[string]any)
	require.Equal(t, "stalled", current["state"])
	require.Equal(t, "waiter-not-armed", current["reason"])
	require.EqualValues(t, seq, current["event_seq"])
	require.NotEmpty(t, current["fix"])
	first := got["recent"].([]any)[0].(map[string]any)
	require.Equal(t, "stalled", first["state"])
	require.EqualValues(t, seq, first["event_seq"])
}

func TestInboxStatus_HookOnlyStall_ShowsStalledInText(t *testing.T) {
	seedStalledEvent(t)
	app.jsonOutput = false
	out := outputInboxCommand(t, "status", "--agent", "worker")
	require.Contains(t, out, "current: stalled via hook-waiter (waiter-not-armed)")
	require.Contains(t, out, "fix: ")
	require.Contains(t, out, "stalled")
}

func TestInboxStatus_AcknowledgedStalledEvent_IsNoLongerStalled(t *testing.T) {
	seq := seedStalledEvent(t)
	require.NoError(t, app.store.InboxAck(t.Context(), "worker", seq))
	got := inboxStatusJSON(t, "--agent", "worker")
	require.NotEqual(t, "stalled", got["current"].(map[string]any)["state"])
}

func TestDoctorJSON_StalledRow_WarnsWithFix(t *testing.T) {
	seedStalledEvent(t)
	svc, err := newConfiguredDoctorService()
	require.NoError(t, err)
	var out bytes.Buffer
	require.NoError(t, runDoctor(context.Background(), &out, svc, doctorOutputOptions{JSON: true}))
	var rows []service.DoctorResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &rows))
	found := false
	for _, r := range rows {
		if r.ID != "wake.listener.stalled" {
			continue
		}
		found = true
		require.Equal(t, service.DoctorWarn, r.Status)
		require.True(t, strings.HasPrefix(r.Fix, "mtix "), r.Fix)
		require.Contains(t, r.Detail, "wake.confirm_window")
		require.Equal(t, []any{"worker"}, r.Data["agents"])
	}
	require.True(t, found, "the stalled row is in the doctor output")
}
