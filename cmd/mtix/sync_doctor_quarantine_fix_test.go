// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

// Quarantine Fix tests pin concrete text and JSON commands requiring operator input.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

func seedDoctorFixCandidate(t *testing.T, id, source string) {
	t.Helper()
	var event model.SyncEvent
	require.NoError(t, json.Unmarshal([]byte(ackCLIRaw), &event))
	event.EventID = id
	raw, err := json.Marshal(event)
	require.NoError(t, err)
	seedAckCLI(t, id, source, string(raw), "invalid node id")
}

func TestDoctorQuarantineFix_ExactTextAndJSON(t *testing.T) {
	initAckCLI(t)
	report := appendQuarantineCheck(t.Context(), DoctorReport{OverallPass: true}, app.store)
	want := "Inspect and report retained events (1 eligible unacknowledged; first shown): mtix sync quarantine list; add --reason '<your explanation>' after the handle, then: mtix sync quarantine acknowledge --handle " + service.QuarantineHandle(ackCLIEventID)
	require.Equal(t, want, report.Checks[0].Fix)
	var text bytes.Buffer
	printDoctorTable(&text, report)
	require.Contains(t, text.String(), "\n       fix: "+want+"\n")
	raw, err := json.Marshal(report)
	require.NoError(t, err)
	var decoded DoctorReport
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, want, decoded.Checks[0].Fix)
}

func TestDoctorQuarantineFix_FirstEligibleAndCountAcrossPages(t *testing.T) {
	initAckCLI(t)
	_, err := app.syncSvc.AcknowledgeQuarantine(t.Context(), ackCLIEventID, "op-first", "reviewed")
	require.NoError(t, err)
	seedDoctorFixCandidate(t, "a-push", "push")
	seedAckCLI(t, "b-malformed", "pull", "not-json", "invalid node id")
	for n := 0; n < 501; n++ {
		seedDoctorFixCandidate(t, fmt.Sprintf("event-%04d", n), "sweep")
	}
	fix, err := quarantineAcknowledgementFix(t.Context(), app.store, false)
	require.NoError(t, err)
	require.Equal(t, "Inspect and report retained events (501 eligible unacknowledged; first shown): mtix sync quarantine list; add --reason '<your explanation>' after the handle, then: mtix sync quarantine acknowledge --handle "+service.QuarantineHandle("event-0000"), fix)
}

func TestDoctorQuarantineFix_HostileIDsAreSingleShellArguments(t *testing.T) {
	for _, id := range []string{"-odd'$(printf altered); spaced", "space and $HOME", "double\"quote;semi"} {
		t.Run(id, func(t *testing.T) {
			initTestApp(t)
			seedDoctorFixCandidate(t, id, "pull")
			fix, err := quarantineAcknowledgementFix(t.Context(), app.store, false)
			require.NoError(t, err)
			_, command, ok := strings.Cut(fix, ", then: ")
			require.True(t, ok)
			// This local shell function prints arguments instead of running mtix.
			out, err := exec.CommandContext(t.Context(), "sh", "-c", `mtix() { printf '%s\000' "$@"; }; `+command).CombinedOutput()
			require.NoError(t, err, "%s", out)
			got := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
			require.Equal(t, []string{"sync", "quarantine", "acknowledge", "--handle", service.QuarantineHandle(id)}, got)
		})
	}
}

func TestDoctorQuarantineFix_NoEligibleEventsHasNoCommand(t *testing.T) {
	for _, source := range []string{"empty", "push", "acknowledged"} {
		t.Run(source, func(t *testing.T) {
			initTestApp(t)
			if source != "empty" {
				kind := "push"
				if source == "acknowledged" {
					kind = "pull"
				}
				seedDoctorFixCandidate(t, ackCLIEventID, kind)
				if source == "acknowledged" {
					_, err := app.syncSvc.AcknowledgeQuarantine(t.Context(), ackCLIEventID, "op-first", "reviewed")
					require.NoError(t, err)
				}
			}
			fix, err := quarantineAcknowledgementFix(t.Context(), app.store, false)
			require.NoError(t, err)
			require.Empty(t, fix)
		})
	}
}
