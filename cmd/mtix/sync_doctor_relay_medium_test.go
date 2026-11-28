// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests RELAY_MEDIUM_UNREADABLE in the doctor row, relay status and the
// stall recovery text (FR-21 section 9, MTIX-290).
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// unreadableSegment seeds a peer whose only segment cannot be opened.
func unreadableSegment(t *testing.T) (dir, path string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not constrain root")
	}
	dir, _ = initDiagnosticRelay(t, false)
	path = diagnosticSegment(t, dir, diagnosticOther, 1, diagnosticCurrent())
	require.NoError(t, os.Chmod(path, 0o000))
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	return dir, path
}

func TestCheckRelayMedium_UnreadableSegment_NamesCodeAndFixInJSON(t *testing.T) {
	dir, path := unreadableSegment(t)
	row := checkRelayMedium(t.Context(), dir)
	require.False(t, row.Pass)
	require.Contains(t, row.Detail, "RELAY_MEDIUM_UNREADABLE")
	require.Contains(t, row.Detail, "peers/"+diagnosticOther+"/segments/")
	require.Contains(t, row.Detail, "permission denied")
	require.NotContains(t, row.Detail, dir, "the message is relay-relative")
	require.Contains(t, row.Fix, "mounted and readable by this user")

	raw, err := json.Marshal(row)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Contains(t, got["detail"], "RELAY_MEDIUM_UNREADABLE")
	require.Contains(t, got["fix"], "mounted and readable by this user")

	require.NoError(t, os.Chmod(path, 0o600))
	require.True(t, checkRelayMedium(t.Context(), dir).Pass, "clears once the medium reads")
}

func TestCheckRelayMedium_UnreadableSegment_TextOutputPrintsTheFixOnce(t *testing.T) {
	dir, _ := unreadableSegment(t)
	row := checkRelayMedium(t.Context(), dir)
	require.NotContains(t, row.Detail, "fix:", "the fix is in Fix only")

	var out bytes.Buffer
	printDoctorTable(&out, DoctorReport{Checks: []DoctorCheck{row}})
	text := out.String()
	require.Contains(t, text, "[FAIL] "+relayMediumRow)
	require.Contains(t, text, "RELAY_MEDIUM_UNREADABLE")
	require.Equal(t, 1, strings.Count(text, "mounted and readable by this user"),
		"the recovery text appears exactly once")
	require.Equal(t, 1, strings.Count(text, "fix:"), "one fix line")
}

func TestCheckRelayMedium_Healthy_Passes(t *testing.T) {
	dir, _ := initDiagnosticRelay(t, false)
	diagnosticSegment(t, dir, diagnosticOther, 1, diagnosticCurrent())
	require.True(t, checkRelayMedium(t.Context(), dir).Pass)
}

func TestAppendRelayChecks_UnreadableMedium_HasMediumRow(t *testing.T) {
	unreadableSegment(t)
	rep := appendRelayChecks(t.Context(), DoctorReport{OverallPass: true})
	var found bool
	for _, c := range rep.Checks {
		if c.Name == relayMediumRow {
			found = true
			require.False(t, c.Pass)
		}
	}
	require.True(t, found, "doctor carries the relay medium row")
}

func TestCollectRelayStatus_UnreadableSegment_SurfacesCodeHumanAndJSON(t *testing.T) {
	unreadableSegment(t)
	rep, err := collectRelayStatus(t.Context())
	require.NoError(t, err)
	var stalled *relayPeerStatus
	for i := range rep.Peers {
		if rep.Peers[i].PeerID == diagnosticOther {
			stalled = &rep.Peers[i]
		}
	}
	require.NotNil(t, stalled)
	require.Equal(t, "RELAY_MEDIUM_UNREADABLE", stalled.StallCode)
	require.Contains(t, stalled.StallReason, "permission denied")

	raw, err := json.Marshal(rep)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"stall_code":"RELAY_MEDIUM_UNREADABLE"`)

	cmd := newRelayStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	printRelayStatus(cmd, rep)
	require.Contains(t, out.String(), "STALLED (RELAY_MEDIUM_UNREADABLE)")
	require.Contains(t, out.String(), "mounted and readable by this user")
}

func TestRelayStallRecovery_MediumUnreadable_IsNotTheGenericFallback(t *testing.T) {
	got := relayStallRecovery("RELAY_MEDIUM_UNREADABLE")
	require.NotEqual(t, relayStallRecovery(""), got)
	require.True(t, strings.Contains(got, "mounted and readable by this user"))
}
