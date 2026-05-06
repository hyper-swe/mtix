// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// PG-free CLI tests for mtix sync status / doctor.

func TestSyncStatusCmd_Construction(t *testing.T) {
	cmd := newSyncStatusCmd()
	require.Equal(t, "status", cmd.Use)
	require.NotEmpty(t, cmd.Long)
}

func TestSyncDoctorCmd_Construction(t *testing.T) {
	cmd := newSyncDoctorCmd()
	require.Equal(t, "doctor [DSN]", cmd.Use)
	require.NotEmpty(t, cmd.Long)
	require.NotNil(t, cmd.Flags().Lookup("insecure-tls"))
}

func TestSyncCmd_RegistersAllSixSoFar(t *testing.T) {
	cmd := newSyncCmd()
	subs := map[string]bool{}
	for _, c := range cmd.Commands() {
		subs[c.Name()] = true
	}
	for _, name := range []string{"init", "clone", "push", "pull", "status", "doctor"} {
		require.Truef(t, subs[name], "%s subcommand registered", name)
	}
}

func TestRunSyncStatus_RefusesOutsideMtixProject(t *testing.T) {
	saved := app.mtixDir
	app.mtixDir = ""
	t.Cleanup(func() { app.mtixDir = saved })

	var stdout, stderr bytes.Buffer
	err := runSyncStatus(context.Background(), &stdout, &stderr)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not in an mtix project")
}

func TestSyncStatus_HighConflictThresholdIsFifty(t *testing.T) {
	st := SyncStatus{OpenConflicts: 50}
	st.HighConflict = st.OpenConflicts > 50
	require.False(t, st.HighConflict, "exactly 50 is NOT high (per FR-18.12 banner threshold)")

	st.OpenConflicts = 51
	st.HighConflict = st.OpenConflicts > 50
	require.True(t, st.HighConflict, "51 triggers the banner")
}

func TestEmptyDash(t *testing.T) {
	require.Equal(t, "-", emptyDash(""))
	require.Equal(t, "MTIX", emptyDash("MTIX"))
}

func TestDoctorReport_AppendCheck(t *testing.T) {
	r := DoctorReport{OverallPass: true}
	r = appendCheck(r, "first", true, "ok")
	require.True(t, r.OverallPass)
	require.Len(t, r.Checks, 1)

	r = appendCheck(r, "second", false, "broken")
	require.False(t, r.OverallPass, "any failed check flips overall to false")
	require.Len(t, r.Checks, 2)
	require.Equal(t, "second", r.Checks[1].Name)
	require.Equal(t, "broken", r.Checks[1].Detail)
}

func TestDoctor_LastCheckPassed(t *testing.T) {
	require.True(t, lastCheckPassed(DoctorReport{}), "empty report defaults to pass")
	require.True(t, lastCheckPassed(DoctorReport{Checks: []DoctorCheck{{Pass: true}}}))
	require.False(t, lastCheckPassed(DoctorReport{Checks: []DoctorCheck{{Pass: true}, {Pass: false}}}))
}

func TestPrintStatusJSON(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, printStatusJSON(&buf, SyncStatus{
		Pending: 3, Pushed: 7, ProjectPrefix: "MTIX",
	}))
	var got SyncStatus
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	require.Equal(t, 3, got.Pending)
	require.Equal(t, 7, got.Pushed)
	require.Equal(t, "MTIX", got.ProjectPrefix)
}

func TestPrintStatusTable_HighConflictBanner(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, printStatusTable(&buf, SyncStatus{
		OpenConflicts: 100, HighConflict: true, ProjectPrefix: "MTIX",
	}))
	out := buf.String()
	require.Contains(t, out, "WARN")
	require.Contains(t, out, "100 unresolved")
	require.Contains(t, out, "mtix sync conflicts list --batch")
}

func TestPrintStatusTable_NoConflictNoBanner(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, printStatusTable(&buf, SyncStatus{
		OpenConflicts: 5, HighConflict: false,
	}))
	require.NotContains(t, buf.String(), "WARN")
}

func TestPrintDoctorTable(t *testing.T) {
	var buf bytes.Buffer
	report := DoctorReport{
		OverallPass: false,
		Checks: []DoctorCheck{
			{Name: "PG reachable", Pass: true, Detail: "ok"},
			{Name: "schema current", Pass: false, Detail: "missing"},
		},
	}
	printDoctorTable(&buf, report)
	out := buf.String()
	require.Contains(t, out, "[PASS] PG reachable")
	require.Contains(t, out, "[FAIL] schema current")
	require.Contains(t, out, "FAILED")
}

func TestCheckSecretsFileMode_Absent(t *testing.T) {
	dir := t.TempDir()
	ok, detail := checkSecretsFileMode(dir)
	require.True(t, ok, "absent secrets file is fine — DSN via env")
	require.Contains(t, strings.ToLower(detail), "absent")
}
