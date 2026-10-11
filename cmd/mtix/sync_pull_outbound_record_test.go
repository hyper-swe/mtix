// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func outboundCLIClock() time.Time { return time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC) }

func outboundCLIInit(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("receipt publication is unavailable on this platform")
	}
	initTestApp(t)
	app.store.SetClock(outboundCLIClock)
	app.syncSvc = service.NewSyncService(app.store, slog.Default(), outboundCLIClock)
	t.Setenv(transport.EnvDSN, "")
}

func outboundCLIPull(ctx context.Context, t *testing.T, hub pullSweepHub) (pullOutcome, error) {
	t.Helper()
	owner, err := app.syncSvc.BeginExchangePull(ctx, app.mtixDir)
	if err != nil {
		return pullOutcome{}, err
	}
	defer owner.Close()
	var stderr bytes.Buffer
	in := testIngest(&stderr)
	in.now = outboundCLIClock
	in.receipt = newPullExchangeTracker(app.syncSvc.MaxImportSize)
	result, err := pullThenSweep(ctx, in, hub, app.store, transport.PullCursor{}, 1)
	if err != nil {
		return result, err
	}
	return result, completePullExchange(ctx, owner, in.receipt, result)
}

func outboundCLIReceipt(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(app.mtixDir, "data", "exchange-record.json"))
	require.NoError(t, err)
	return raw
}

func TestPullThenSweep_LocalOutboundAndRemoteApply_PreservesReceivedBytes(t *testing.T) {
	outboundCLIInit(t)
	_, err := outboundCLIPull(t.Context(), t, &fakeLateHub{hubNows: []time.Time{sweepHubT1}})
	require.NoError(t, err)
	before := outboundCLIReceipt(t)
	require.NoError(t, app.store.CreateNode(t.Context(), mkPGNode("TEST-2", "", 0, 2, "local title")))
	var journal string
	require.NoError(t, app.store.QueryRow(t.Context(), `SELECT payload FROM sync_events WHERE node_id=?`, "TEST-2").Scan(&journal))
	events := offlineEvents(t)
	hub := &fakeLateHub{events: events, pullEvents: events, hubNows: []time.Time{sweepHubT1}}
	outcome, err := outboundCLIPull(t.Context(), t, hub)
	require.NoError(t, err)
	require.True(t, outcome.sweep.Completed)
	remote, err := app.store.GetNode(t.Context(), "TEST-9")
	require.NoError(t, err)
	require.Equal(t, "second edit", remote.Description)
	local, err := app.store.GetNode(t.Context(), "TEST-2")
	require.NoError(t, err)
	require.Equal(t, "local title", local.Title)
	var afterJournal, state string
	require.NoError(t, app.store.QueryRow(t.Context(), `SELECT payload,sync_status FROM sync_events WHERE node_id=?`, "TEST-2").Scan(&afterJournal, &state))
	require.Equal(t, journal, afterJournal)
	require.Equal(t, "pending", state)
	assert.Equal(t, before, outboundCLIReceipt(t))
	status, err := app.syncSvc.ExchangeRecordStatus(t.Context(), app.mtixDir)
	require.NoError(t, err)
	assert.Equal(t, "pending-outbound", status.Reason)
}

func outboundCLILegacy(t *testing.T, null bool) []byte {
	t.Helper()
	_, err := outboundCLIPull(t.Context(), t, &fakeLateHub{hubNows: []time.Time{sweepHubT1}})
	require.NoError(t, err)
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(outboundCLIReceipt(t), &fields))
	var boundary map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(fields["boundary"], &boundary))
	delete(boundary, "pending_outbound")
	if null {
		boundary["pending_outbound"] = json.RawMessage("null")
	}
	fields["boundary"], err = json.Marshal(boundary)
	require.NoError(t, err)
	raw, err := json.Marshal(fields)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(app.mtixDir, "data", "exchange-record.json"), raw, 0o600))
	return raw
}

func outboundDoctor(ctx context.Context, t *testing.T) DoctorCheck {
	t.Helper()
	app.jsonOutput = true
	var stdout, stderr bytes.Buffer
	require.ErrorIs(t, runSyncDoctor(ctx, &stdout, &stderr, nil, transport.Options{}), errDoctorChecksFailed)
	var report DoctorReport
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &report))
	for _, check := range report.Checks {
		if check.Name == "sync.exchange-record" {
			return check
		}
	}
	t.Fatal("actual doctor JSON omitted received provenance")
	return DoctorCheck{}
}

func TestRunSyncDoctor_LegacyOutboundBoundary_ReportsUnknownWithoutWriting(t *testing.T) {
	for _, null := range []bool{false, true} {
		t.Run(map[bool]string{false: "absent", true: "null"}[null], func(t *testing.T) {
			outboundCLIInit(t)
			before := outboundCLILegacy(t, null)
			var metaBefore string
			require.NoError(t, app.store.QueryRow(t.Context(), `SELECT json_group_object(key,value) FROM (SELECT key,value FROM meta ORDER BY key)`).Scan(&metaBefore))
			check := outboundDoctor(t.Context(), t)
			require.True(t, check.Pass)
			require.True(t, check.Warn)
			require.Contains(t, check.Detail, "outbound-boundary-unknown")
			require.Contains(t, check.Detail, "inferred")
			require.NotContains(t, check.Detail, "recorded omission (may be stale)")
			require.Contains(t, check.Fix, "mtix sync push")
			require.Contains(t, check.Fix, "mtix sync pull")
			require.Contains(t, check.Fix, "new, complete")
			require.Equal(t, before, outboundCLIReceipt(t))
			var reason string
			require.NoError(t, app.store.QueryRow(t.Context(), `SELECT COALESCE((SELECT value FROM meta WHERE key=?),'')`, "meta.sync.exchange_record.reason").Scan(&reason))
			require.Empty(t, reason)
			var metaAfter string
			require.NoError(t, app.store.QueryRow(t.Context(), `SELECT json_group_object(key,value) FROM (SELECT key,value FROM meta ORDER BY key)`).Scan(&metaAfter))
			require.Equal(t, metaBefore, metaAfter)
		})
	}
}

func outboundCLIReason(ctx context.Context) (string, error) {
	var reason string
	err := app.store.QueryRow(ctx, `SELECT value FROM meta WHERE key=?`, "meta.sync.exchange_record.reason").Scan(&reason)
	return reason, err
}

func outboundCLIRequireZero(t *testing.T) {
	t.Helper()
	var record struct {
		Boundary map[string]json.RawMessage `json:"boundary"`
	}
	require.NoError(t, json.Unmarshal(outboundCLIReceipt(t), &record))
	require.Equal(t, "0", string(record.Boundary["pending_outbound"]))
	reason, err := outboundCLIReason(t.Context())
	require.NoError(t, err)
	require.Empty(t, reason)
}

func TestPullThenSweep_PushAcknowledgmentThenNewPull_PublishesKnownZero(t *testing.T) {
	outboundCLIInit(t)
	_, err := outboundCLIPull(t.Context(), t, &fakeLateHub{hubNows: []time.Time{sweepHubT1}})
	require.NoError(t, err)
	before := outboundCLIReceipt(t)
	require.NoError(t, app.store.CreateNode(t.Context(), mkPGNode("TEST-2", "", 0, 2, "local")))
	_, err = outboundCLIPull(t.Context(), t, &fakeLateHub{hubNows: []time.Time{sweepHubT1}})
	require.NoError(t, err)
	require.Equal(t, before, outboundCLIReceipt(t))
	check := outboundDoctor(t.Context(), t)
	require.True(t, check.Pass)
	require.True(t, check.Warn)
	require.Contains(t, check.Detail, "recorded omission (may be stale): pending-outbound")
	require.Contains(t, check.Fix, "new, complete")
	pending, err := readPendingBatch(t.Context(), app.store, 100)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.NoError(t, markPushed(t.Context(), app.store, []string{pending[0].EventID}))
	require.Equal(t, before, outboundCLIReceipt(t), "acknowledgment alone cannot publish")
	reason, err := outboundCLIReason(t.Context())
	require.NoError(t, err)
	require.Equal(t, "pending-outbound", reason, "acknowledgment alone cannot clear the omission")
	_, err = outboundCLIPull(t.Context(), t, &fakeLateHub{hubNows: []time.Time{sweepHubT1}})
	require.NoError(t, err)
	require.NotEqual(t, before, outboundCLIReceipt(t))
	outboundCLIRequireZero(t)
}

func TestRunSyncDoctor_SavedOmission_UsesRecordedDetail(t *testing.T) {
	outboundCLIInit(t)
	_, err := outboundCLIPull(t.Context(), t, &fakeLateHub{hubNows: []time.Time{sweepHubT1}})
	require.NoError(t, err)
	require.NoError(t, app.store.SaveExchangeRecordReason(t.Context(), "partial-pull"))
	check := outboundDoctor(t.Context(), t)
	require.True(t, check.Pass)
	require.True(t, check.Warn)
	require.Contains(t, check.Detail, "recorded omission (may be stale): partial-pull")
	require.NotContains(t, check.Detail, "inferred")
	require.Empty(t, check.Fix)
}
