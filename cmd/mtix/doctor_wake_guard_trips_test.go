// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/testutil"
)

func guardTripRows(t *testing.T, jsonOutput bool) (string, map[string]service.DoctorResult) {
	t.Helper()
	svc, err := newConfiguredDoctorService()
	require.NoError(t, err)
	var out bytes.Buffer
	require.NoError(t, runDoctor(t.Context(), &out, svc, doctorOutputOptions{JSON: jsonOutput}))
	byID := map[string]service.DoctorResult{}
	if jsonOutput {
		var rows []service.DoctorResult
		require.NoError(t, json.Unmarshal(out.Bytes(), &rows))
		for _, row := range rows {
			byID[row.ID] = row
		}
	}
	return out.String(), byID
}

func TestDoctorGuardTripsJSON_ListsRecentTrips(t *testing.T) {
	initTestApp(t)
	state := testutil.OperatorState(t)
	older := service.NewAlerts(state, func() time.Time { return time.Date(2026, 10, 28, 9, 30, 0, 0, time.UTC) })
	newer := service.NewAlerts(state, func() time.Time { return time.Date(2026, 10, 28, 10, 45, 0, 0, time.UTC) })
	for _, step := range []struct {
		alerts *service.Alerts
		req    service.AlertRequest
	}{
		{older, service.AlertRequest{Agent: "worker", Class: oplocal.AlertClassWakeHopDepth, Detail: "held wake: hop depth 33 exceeds 32 [chain: event 41 hop 33]; mtix config set wake.max_hop_depth 33 --agent worker", Fix: "mtix config set wake.max_hop_depth 33 --agent worker"}},
		{older, service.AlertRequest{Agent: "worker", Class: oplocal.AlertClassBudgetRewind, Detail: "unrelated rewind", Fix: "none"}},
		{newer, service.AlertRequest{Agent: "other", Class: oplocal.AlertClassWakeBackoff, Detail: "held wake: 5 unacked [chain: event 52, 5 unacked wakes]; x", Fix: "mtix config set wake.backoff_after_unacked 6 --agent other"}},
	} {
		_, err := step.alerts.Raise(t.Context(), step.req)
		require.NoError(t, err)
	}

	text, _ := guardTripRows(t, false)
	require.Contains(t, text, "wake.guard-trips")
	require.Contains(t, text, "worker")
	require.Contains(t, text, "2026-10-28T09:30:00Z")
	require.Contains(t, text, "chain event 41 hop 33")
	require.Less(t, strings.Index(text, "other wake.backoff"), strings.Index(text, "worker wake.hop-depth"), "newest first in text")
	require.Contains(t, text, "mtix config set wake.max_hop_depth 33 --agent worker")
	require.NotContains(t, text, "unrelated rewind")

	_, rows := guardTripRows(t, true)
	row, ok := rows["wake.guard-trips"]
	require.True(t, ok)
	require.Equal(t, service.DoctorWarn, row.Status)
	trips, ok := row.Data["trips"].([]any)
	require.True(t, ok)
	require.Len(t, trips, 2)
	first := trips[0].(map[string]any)
	second := trips[1].(map[string]any)
	require.Equal(t, "other", first["agent"], "newest trip first")
	require.Equal(t, "event 52, 5 unacked wakes", first["chain"])
	require.Equal(t, "2026-10-28T10:45:00Z", first["last_at"])
	require.Equal(t, "event 41 hop 33", second["chain"])
	require.Equal(t, "2026-10-28T09:30:00Z", second["last_at"])
	for _, field := range []string{"agent", "class", "chain", "fix", "first_at", "last_at", "count"} {
		require.Contains(t, first, field)
	}
}

func TestDoctorGuardTrips_NoTrips_Info(t *testing.T) {
	initTestApp(t)
	testutil.OperatorState(t)
	_, rows := guardTripRows(t, true)
	row := rows["wake.guard-trips"]
	require.Equal(t, service.DoctorInfo, row.Status)
	require.Contains(t, row.Detail, "No loop-guard trips")
}

func TestDoctorGuardTrips_ReadOnly(t *testing.T) {
	initTestApp(t)
	state := testutil.OperatorState(t)
	_, err := service.NewAlerts(state, time.Now).Raise(t.Context(), service.AlertRequest{Agent: "worker", Class: oplocal.AlertClassWakeHopDepth, Detail: "d", Fix: "f"})
	require.NoError(t, err)
	dir, err := state.Path()
	require.NoError(t, err)
	before := budgetDoctorOperatorSnapshot(t, dir)
	guardTripRows(t, false)
	guardTripRows(t, true)
	require.Equal(t, before, budgetDoctorOperatorSnapshot(t, dir))
}
