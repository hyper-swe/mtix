// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// doctorCheckNamed returns the named check of a doctor JSON report.
func doctorCheckNamed(t *testing.T, raw []byte, name string) DoctorCheck {
	t.Helper()
	var report DoctorReport
	require.NoError(t, json.Unmarshal(raw, &report))
	for _, c := range report.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("doctor report has no %q check: %s", name, raw)
	return DoctorCheck{}
}

// TestRunSyncDoctor_Quarantined_FailsWithRetryAndInspectDetail: `mtix sync
// doctor` has a "quarantined events" check (MTIX-95.11 acceptance 5). It
// passes with none, and fails when any pulled event is quarantined, with a
// detail that says the events are retried on every pull, what to run next
// and how to inspect them; the JSON check carries only name, pass and
// detail. The hub checks fail here (no DSN), which does not change this
// local check.
func TestRunSyncDoctor_Quarantined_FailsWithRetryAndInspectDetail(t *testing.T) {
	tests := []struct {
		name       string
		n          int
		wantPass   bool
		wantDetail []string
		wantLine   *regexp.Regexp
	}{
		{"none", 0, true, []string{"ok"},
			regexp.MustCompile(`(?m)^\[PASS\] quarantined events\s+ok$`)},
		{"one", 1, false, []string{
			"1 pulled events quarantined, not applied",
			"retried on every pull",
			"run 'mtix sync pull'",
			"mtix sync quarantine list",
		}, regexp.MustCompile(`(?m)^\[FAIL\] quarantined events\s+1 pulled events quarantined`)},
		{"two", 2, false, []string{
			"2 pulled events quarantined, not applied",
			"retried on every pull",
			"run 'mtix sync pull'",
			"mtix sync quarantine list",
		}, regexp.MustCompile(`(?m)^\[FAIL\] quarantined events\s+2 pulled events quarantined`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			t.Setenv(transport.EnvDSN, "")
			ctx := context.Background()
			quarantineN(t, tt.n)

			var out, errOut bytes.Buffer
			app.jsonOutput = true
			require.ErrorIs(t, runSyncDoctor(ctx, &out, &errOut, nil, transport.Options{}), errDoctorChecksFailed)
			check := doctorCheckNamed(t, out.Bytes(), "quarantined events")
			require.Equal(t, tt.wantPass, check.Pass)
			for _, want := range tt.wantDetail {
				require.Contains(t, check.Detail, want)
			}
			var raw struct {
				Checks []map[string]any `json:"checks"`
			}
			require.NoError(t, json.Unmarshal(out.Bytes(), &raw))
			for _, c := range raw.Checks {
				if c["name"] == "quarantined events" {
					for key := range c {
						require.Contains(t, []string{"name", "pass", "detail"}, key,
							"the check uses only the existing DoctorCheck fields")
					}
				}
			}

			out.Reset()
			app.jsonOutput = false
			require.ErrorIs(t, runSyncDoctor(ctx, &out, &errOut, nil, transport.Options{}), errDoctorChecksFailed)
			require.Regexp(t, tt.wantLine, out.String())
		})
	}
}

// TestCheckQuarantinedEvents_NoStore_Fails: without a local store the check
// fails like the doctor's other local checks.
func TestCheckQuarantinedEvents_NoStore_Fails(t *testing.T) {
	report := appendQuarantineCheck(context.Background(), DoctorReport{OverallPass: true}, nil)

	require.False(t, report.OverallPass)
	require.Equal(t, []DoctorCheck{{Name: "quarantined events", Pass: false,
		Detail: "local store not initialized"}}, report.Checks)
}
