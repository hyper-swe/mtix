// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Reachability tests distinguish unavailable diagnostics from stuck queues (MTIX-217).
package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

func TestAppendLocalChecks_QueueReachability(t *testing.T) {
	for _, tc := range []struct {
		name    string
		checks  []DoctorCheck
		pending bool
		pass    bool
		skipped bool
	}{
		{"unreachable old pending", []DoctorCheck{{Name: "PG reachable"}}, true, false, true},
		{"unreachable empty", []DoctorCheck{{Name: "PG reachable"}}, false, false, true},
		{"reachable old pending", []DoctorCheck{{Name: "PG reachable", Pass: true}}, true, false, false},
		{"reachable empty", []DoctorCheck{{Name: "PG reachable", Pass: true}}, false, true, false},
		{"schema failure old pending", []DoctorCheck{{Name: "PG reachable", Pass: true}, {Name: schemaCurrentName}}, true, false, false},
		{"relay only old pending", nil, true, false, false},
		{"relay only empty", nil, false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			initTestApp(t)
			if tc.pending {
				seedOldDoctorQueue(t)
			}
			report := appendLocalChecks(t.Context(), DoctorReport{OverallPass: true, Checks: tc.checks})
			raw, err := json.Marshal(report)
			require.NoError(t, err)
			row := doctorReachabilityRow(t, raw, "queue draining")
			assert.Equal(t, tc.pass, row["pass"])
			if tc.skipped {
				assert.Equal(t, true, row["skipped"])
				assert.Contains(t, row["detail"], "hub unreachable")
				assert.NotContains(t, row["detail"], "pusher")
			} else {
				assert.NotContains(t, row, "skipped")
				if tc.pending {
					assert.Contains(t, row["detail"], "events pending for >1h")
				}
			}
		})
	}
}

func seedOldDoctorQueue(t *testing.T) {
	t.Helper()
	require.NoError(t, runCreate("queued task", "", "", 3, "", "", "", "", ""))
	// Pin well before the one-hour boundary without sleeps or clock-sensitive timing.
	_, err := app.store.WriteDB().ExecContext(t.Context(), `UPDATE sync_events SET created_at = ? WHERE sync_status = ?`, "2000-01-01T00:00:00Z", "pending")
	require.NoError(t, err)
	var n int
	require.NoError(t, app.store.QueryRow(t.Context(), `SELECT COUNT(*) FROM sync_events WHERE sync_status = ?`, "pending").Scan(&n))
	require.Positive(t, n)
}

func doctorReachabilityRow(t *testing.T, raw []byte, name string) map[string]any {
	t.Helper()
	var report struct {
		Checks []map[string]any `json:"checks"`
	}
	require.NoError(t, json.Unmarshal(raw, &report))
	for _, row := range report.Checks {
		if row["name"] == name {
			return row
		}
	}
	t.Fatalf("missing doctor row %s", name)
	return nil
}

func TestRunSyncDoctor_UnreachableDependentChecksSkipped(t *testing.T) {
	for _, tc := range []struct{ name, dsn, kept string }{
		{"missing DSN", "", ""},
		{"refused connection", "postgres://fixture@127.0.0.1:1/fixture?sslmode=disable", ""},
		{"strict refused connection", "postgres://fixture@127.0.0.1:1/fixture?sslmode=disable", "fixture_role"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			initTestApp(t)
			t.Setenv(transport.EnvDSN, tc.dsn)
			if tc.kept != "" {
				_, err := app.configSvc.Set("sync.keep_roles", tc.kept)
				require.NoError(t, err)
			}
			seedOldDoctorQueue(t)
			var out bytes.Buffer
			app.jsonOutput = true
			require.ErrorIs(t, runSyncDoctor(t.Context(), &out, nil, transport.Options{InsecureTLS: true}), errDoctorChecksFailed)
			for _, name := range []string{"queue draining", schemaCurrentName, hubPrivilegesName} {
				row := doctorReachabilityRow(t, out.Bytes(), name)
				assert.Equal(t, false, row["pass"], name)
				assert.Equal(t, true, row["skipped"], name)
				assert.Contains(t, row["detail"], "hub unreachable", name)
			}
			var report DoctorReport
			require.NoError(t, json.Unmarshal(out.Bytes(), &report))
			for _, row := range doctorSyncRows(report) {
				switch row.ID {
				case "sync.queue-draining", "sync.schema-current", "sync.hub-privileges":
					assert.Equal(t, service.DoctorInfo, row.Status, row.ID)
					assert.Contains(t, row.Detail, "hub unreachable")
				case "sync.pg-reachable":
					assert.Equal(t, service.DoctorFail, row.Status)
				}
			}
			out.Reset()
			app.jsonOutput = false
			require.ErrorIs(t, runSyncDoctor(t.Context(), &out, nil, transport.Options{InsecureTLS: true}), errDoctorChecksFailed)
			for _, name := range []string{"queue draining", schemaCurrentName, hubPrivilegesName} {
				assert.Contains(t, out.String(), "[SKIPPED] "+name)
				assert.NotContains(t, out.String(), "[FAIL] "+name)
			}
			assert.NotContains(t, out.String(), "pusher may be stuck")
		})
	}
}

func TestDoctorCheck_SkippedAggregationAndRendering(t *testing.T) {
	// Decode the additive field so this test runs (and fails) against the old type.
	var skipped DoctorCheck
	require.NoError(t, json.Unmarshal([]byte(`{"name":"queue draining","pass":false,"skipped":true,"detail":"skipped (hub unreachable)"}`), &skipped))
	for _, failed := range []bool{false, true} {
		report := DoctorReport{OverallPass: true}
		if failed {
			report = appendCheck(report, "PG reachable", false, "unreachable")
		}
		report = appendDoctorCheck(report, skipped)
		assert.Equal(t, !failed, report.OverallPass)
		var out bytes.Buffer
		printDoctorTable(&out, report)
		assert.Contains(t, out.String(), "[SKIPPED] queue draining")
		assert.NotContains(t, out.String(), "[FAIL] queue draining")
		if !failed {
			assert.Contains(t, out.String(), "no checks failed (0 warnings, 1 skipped)")
			assert.NotContains(t, out.String(), "all checks passed")
		}
	}
}
