// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// runDoctorReport runs mtix sync doctor --json against the test hub.
func runDoctorReport(t *testing.T) (doctorJSON, error) {
	t.Helper()
	app.jsonOutput = true
	defer func() { app.jsonOutput = false }()
	var stdout, stderr bytes.Buffer
	err := runSyncDoctor(context.Background(), &stdout, &stderr, nil, cloudOpts)
	var report doctorJSON
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &report), stdout.String())
	return report, err
}

// doctorCheckNamed returns the check called name from report.
func doctorCheckNamed(t *testing.T, report doctorJSON, name string) (pass, warn bool, detail, fix string) {
	t.Helper()
	for _, c := range report.Checks {
		if c.Name == name {
			return c.Pass, c.Warn, c.Detail, c.Fix
		}
	}
	t.Fatalf("no %s check in the doctor report", name)
	return false, false, "", ""
}

// TestGradeHubObjects_StateAndMode_ReportsEachGapWithItsFix grades the
// hub-triggers check from what the hub holds: PASS when every function
// and trigger is present and enabled; otherwise a WARN by default and a
// FAIL in strict mode, naming each gap and the exact fix (MTIX-95.7).
func TestGradeHubObjects_StateAndMode_ReportsEachGapWithItsFix(t *testing.T) {
	const enable = "ALTER TABLE public.audit_log ENABLE TRIGGER audit_log_no_update;"
	missingFn := hubObjectState{functions: 2, triggers: 7, missingFunctions: []string{"audit_log_immutable"},
		missingTriggers: []string{"audit_log_no_update on public.audit_log"}}
	disabled := hubObjectState{functions: 2, triggers: 7,
		disabledTriggers: []string{"audit_log_no_update on public.audit_log (tgenabled D)"}, enableStatements: []string{enable}}
	both := missingFn
	both.disabledTriggers, both.enableStatements = disabled.disabledTriggers, disabled.enableStatements

	tests := []struct {
		name       string
		state      hubObjectState
		strict     bool
		wantPass   bool
		wantWarn   bool
		wantFix    string
		wantDetail []string
	}{
		{"every object present and enabled", hubObjectState{functions: 2, triggers: 7}, false, true, false, "",
			[]string{"every mtix function (2) and trigger (7) is present and enabled"}},
		{"every object present and enabled, strict mode", hubObjectState{functions: 2, triggers: 7}, true, true, false, "",
			[]string{"present and enabled"}},
		{"missing objects warn by default", missingFn, false, true, true, "mtix sync init",
			[]string{"missing functions: audit_log_immutable", "missing triggers: audit_log_no_update on public.audit_log",
				"as the table owner"}},
		{"missing objects fail in strict mode", missingFn, true, false, false, "mtix sync init",
			[]string{"strict mode", "missing functions: audit_log_immutable"}},
		{"a disabled trigger names its ALTER statement", disabled, false, true, true, enable,
			[]string{"triggers not enabled: audit_log_no_update on public.audit_log (tgenabled D)"}},
		{"missing and disabled: init, then the statement", both, false, true, true, "mtix sync init, then " + enable,
			[]string{"missing functions", "triggers not enabled"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := gradeHubObjects(tt.state, tt.strict)
			require.Equal(t, "hub-triggers", got.Name)
			require.Equal(t, tt.wantPass, got.Pass)
			require.Equal(t, tt.wantWarn, got.Warn)
			require.Equal(t, tt.wantFix, got.Fix)
			for _, want := range tt.wantDetail {
				require.Contains(t, got.Detail, want)
			}
		})
	}
}

// TestCheckHubObjects_HubNotReady_WarnsOrFailsStrict: when the hub is
// unreachable or its schema is not current, the check says it was
// skipped: a WARN by default, a FAIL in strict mode (MTIX-95.7).
func TestCheckHubObjects_HubNotReady_WarnsOrFailsStrict(t *testing.T) {
	for _, strict := range []bool{false, true} {
		t.Run(map[bool]string{false: "default", true: "strict"}[strict], func(t *testing.T) {
			initTestApp(t)
			if strict {
				setKeepRoles(t, "mtix_team")
			}
			got := checkHubObjects(context.Background(), "", false, transport.Options{})
			require.Equal(t, "hub-triggers", got.Name)
			require.Contains(t, got.Detail, "skipped")
			require.Equal(t, !strict, got.Pass)
			require.Equal(t, !strict, got.Warn)
		})
	}
}

// TestHubObjectState_Record_ClassifiesEachTrigger: a trigger is missing
// when tgenabled is empty, not enabled for anything but 'O' (disabled,
// replica-only or always), and fine at 'O' (MTIX-95.7).
func TestHubObjectState_Record_ClassifiesEachTrigger(t *testing.T) {
	const stmt = "ALTER TABLE public.audit_log ENABLE TRIGGER t;"
	tests := []struct {
		name         string
		schema       string
		enabled      string
		wantMissing  []string
		wantDisabled []string
	}{
		{"enabled", "public", "O", nil, nil},
		{"missing trigger", "public", "", []string{"t on public.audit_log"}, nil},
		{"missing table", "", "", []string{"t on audit_log"}, nil},
		{"disabled", "public", "D", nil, []string{"t on public.audit_log (tgenabled D)"}},
		{"replica only", "public", "R", nil, []string{"t on public.audit_log (tgenabled R)"}},
		{"always", "public", "A", nil, []string{"t on public.audit_log (tgenabled A)"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var s hubObjectState
			fix := ""
			if tt.enabled != "" {
				fix = stmt
			}
			s.record("audit_log", "t", tt.schema, tt.enabled, fix)
			require.Equal(t, tt.wantMissing, s.missingTriggers)
			require.Equal(t, tt.wantDisabled, s.disabledTriggers)
			if tt.wantDisabled != nil {
				require.Equal(t, []string{stmt}, s.enableStatements)
			} else {
				require.Empty(t, s.enableStatements)
			}
		})
	}
}

// TestRunSyncDoctor_HubUnreachable_ReportsHubTriggersSkipped: the doctor
// always reports the hub-triggers check; with the hub unreachable it is a
// WARN that says it was skipped, and it names no part of the DSN
// (MTIX-95.7).
func TestRunSyncDoctor_HubUnreachable_ReportsHubTriggersSkipped(t *testing.T) {
	initTestApp(t)
	t.Setenv(transport.EnvDSN, "postgres://doctor_user:Qx7doctorSecret@hub.example.invalid:5432/hub?connect_timeout=2")
	app.jsonOutput = true
	defer func() { app.jsonOutput = false }()
	var stdout, stderr bytes.Buffer
	err := runSyncDoctor(context.Background(), &stdout, &stderr, nil, transport.Options{})
	require.ErrorIs(t, err, errDoctorChecksFailed, "PG reachable fails")
	var report doctorJSON
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &report), stdout.String())
	pass, warn, detail, _ := doctorCheckNamed(t, report, "hub-triggers")
	require.True(t, pass)
	require.True(t, warn)
	require.Contains(t, detail, "skipped")
	require.NotContains(t, stdout.String(), "Qx7doctorSecret")
}
