// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// Copying the printed command cannot record a receipt with a default reason.
func TestDoctorQuarantineFix_CopiedCommandRequiresOperatorReason(t *testing.T) {
	for _, jsonOutput := range []bool{false, true} {
		t.Run(map[bool]string{false: "text", true: "json"}[jsonOutput], func(t *testing.T) {
			t.Setenv(transport.EnvDSN, "")
			initTestApp(t)
			app.jsonOutput = jsonOutput
			app.authorID = "local-operator"
			id := "-event'with space"
			seedDoctorFixCandidate(t, id, "pull")
			report := appendQuarantineCheck(t.Context(), DoctorReport{OverallPass: true}, app.store)
			fix := report.Checks[0].Fix
			_, command, ok := strings.Cut(fix, ", then: ")
			require.True(t, ok)
			out, err := exec.CommandContext(t.Context(), "sh", "-c", `mtix() { printf '%s\000' "$@"; }; `+command).CombinedOutput()
			require.NoError(t, err, "%s", out)
			args := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
			require.GreaterOrEqual(t, len(args), 3)
			require.Equal(t, []string{"sync", "quarantine", "acknowledge"}, args[:3])

			cmd := newSyncQuarantineCmd()
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			cmd.SetArgs(args[2:])
			require.ErrorContains(t, cmd.ExecuteContext(t.Context()), "required flag")
			counts, err := app.store.CountQuarantineAcknowledgements(t.Context())
			require.NoError(t, err)
			require.Zero(t, counts.Acknowledged)
			require.Equal(t, []string{"sync", "quarantine", "acknowledge", "--handle", service.QuarantineHandle(id)}, args)
			require.Contains(t, fix, "add --reason '<your explanation>' after the handle")

			// The operator inserts both the flag and its explanation before --.
			const reason = "node owner confirmed this event must remain unapplied"
			withReason := append([]string{"acknowledge", "--reason", reason}, args[3:]...)
			cmd = newSyncQuarantineCmd()
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			cmd.SetArgs(withReason)
			require.NoError(t, cmd.ExecuteContext(t.Context()))
			rows, err := app.syncSvc.ListQuarantined(t.Context())
			require.NoError(t, err)
			require.Len(t, rows, 1)
			require.NotNil(t, rows[0].Acknowledgement)
			require.Equal(t, reason, rows[0].Acknowledgement.Reason)
			require.Equal(t, id, rows[0].Acknowledgement.EventID)
			require.Equal(t, "local-operator", rows[0].Acknowledgement.Actor)
			ok, detail := checkQuarantinedEvents(t.Context(), app.store)
			require.True(t, ok, detail)
		})
	}
}
