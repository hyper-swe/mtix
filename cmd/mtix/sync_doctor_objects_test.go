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
// FAIL in strict mode, naming each gap and the exact fix, which names the
// table owner who runs it. mtix sync init repairs a missing trigger and a
// trigger bound to another function alike, with no separate step
// (MTIX-95.7).
func TestGradeHubObjects_StateAndMode_ReportsEachGapWithItsFix(t *testing.T) {
	const enable = "ALTER TABLE public.audit_log ENABLE TRIGGER audit_log_no_update;"
	const owner = "as the table owner (mtix_owner): "
	missingFn := hubObjectState{functions: 2, triggers: 7, missingFunctions: []string{"audit_log_immutable"},
		missingTriggers: []string{"audit_log_no_update on public.audit_log"}}
	disabled := hubObjectState{functions: 2, triggers: 7, owners: []string{"mtix_owner"},
		disabledTriggers: []string{"audit_log_no_update on public.audit_log (tgenabled D)"}, enableStatements: []string{enable}}
	both := missingFn
	both.disabledTriggers, both.enableStatements = disabled.disabledTriggers, disabled.enableStatements
	wrongFn := hubObjectState{functions: 2, triggers: 7, owners: []string{"mtix_owner"},
		wrongFunction: []string{"audit_log_no_update on public.audit_log calls noop, not audit_log_immutable"}}
	twoOwners := wrongFn
	twoOwners.owners = []string{"mtix_a", "mtix_b"}
	wrongAndDisabled := wrongFn
	wrongAndDisabled.disabledTriggers, wrongAndDisabled.enableStatements = disabled.disabledTriggers, disabled.enableStatements

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
		{"missing objects warn by default", missingFn, false, true, true, "as the table owner: mtix sync init",
			[]string{"missing functions: audit_log_immutable", "missing triggers: audit_log_no_update on public.audit_log",
				"as the table owner"}},
		{"missing objects fail in strict mode", missingFn, true, false, false, "as the table owner: mtix sync init",
			[]string{"strict mode", "missing functions: audit_log_immutable"}},
		{"a disabled trigger names its ALTER statement", disabled, false, true, true, owner + enable,
			[]string{"triggers not enabled: audit_log_no_update on public.audit_log (tgenabled D)"}},
		{"missing and disabled: init, then the statement", both, false, true, true,
			"as the table owner: mtix sync init, then " + enable,
			[]string{"missing functions", "triggers not enabled"}},
		{"another function: init alone", wrongFn, false, true, true, owner + "mtix sync init",
			[]string{"triggers calling another function: audit_log_no_update on public.audit_log calls noop, not audit_log_immutable"}},
		{"tables with two owners name both", twoOwners, false, true, true, "as the table owner (mtix_a, mtix_b): mtix sync init",
			[]string{"triggers calling another function"}},
		{"another function and disabled", wrongAndDisabled, true, false, false, owner + "mtix sync init, then " + enable,
			[]string{"strict mode", "triggers calling another function", "triggers not enabled"}},
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
// when tgenabled is empty; it calls the wrong function when its function
// is not the one its migration binds; it is not enabled when tgenabled is
// D (disabled) or R (replica sessions only); O and A (always) are both
// enabled, as for mtix sync harden (MTIX-95.7).
func TestHubObjectState_Record_ClassifiesEachTrigger(t *testing.T) {
	const enable = "ALTER TABLE public.audit_log ENABLE TRIGGER t;"
	row := func(schema, enabled, function string) triggerRow {
		r := triggerRow{table: "audit_log", name: "t", schema: schema, enabled: enabled,
			function: function, wantFunction: "audit_log_immutable"}
		if schema != "" {
			r.owner = "mtix_owner"
		}
		if enabled != "" {
			r.enable = enable
		}
		return r
	}
	tests := []struct {
		name         string
		row          triggerRow
		wantMissing  []string
		wantDisabled []string
		wantWrongFn  []string
	}{
		{"enabled", row("public", "O", "audit_log_immutable"), nil, nil, nil},
		{"always", row("public", "A", "audit_log_immutable"), nil, nil, nil},
		{"missing trigger", row("public", "", ""), []string{"t on public.audit_log"}, nil, nil},
		{"missing table", row("", "", ""), []string{"t on audit_log"}, nil, nil},
		{"disabled", row("public", "D", "audit_log_immutable"), nil, []string{"t on public.audit_log (tgenabled D)"}, nil},
		{"replica only", row("public", "R", "audit_log_immutable"), nil, []string{"t on public.audit_log (tgenabled R)"}, nil},
		{"another function", row("public", "O", "noop"), nil, nil,
			[]string{"t on public.audit_log calls noop, not audit_log_immutable"}},
		{"another function, disabled", row("public", "D", "noop"), nil, nil,
			[]string{"t on public.audit_log calls noop, not audit_log_immutable"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var s hubObjectState
			s.record(tt.row)
			require.Equal(t, tt.wantMissing, s.missingTriggers)
			require.Equal(t, tt.wantDisabled, s.disabledTriggers)
			require.Equal(t, tt.wantWrongFn, s.wrongFunction)
			if tt.wantDisabled != nil {
				require.Equal(t, []string{enable}, s.enableStatements)
			} else {
				require.Empty(t, s.enableStatements)
			}
			if tt.row.schema != "" {
				require.Equal(t, []string{"mtix_owner"}, s.owners, "the table's owner is recorded")
			} else {
				require.Empty(t, s.owners)
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
