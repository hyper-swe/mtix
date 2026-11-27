// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package main

import (
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
	"github.com/hyper-swe/mtix/internal/sync/pushlock"
)

func TestHooksStatus_ValidateInput(t *testing.T) {
	cmd := newHooksCmd()
	status, _, err := cmd.Find([]string{"status"})
	require.NoError(t, err)
	require.Equal(t, "status", status.Name())
	root, err := filepath.EvalSymlinks(operatorTestHome(t))
	require.NoError(t, err)
	state := oplocal.New(operatorTestEnv(root))
	saveAndResetApp(t)
	app.jsonOutput = true
	text := captureStdout(t, func() { require.NoError(t, runHooksStatus(state)) })
	require.Contains(t, text, `"status": "absent"`)
	require.Contains(t, text, oplocal.PlacementLimit)
	require.NoError(t, state.Ensure())
	text = captureStdout(t, func() { require.NoError(t, runHooksStatus(state)) })
	require.Contains(t, text, `"status": "ready"`)
	require.Contains(t, text, oplocal.PlacementLimit)
}

func TestHooksStatus_Output(t *testing.T) {
	root, err := filepath.EvalSymlinks(operatorTestHome(t))
	require.NoError(t, err)
	env := operatorTestEnv(root)
	env.Values["CODEX_HOME"] = root
	state := oplocal.New(env)
	saveAndResetApp(t)
	text := captureStdout(t, func() { require.Error(t, runHooksStatus(state)) })
	require.Contains(t, text, "refused")
	require.Contains(t, text, oplocal.PlacementLimit)
	require.Contains(t, text, "mtix hooks status --json")
	app.jsonOutput = true
	text = captureStdout(t, func() { require.Error(t, runHooksStatus(state)) })
	require.Contains(t, text, `"status": "refused"`)
	require.Contains(t, text, oplocal.PlacementLimit)
	require.Contains(t, text, "directory must be outside")
}

func TestHooksStatus_Command(t *testing.T) {
	saveAndResetApp(t)
	temp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", temp)
	t.Setenv("APPDATA", temp)
	t.Setenv("CODEX_HOME", temp)
	cmd := newHooksStatusCmd()
	text := captureStdout(t, func() { require.Error(t, cmd.RunE(cmd, nil)) })
	require.Contains(t, text, "Operator-local state: refused")
}

func TestHooksStatus_Reports(t *testing.T) {
	saveAndResetApp(t)
	root, err := filepath.EvalSymlinks(operatorTestHome(t))
	require.NoError(t, err)
	state := oplocal.New(operatorTestEnv(root))
	require.NoError(t, state.WriteJSON("hooks", map[string]int{}))
	dir, err := state.Path()
	require.NoError(t, err)
	body := []byte(`{"host_id":"00000000000000000000000000000000","data":{}}`)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hooks.json"), body, 0600))
	text := captureStdout(t, func() { require.NoError(t, runHooksStatus(state)) })
	require.Contains(t, text, "record belongs to another host")
}

func TestHooksStatus_HomeInput(t *testing.T) {
	saveAndResetApp(t)
	key := "HOME"
	if runtime.GOOS == "windows" {
		key = "USERPROFILE"
	}
	t.Setenv(key, "")
	cmd := newHooksStatusCmd()
	require.ErrorContains(t, cmd.RunE(cmd, nil), "resolve home")
}

func TestHookStatusReadersWithHeldSyncLock(t *testing.T) {
	pulledProject(t, "prefix: TEST\n")
	mtixDir := ".mtix"
	config := `hooks:
  - name: zulu
    match: {events: [comment.addressed]}
    deliver: [exec]
    exec: {command: [sh, wake.sh]}
  - name: alpha
    match: {events: [comment.addressed]}
    deliver: [exec]
    exec: {command: [sh, wake.sh]}
  - name: ' alpha'
    match: {events: [comment.addressed]}
    deliver: [exec]
    exec: {command: [sh, wake.sh]}
  - name: pending
    match: {events: [comment.addressed]}
    deliver: [exec]
    exec: {command: [sh, wake.sh]}
`
	configPath := filepath.Join(mtixDir, "hooks.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(config), 0600))
	require.NoError(t, os.WriteFile("wake.sh", []byte("#!/bin/sh\nexit 0\n"), 0700))
	env, err := oplocal.FromProcess()
	require.NoError(t, err)
	state := oplocal.New(env)
	approvals := hooks.NewApprovals(mtixDir, state, false)
	for _, name := range []string{" alpha", "alpha", "zulu"} {
		require.NoError(t, approvals.Approve(name, true))
	}
	doctorArgs := []string{"doctor", "--json"}
	unlockedDoctor, err := runHookStatusReader(t, false, doctorArgs...)
	require.NoError(t, err, unlockedDoctor)
	wantDoctorIDs := doctorCheckIDs(t, unlockedDoctor)
	lock, err := pushlock.AcquireNamed(mtixDir, "sync.lock")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, lock.Release()) })

	for _, tc := range []struct {
		name      string
		args      []string
		wantStale bool
		check     func(*testing.T, string)
	}{
		{"trust text", []string{"hooks", "trust", "--status"}, true, func(t *testing.T, out string) {
			require.Equal(t, "\" alpha\": trusted=true\n\"alpha\": trusted=true\n\"zulu\": trusted=true\n", out)
		}},
		{"trust json", []string{"hooks", "trust", "--status", "--json"}, true, func(t *testing.T, out string) {
			var result struct {
				Data        []hooks.Decision `json:"data"`
				Stale       bool             `json:"stale"`
				StaleReason string           `json:"stale_reason"`
			}
			require.NoError(t, json.Unmarshal([]byte(out), &result), out)
			require.True(t, result.Stale)
			require.Equal(t, "sync_lock_busy", result.StaleReason)
			require.Len(t, result.Data, 3)
			for i, name := range []string{" alpha", "alpha", "zulu"} {
				require.Equal(t, name, result.Data[i].Name)
				require.True(t, result.Data[i].Trusted)
			}
		}},
		{"hooks list", []string{"hooks", "list"}, true, func(t *testing.T, out string) {
			for _, name := range []string{"alpha", "pending", "zulu"} {
				require.Contains(t, out, name+"\n")
			}
		}},
		{"doctor json", doctorArgs, false, func(t *testing.T, out string) {
			require.ElementsMatch(t, wantDoctorIDs, doctorCheckIDs(t, out))
		}},
		{"inbox status", []string{"inbox", "status", "--agent=reader"}, true, func(t *testing.T, out string) {
			require.Contains(t, out, "current: ")
			require.Contains(t, out, "no wake rows recorded for this agent")
		}},
		{"ready json", []string{"ready", "--json"}, true, func(t *testing.T, out string) {
			var result struct {
				Nodes []struct{ ID string } `json:"nodes"`
				Total int                   `json:"total"`
				Stale bool                  `json:"stale"`
			}
			require.NoError(t, json.Unmarshal([]byte(out), &result), out)
			require.True(t, result.Stale)
			require.Equal(t, 1, result.Total)
			require.Len(t, result.Nodes, 1)
			require.Equal(t, "TEST-1", result.Nodes[0].ID)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runHookStatusReader(t, tc.wantStale, tc.args...)
			require.NoError(t, err, out)
			tc.check(t, out)
		})
	}
	t.Run("stdout write fails", func(t *testing.T) {
		saveAndResetApp(t)
		closed, err := os.CreateTemp(t.TempDir(), "stdout")
		require.NoError(t, err)
		require.NoError(t, closed.Close())
		stdout := os.Stdout
		defer func() { os.Stdout = stdout }()
		os.Stdout = closed
		require.ErrorIs(t, runHooksTrustStatus(approvals), os.ErrClosed)
	})

	t.Run("same-name env-pass refusal", func(t *testing.T) {
		const refusedStanza = `  - name: alpha
    match: {events: [comment.addressed]}
    deliver: [exec]
    exec: {command: [sh, wake.sh], env-pass: ['not-an-env-name']}
`
		require.NoError(t, os.WriteFile(configPath, []byte(config+refusedStanza), 0600))
		t.Cleanup(func() { require.NoError(t, os.WriteFile(configPath, []byte(config), 0600)) })
		err := approvals.Approve("alpha", true)
		require.ErrorContains(t, err, `hook "alpha" cannot be approved: exec.env-pass`)
		assertNoTrustStatusOutput(t, err.Error())
	})

	t.Run("one approval fails", func(t *testing.T) {
		key := hookApprovalKey(t, state, "zulu")
		require.NoError(t, state.WriteJSON(key, map[string]string{}))
		decisions, err := approvals.Check()
		require.ErrorContains(t, err, `hook "zulu" approval record is invalid`)
		require.True(t, decisions["alpha"].Trusted, "fixture must return a trusted decision alongside the error")
		require.False(t, decisions["zulu"].Trusted)
		assertNoTrustStatusOutput(t, err.Error())
	})

	require.NoError(t, os.Remove(configPath))
	t.Run("missing config is an empty list", func(t *testing.T) {
		out, err := runHookStatusReader(t, true, "hooks", "trust", "--status")
		require.NoError(t, err)
		require.Equal(t, "(no trusted hooks)\n", out)
		out, err = runHookStatusReader(t, true, "hooks", "trust", "--status", "--json")
		require.NoError(t, err)
		var result struct {
			Data []hooks.Decision `json:"data"`
		}
		require.NoError(t, json.Unmarshal([]byte(out), &result), out)
		require.Equal(t, []hooks.Decision{}, result.Data)
	})
	t.Run("malformed config", func(t *testing.T) {
		require.NoError(t, os.WriteFile(configPath, []byte("hooks: ["), 0600))
		t.Cleanup(func() { require.NoError(t, os.Remove(configPath)) })
		assertNoTrustStatusOutput(t, "parse hooks.yaml")
	})
	t.Run("unreadable config", func(t *testing.T) {
		// A directory at the config path fails ReadFile even as root.
		require.NoError(t, os.Mkdir(configPath, 0700))
		assertNoTrustStatusOutput(t, "hooks.yaml")
	})
}

func doctorCheckIDs(t *testing.T, out string) []string {
	t.Helper()
	var rows []service.DoctorResult
	require.NoError(t, json.Unmarshal([]byte(out), &rows), out)
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	require.Contains(t, ids, hooksProgramsCheckID)
	return ids
}

func hookApprovalKey(t *testing.T, state *oplocal.State, name string) string {
	t.Helper()
	dir, err := state.Path()
	require.NoError(t, err)
	paths, err := filepath.Glob(filepath.Join(dir, "hook-*.json"))
	require.NoError(t, err)
	for _, path := range paths {
		key := strings.TrimSuffix(filepath.Base(path), ".json")
		var record struct{ Hook string }
		reports, err := state.ReadJSON(key, &record)
		require.NoError(t, err)
		require.Empty(t, reports)
		if record.Hook == name {
			return key
		}
	}
	t.Fatalf("no approval record for %q", name)
	return ""
}

func assertNoTrustStatusOutput(t *testing.T, wantError string) {
	t.Helper()
	for _, mode := range []string{"text", "json"} {
		t.Run(mode, func(t *testing.T) {
			args := []string{"hooks", "trust", "--status"}
			if mode == "json" {
				args = append(args, "--json")
			}
			out, err := runHookStatusReader(t, true, args...)
			require.ErrorContains(t, err, wantError)
			require.NotZero(t, exitCodeForError(err))
			require.Empty(t, out)
		})
	}
}

func runHookStatusReader(t *testing.T, wantStale bool, args ...string) (string, error) {
	t.Helper()
	saveAndResetApp(t)
	var out string
	var err error
	stderr := captureStderr(t, func() {
		out = captureStdout(t, func() { err = runArgs(args) })
	})
	require.NotContains(t, stderr, "auto-import failed")
	if wantStale {
		require.Contains(t, stderr, "possibly stale data: sync lock busy", "command error: %v", err)
	}
	return out, err
}

func operatorTestHome(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	dir, err := os.MkdirTemp(home, ".mtix-status-test-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(dir)) })
	return dir
}

func operatorTestEnv(root string) oplocal.Env {
	env := oplocal.Env{GOOS: runtime.GOOS, Home: root, Values: map[string]string{}}
	if runtime.GOOS == "windows" {
		env.Values["APPDATA"] = filepath.Join(root, "config")
	}
	return env
}
