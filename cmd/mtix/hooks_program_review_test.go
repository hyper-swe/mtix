// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/hooks"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
)

// reviewProject writes hooks.yaml with one pinned-script hook and one hook
// whose program is not a pinned project script.
func reviewProject(t *testing.T) (state *oplocal.State) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses Unix programs")
	}
	saveAndResetApp(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	app.mtixDir = filepath.Join(root, ".mtix")
	require.NoError(t, os.Mkdir(app.mtixDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "wake.sh"), []byte("#!/bin/sh\nexit 0\n"), 0o700))
	config := "hooks:\n" +
		"  - name: pinned\n    match: {events: [comment.addressed]}\n    deliver: [exec]\n    exec: {command: [sh, wake.sh]}\n" +
		"  - name: runner\n    match: {events: [comment.addressed]}\n    deliver: [exec]\n    exec: {command: [find, ., -maxdepth, \"0\"]}\n" +
		"  - name: pending\n    match: {events: [comment.addressed]}\n    deliver: [exec]\n    exec: {command: [find, ., -maxdepth, \"1\"]}\n"
	require.NoError(t, os.WriteFile(filepath.Join(app.mtixDir, "hooks.yaml"), []byte(config), 0o600))
	home, err := filepath.EvalSymlinks(operatorTestHome(t))
	require.NoError(t, err)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("APPDATA", filepath.Join(home, "config"))
	env, err := oplocal.FromProcess()
	require.NoError(t, err)
	return oplocal.New(env)
}

func TestHooksTrust_ProgramReviewNoticeOnlyForOtherPrograms(t *testing.T) {
	state := reviewProject(t)
	approvals := hooks.NewApprovals(app.mtixDir, state, false)

	var notice string
	stdout := captureStdout(t, func() {
		notice = captureStderr(t, func() { require.NoError(t, runHooksTrustInput(approvals, "pinned", false, true)) })
	})
	require.NotContains(t, notice, "Program review", "a pinned hook shows no notice")
	require.NotContains(t, stdout, "Program review")

	notice = ""
	captureStdout(t, func() {
		notice = captureStderr(t, func() { require.NoError(t, runHooksTrustInput(approvals, "runner", false, true)) })
	})
	require.Contains(t, notice, "Program review")
	require.Contains(t, notice, "runner")
	require.Contains(t, notice, "pinned project script")

	app.jsonOutput = true
	var out string
	captureStderr(t, func() {
		out = captureStdout(t, func() { require.NoError(t, runHooksTrustInput(approvals, "runner", false, true)) })
	})
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	review, ok := got["program_review"].(map[string]any)
	require.True(t, ok, "the JSON result carries program_review")
	require.Equal(t, true, review["review_needed"])
}

func TestHookProgramReview_StatusReportsPerHook(t *testing.T) {
	state := reviewProject(t)
	app.jsonOutput = true
	out := captureStdout(t, func() { require.NoError(t, runHooksStatus(state)) })
	var report struct {
		Hooks []struct {
			Name          string `json:"name"`
			ProgramReview *struct {
				Class  string `json:"class"`
				Needed bool   `json:"review_needed"`
				Fix    string `json:"fix"`
			} `json:"program_review"`
		} `json:"hooks"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	byName := map[string]bool{}
	for _, h := range report.Hooks {
		require.NotNil(t, h.ProgramReview, h.Name)
		byName[h.Name] = h.ProgramReview.Needed
		if h.Name == "runner" {
			require.Equal(t, "other", h.ProgramReview.Class)
			require.Contains(t, h.ProgramReview.Fix, "pinned project script")
		}
	}
	require.Equal(t, map[string]bool{"pinned": false, "runner": true, "pending": true}, byName)

	app.jsonOutput = false
	text := captureStdout(t, func() { require.NoError(t, runHooksStatus(state)) })
	require.Contains(t, text, "runner: program review needed")
	require.NotContains(t, text, "pinned: program review", "a pinned hook has no review line")
}

func approveAllExec(t *testing.T, state *oplocal.State) {
	t.Helper()
	approvals := hooks.NewApprovals(app.mtixDir, state, false)
	cfg, _ := hooks.Load(app.mtixDir)
	for _, h := range cfg.Hooks {
		if h.Name == "pending" {
			continue // stays unapproved
		}
		_, err := approvals.ApproveSelected(h, true)
		require.NoError(t, err)
	}
}

func TestDoctor_ProgramsCheck(t *testing.T) {
	state := reviewProject(t)
	approveAllExec(t, state)
	svc, err := newConfiguredDoctorService()
	require.NoError(t, err)
	rows, err := svc.Run(context.Background(), service.DoctorRunOptions{})
	require.NoError(t, err)
	var found *service.DoctorResult
	for i := range rows {
		if rows[i].ID == "hooks.programs" {
			found = &rows[i]
		}
	}
	require.NotNil(t, found, "the hooks.programs check is registered")
	require.Equal(t, service.DoctorWarn, found.Status)
	require.Contains(t, found.Detail, "runner")
	require.True(t, strings.HasSuffix(found.Detail, ": runner"), "only the hook that needs review is listed")
	require.Contains(t, found.Fix, "mtix hooks trust -- 'runner'")
	raw, err := json.Marshal(rows)
	require.NoError(t, err)
	var generic []map[string]any
	require.NoError(t, json.Unmarshal(raw, &generic))
	for _, row := range generic {
		require.ElementsMatch(t, []string{"id", "status", "detail", "fix"}, keysOfMap(row))
	}
}

func TestDoctor_ProgramsCheckPassesWhenNothingNeedsReview(t *testing.T) {
	state := reviewProject(t)
	config := "hooks:\n  - name: pinned\n    match: {events: [comment.addressed]}\n    deliver: [exec]\n    exec: {command: [sh, wake.sh]}\n"
	require.NoError(t, os.WriteFile(filepath.Join(app.mtixDir, "hooks.yaml"), []byte(config), 0o600))
	approveAllExec(t, state)
	svc, err := newConfiguredDoctorService()
	require.NoError(t, err)
	rows, err := svc.Run(context.Background(), service.DoctorRunOptions{})
	require.NoError(t, err)
	for _, row := range rows {
		if row.ID == "hooks.programs" {
			require.Equal(t, service.DoctorPass, row.Status)
			return
		}
	}
	t.Fatal("hooks.programs row missing")
}

func keysOfMap(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func TestProgramsResult_CountsUnclassifiedHooks(t *testing.T) {
	decisions := map[string]hooks.Decision{
		"ok":      {Name: "ok", Trusted: true, ProgramReview: &hooks.ProgramReview{Class: hooks.ClassPinnedScript}},
		"unknown": {Name: "unknown", Trusted: true, ProgramReview: &hooks.ProgramReview{Class: hooks.ClassUnknown, Needed: true}},
		"none":    {Name: "none", Trusted: true},
	}
	row := programsResult(decisions, nil, nil)
	require.Equal(t, service.DoctorWarn, row.Status)
	require.Contains(t, row.Detail, "unknown")
	require.Contains(t, row.Detail, "none", "a trusted hook with no review at all is treated as unclassified")
	require.NotContains(t, row.Detail, " ok")
}

func TestDoctor_ProgramsCheckReportsALoadFailureInsteadOfDroppingTheRow(t *testing.T) {
	reviewProject(t)
	require.NoError(t, os.WriteFile(filepath.Join(app.mtixDir, "hooks.yaml"), []byte("hooks: [oops\n"), 0o600))
	svc, err := newConfiguredDoctorService()
	require.NoError(t, err)
	rows, err := svc.Run(context.Background(), service.DoctorRunOptions{})
	require.NoError(t, err)
	for _, row := range rows {
		if row.ID == "hooks.programs" {
			require.Equal(t, service.DoctorWarn, row.Status)
			require.Contains(t, row.Detail, "parse failed")
			require.NotEmpty(t, row.Fix)
			return
		}
	}
	t.Fatal("hooks.programs row missing")
}

func TestDoctor_ProgramsCheckHasNoRowWithoutHooksFile(t *testing.T) {
	reviewProject(t)
	require.NoError(t, os.Remove(filepath.Join(app.mtixDir, "hooks.yaml")))
	svc, err := newConfiguredDoctorService()
	require.NoError(t, err)
	rows, err := svc.Run(context.Background(), service.DoctorRunOptions{})
	require.NoError(t, err)
	for _, row := range rows {
		require.NotEqual(t, "hooks.programs", row.ID)
	}
}
