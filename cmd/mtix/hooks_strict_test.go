// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/hooks"
	"github.com/hyper-swe/mtix/internal/service"
)

func TestHooksStrict_OnOffRequireTerminalAndNoAgentSession(t *testing.T) {
	state := reviewProject(t)
	require.Error(t, runHooksStrict(state, "on", false, false), "no terminal")
	require.Error(t, runHooksStrict(state, "on", true, true), "agent session marker")
	require.Error(t, runHooksStrict(state, "bogus", true, false))
	captureStdout(t, func() { require.NoError(t, runHooksStrict(state, "on", true, false)) })
	require.Error(t, runHooksStrict(state, "off", false, false))
	require.Error(t, runHooksStrict(state, "off", true, true), "an agent cannot switch strict mode off")
	on, err := hooks.NewApprovals(app.mtixDir, state, false).StrictEnabled()
	require.NoError(t, err)
	require.True(t, on)
	captureStdout(t, func() { require.NoError(t, runHooksStrict(state, "off", true, false)) })
	on, err = hooks.NewApprovals(app.mtixDir, state, false).StrictEnabled()
	require.NoError(t, err)
	require.False(t, on)
}

func TestHooksStrict_StatusNeedsNoTerminalAndSupportsJSON(t *testing.T) {
	state := reviewProject(t)
	captureStdout(t, func() { require.NoError(t, runHooksStrict(state, "on", true, false)) })
	app.jsonOutput = true
	out := captureStdout(t, func() { require.NoError(t, runHooksStrict(state, "status", false, true)) })
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	require.ElementsMatch(t, []string{"enabled", "detail", "fix", "programs"}, keysOfMap(got))
	require.Equal(t, true, got["enabled"])
	require.Contains(t, got["fix"], "mtix hooks trust --program -- 'runner'")
	app.jsonOutput = false
	text := captureStdout(t, func() { require.NoError(t, runHooksStrict(state, "status", false, true)) })
	require.Contains(t, text, "Fix: ")
	require.Contains(t, text, "mtix hooks trust --program -- 'runner'")
}

func TestHooksTrustProgram_ApprovesOnTerminalOnly(t *testing.T) {
	state := reviewProject(t)
	approvals := hooks.NewApprovals(app.mtixDir, state, false)
	require.Error(t, runHooksTrustProgram(approvals, "runner", false, false), "no terminal")
	agent := hooks.NewApprovals(app.mtixDir, state, true)
	require.Error(t, runHooksTrustProgram(agent, "runner", false, true), "agent session")
	require.Error(t, runHooksTrustProgram(approvals, "pinned", false, true), "a pinned hook needs no program approval")
	require.Error(t, runHooksTrustProgram(approvals, "absent", false, true))
	text := captureStdout(t, func() { require.NoError(t, runHooksTrustProgram(approvals, "runner", false, true)) })
	require.Contains(t, text, "approved=true")
}

func TestHooksReport_StrictModeInJSONAndText(t *testing.T) {
	state := reviewProject(t)
	captureStdout(t, func() { require.NoError(t, runHooksStrict(state, "on", true, false)) })
	app.jsonOutput = true
	out := captureStdout(t, func() { _ = runHooksStatus(state) })
	var got struct {
		Strict hooks.StrictStatus `json:"strict"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	require.True(t, got.Strict.Enabled)
	require.NotEmpty(t, got.Strict.Programs)
	require.Contains(t, got.Strict.Fix, "mtix hooks trust --program")
	app.jsonOutput = false
	text := captureStdout(t, func() { _ = runHooksStatus(state) })
	require.Contains(t, text, "Strict program mode: true")
	require.Contains(t, text, "mtix hooks trust --program")
}

func TestDoctor_StrictCheckShapeAndFix(t *testing.T) {
	state := reviewProject(t)
	run := func() service.DoctorResult {
		svc, err := newConfiguredDoctorService()
		require.NoError(t, err)
		rows, err := svc.Run(context.Background(), service.DoctorRunOptions{})
		require.NoError(t, err)
		for _, row := range rows {
			if row.ID == "hooks.strict" {
				return row
			}
		}
		t.Fatal("hooks.strict row missing")
		return service.DoctorResult{}
	}
	off := run()
	require.Equal(t, service.DoctorPass, off.Status)
	require.Contains(t, off.Detail, "off")
	captureStdout(t, func() { require.NoError(t, runHooksStrict(state, "on", true, false)) })
	on := run()
	require.Equal(t, service.DoctorWarn, on.Status)
	require.Contains(t, on.Fix, "mtix hooks trust --program -- 'runner'")
	raw, err := json.Marshal(on)
	require.NoError(t, err)
	var generic map[string]any
	require.NoError(t, json.Unmarshal(raw, &generic))
	require.ElementsMatch(t, []string{"id", "status", "detail", "fix"}, keysOfMap(generic))
}

func TestHooksStrict_CommandIsRegisteredWithTrustProgramFlag(t *testing.T) {
	cmd := newHooksCmd()
	strict, _, err := cmd.Find([]string{"strict"})
	require.NoError(t, err)
	require.Equal(t, "strict", strict.Name())
	trust, _, err := cmd.Find([]string{"trust"})
	require.NoError(t, err)
	require.NotNil(t, trust.Flags().Lookup("program"))
}

func TestDoctor_StrictCheckFailsWhenTheRecordIsUnreadable(t *testing.T) {
	state := reviewProject(t)
	captureStdout(t, func() { require.NoError(t, runHooksStrict(state, "on", true, false)) })
	dir, err := state.Path()
	require.NoError(t, err)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		data, rerr := os.ReadFile(filepath.Join(dir, e.Name()))
		if rerr == nil && strings.Contains(string(data), "enabled") {
			require.NoError(t, os.WriteFile(filepath.Join(dir, e.Name()), []byte("{garbage"), 0o600))
		}
	}
	svc, err := newConfiguredDoctorService()
	require.NoError(t, err)
	rows, err := svc.Run(context.Background(), service.DoctorRunOptions{})
	require.Error(t, err, "a failing check fails the run")
	for _, row := range rows {
		if row.ID == "hooks.strict" {
			require.Equal(t, service.DoctorFail, row.Status, "every launch is refused, so this is a failure")
			require.Contains(t, row.Fix, "mtix hooks strict on")
			return
		}
	}
	t.Fatal("hooks.strict row missing")
}
