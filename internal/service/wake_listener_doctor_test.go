// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests the wake.listener doctor rows (MTIX-112.40.6). Rows for routes whose adapter is
// not built yet must be present and say so; they are never omitted.
package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

type doctorFixture struct {
	agents      []string
	agentsErr   error
	dispatcher  bool
	dispErr     error
	rows        []model.WakeStatusRow
	rowsErr     error
	settingsErr error
	live        bool
}

func (f *doctorFixture) rowsByID(t *testing.T) map[string]service.DoctorResult {
	t.Helper()
	status := service.NewWakeStatusService(service.WakeStatusDeps{
		Settings:   func(string) (bool, time.Duration, error) { return true, 30 * time.Second, f.settingsErr },
		Registered: func(context.Context, string) (bool, error) { return true, nil },
		Dispatcher: func(context.Context) (bool, error) { return f.dispatcher, f.dispErr },
		Rows:       func(context.Context, string, int) ([]model.WakeStatusRow, error) { return f.rows, f.rowsErr },
		Clock:      func() time.Time { return statusNow },
	})
	checks := service.WakeListenerDoctorChecks(service.WakeListenerDoctorDeps{
		Agents:     func(context.Context) ([]string, error) { return f.agents, f.agentsErr },
		Dispatcher: func(context.Context) (bool, error) { return f.dispatcher, f.dispErr },
		Status:     status,
		Clock:      func() time.Time { return statusNow },
	})
	out := map[string]service.DoctorResult{}
	for _, c := range checks {
		res := c.Run(t.Context())
		res.ID = c.ID
		out[c.ID] = res
	}
	return out
}

func TestWakeListenerDoctor_AllDocumentedIDs_AlwaysPresent(t *testing.T) {
	want := []string{
		"wake.listener.routes", "wake.listener.dispatcher", "wake.listener.unconfirmed-rate",
		"wake.listener.hook-installed", "wake.listener.waiter-armed", "wake.listener.route-contract", "wake.listener.bypass-hold",
	}
	for _, f := range []*doctorFixture{{}, {agents: []string{"worker"}, dispatcher: true}, {agentsErr: errors.New("boom")}} {
		rows := f.rowsByID(t)
		for _, id := range want {
			require.Contains(t, rows, id)
			require.NotEmpty(t, rows[id].Detail, id)
			require.NotEmpty(t, rows[id].Fix, id+" must carry a runnable fix")
			require.NotContains(t, rows[id].Fix, "<", id+" fix must run as printed, with no placeholder")
			require.NotContains(t, rows[id].Fix, ">", id)
		}
		require.Len(t, rows, len(want))
	}
}

func TestWakeListenerDoctor_UnbuiltRoutes_ReportedNotReachableWithBlockedBy(t *testing.T) {
	rows := (&doctorFixture{agents: []string{"worker"}, dispatcher: true}).rowsByID(t)
	blocked := map[string]string{
		"wake.listener.hook-installed": "MTIX-112.40.5",
		"wake.listener.waiter-armed":   "MTIX-112.40.4",
		"wake.listener.route-contract": "MTIX-112.40.3",
		"wake.listener.bypass-hold":    "MTIX-112.40.3",
	}
	for id, ticket := range blocked {
		require.Equal(t, service.DoctorInfo, rows[id].Status, id)
		require.Equal(t, false, rows[id].Data["reachable"], id)
		require.Equal(t, ticket, rows[id].Data["blocked_by"], id)
		require.Contains(t, rows[id].Detail, "not reachable in this build", id)
	}
	routes := rows["wake.listener.routes"]
	require.Equal(t, service.DoctorInfo, routes.Status)
	list, ok := routes.Data["routes"].([]map[string]any)
	require.True(t, ok)
	names := map[string]bool{}
	for _, r := range list {
		names[r["route"].(string)] = r["reachable"].(bool)
	}
	require.Equal(t, map[string]bool{"hook-waiter": false, "claude-socket": false, "codex-queue": false, "inbox-only": true}, names)
	require.Contains(t, routes.Fix, "mtix inbox --agent worker --wait")
}

func TestWakeListenerDoctor_Dispatcher_States(t *testing.T) {
	r := (&doctorFixture{agents: []string{"worker"}}).rowsByID(t)["wake.listener.dispatcher"]
	require.Equal(t, service.DoctorWarn, r.Status)
	require.Contains(t, r.Fix, "mtix inbox --agent worker --wait")

	r = (&doctorFixture{agents: []string{"worker"}, dispatcher: true}).rowsByID(t)["wake.listener.dispatcher"]
	require.Equal(t, service.DoctorInfo, r.Status)

	r = (&doctorFixture{}).rowsByID(t)["wake.listener.dispatcher"]
	require.Equal(t, service.DoctorInfo, r.Status, "no registrations: nothing to wake")
	require.Contains(t, r.Detail, "No registered")

	r = (&doctorFixture{agents: []string{"worker"}, dispErr: errors.New("db down")}).rowsByID(t)["wake.listener.dispatcher"]
	require.Equal(t, service.DoctorFail, r.Status)
	require.Contains(t, r.Detail, "db down")

	r = (&doctorFixture{agentsErr: errors.New("reg down")}).rowsByID(t)["wake.listener.dispatcher"]
	require.Equal(t, service.DoctorFail, r.Status)
	require.Contains(t, r.Detail, "reg down")
}

func TestWakeListenerDoctor_UnconfirmedRate_WarnsAboveThreshold(t *testing.T) {
	ago := func(d time.Duration) string { return statusNow.Add(-d).Format(time.RFC3339Nano) }
	var stale, ok []model.WakeStatusRow
	for i := 0; i < 6; i++ {
		stale = append(stale, model.WakeStatusRow{EventSeq: int64(i + 1), Outcome: model.WakeSent, OutcomeAt: ago(time.Hour)})
		ok = append(ok, model.WakeStatusRow{EventSeq: int64(i + 1), Outcome: model.WakeConfirmed, OutcomeAt: ago(time.Hour)})
	}
	old := model.WakeStatusRow{EventSeq: 99, Outcome: model.WakeSent, OutcomeAt: ago(48 * time.Hour)}

	r := (&doctorFixture{agents: []string{"worker"}, dispatcher: true, rows: stale}).rowsByID(t)["wake.listener.unconfirmed-rate"]
	require.Equal(t, service.DoctorWarn, r.Status)
	require.Contains(t, r.Fix, "mtix inbox status --agent worker --json")
	require.Contains(t, r.Fix, "mtix config set wake.confirm_window")
	require.EqualValues(t, 6, r.Data["unconfirmed"])

	r = (&doctorFixture{agents: []string{"worker"}, dispatcher: true, rows: ok}).rowsByID(t)["wake.listener.unconfirmed-rate"]
	require.Equal(t, service.DoctorInfo, r.Status)

	r = (&doctorFixture{agents: []string{"worker"}, dispatcher: true, rows: []model.WakeStatusRow{old}}).rowsByID(t)["wake.listener.unconfirmed-rate"]
	require.Equal(t, service.DoctorInfo, r.Status, "rows older than 24h are not counted")
	require.Contains(t, r.Detail, "No nudges")

	r = (&doctorFixture{agents: []string{"worker"}, rowsErr: errors.New("ledger down")}).rowsByID(t)["wake.listener.unconfirmed-rate"]
	require.Equal(t, service.DoctorFail, r.Status)
	require.Contains(t, r.Detail, "ledger down")
}

func TestWakeListenerDoctor_NoAgent_FixListsRegistrationsAndDetailSaysWhy(t *testing.T) {
	rows := (&doctorFixture{}).rowsByID(t)
	for _, id := range []string{"wake.listener.routes", "wake.listener.hook-installed", "wake.listener.waiter-armed", "wake.listener.route-contract", "wake.listener.bypass-hold"} {
		require.Equal(t, "mtix agent list --registrations", rows[id].Fix, id)
		require.Contains(t, rows[id].Detail, "mtix agent register", id+" detail says how to register")
	}
}

func TestWakeListenerDoctor_UnconfirmedRate_BoundaryAndMinimumSample(t *testing.T) {
	ago := func(d time.Duration) string { return statusNow.Add(-d).Format(time.RFC3339Nano) }
	mix := func(unconfirmed, confirmed int) []model.WakeStatusRow {
		var rows []model.WakeStatusRow
		for i := 0; i < unconfirmed; i++ {
			rows = append(rows, model.WakeStatusRow{EventSeq: int64(len(rows) + 1), Outcome: model.WakeSent, OutcomeAt: ago(time.Hour)})
		}
		for i := 0; i < confirmed; i++ {
			rows = append(rows, model.WakeStatusRow{EventSeq: int64(len(rows) + 1), Outcome: model.WakeConfirmed, OutcomeAt: ago(time.Hour)})
		}
		return rows
	}
	tests := []struct {
		name                   string
		unconfirmed, confirmed int
		want                   service.DoctorStatus
	}{
		{"exactly 20 percent of 5 is not above the threshold", 1, 4, service.DoctorInfo},
		{"40 percent of 5 warns", 2, 3, service.DoctorWarn},
		{"4 nudges all unconfirmed is below the minimum sample", 4, 0, service.DoctorInfo},
		{"5 nudges all unconfirmed meets the minimum", 5, 0, service.DoctorWarn},
		{"21 percent of 100 warns", 21, 79, service.DoctorWarn},
		{"20 percent of 100 does not", 20, 80, service.DoctorInfo},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &doctorFixture{agents: []string{"worker"}, dispatcher: true, rows: mix(tt.unconfirmed, tt.confirmed)}
			require.Equal(t, tt.want, f.rowsByID(t)["wake.listener.unconfirmed-rate"].Status)
		})
	}
}

func TestWakeListenerDoctor_FailedSource_FixDoesNotDependOnTheFailingSource(t *testing.T) {
	// settings unreadable: point at the operator-state report, not at a command that reads the same file.
	f := &doctorFixture{agents: []string{"worker"}, dispatcher: true, settingsErr: errors.New("wake.json corrupt")}
	r := f.rowsByID(t)["wake.listener.unconfirmed-rate"]
	require.Equal(t, service.DoctorFail, r.Status)
	require.Equal(t, "mtix hooks status --json", r.Fix)

	f = &doctorFixture{agents: []string{"worker"}, dispatcher: true, rowsErr: errors.New("ledger down")}
	require.Equal(t, "mtix doctor --json", f.rowsByID(t)["wake.listener.unconfirmed-rate"].Fix)

	f = &doctorFixture{agents: []string{"worker"}, dispErr: errors.New("heartbeat down")}
	require.Equal(t, "mtix doctor --json", f.rowsByID(t)["wake.listener.dispatcher"].Fix)

	f = &doctorFixture{agentsErr: errors.New("reg down")}
	require.Equal(t, "mtix doctor --json", f.rowsByID(t)["wake.listener.dispatcher"].Fix)
	require.Equal(t, "mtix doctor --json", f.rowsByID(t)["wake.listener.unconfirmed-rate"].Fix)
}
