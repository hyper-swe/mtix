// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// TestDoctorCheck_JSON_OptionalFieldsOmitted: a check that is not a
// warning and has no fix or findings marshals exactly as before, so every
// existing check is unchanged for agents (MTIX-95.1).
func TestDoctorCheck_JSON_OptionalFieldsOmitted(t *testing.T) {
	body, err := json.Marshal(DoctorCheck{Name: "PG reachable", Pass: true, Detail: "ok"})
	require.NoError(t, err)
	require.JSONEq(t, `{"name":"PG reachable","pass":true,"detail":"ok"}`, string(body))

	body, err = json.Marshal(DoctorCheck{
		Name: "hub-privileges", Pass: true, Warn: true, Detail: "d", Fix: "mtix sync harden",
		Findings: []transport.PrivilegeFinding{{Role: "anon", Object: "public.audit_log", Kind: "table", Via: "grant"}},
	})
	require.NoError(t, err)
	var got map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &got))
	for _, key := range []string{"name", "pass", "warn", "detail", "fix", "findings"} {
		require.Contains(t, got, key)
	}
}

// TestAppendDoctorCheck_Warn_KeepsOverallPass: a WARN (warn with pass)
// keeps the report passing, so the doctor exits 0; a failing check still
// fails it (MTIX-95.1).
func TestAppendDoctorCheck_Warn_KeepsOverallPass(t *testing.T) {
	r := DoctorReport{OverallPass: true}
	r = appendDoctorCheck(r, DoctorCheck{Name: "hub-privileges", Pass: true, Warn: true, Detail: "w"})
	require.True(t, r.OverallPass)
	require.Len(t, r.Checks, 1)
	r = appendDoctorCheck(r, DoctorCheck{Name: "other", Pass: false})
	require.False(t, r.OverallPass)
}

// TestPrintDoctorTable_Warn_ShowsWarnMark: a warning prints as WARN, the
// other marks are unchanged, and the summary says the checks passed with a
// warning (MTIX-95.1).
func TestPrintDoctorTable_Warn_ShowsWarnMark(t *testing.T) {
	var buf bytes.Buffer
	printDoctorTable(&buf, DoctorReport{OverallPass: true, Checks: []DoctorCheck{
		{Name: "PG reachable", Pass: true, Detail: "ok"},
		{Name: "hub-privileges", Pass: true, Warn: true, Detail: "anon can read"},
	}})
	out := buf.String()
	require.Contains(t, out, "[PASS] PG reachable         ok\n")
	require.Contains(t, out, "[WARN] hub-privileges       anon can read\n")
	require.Contains(t, out, "all checks passed (1 warning)")

	buf.Reset()
	printDoctorTable(&buf, DoctorReport{OverallPass: true, Checks: []DoctorCheck{{Name: "a", Pass: true, Detail: "ok"}}})
	require.Contains(t, buf.String(), "all checks passed\n", "without warnings the summary is unchanged")
}
