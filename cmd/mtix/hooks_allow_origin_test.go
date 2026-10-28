// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/hooks"
	"github.com/hyper-swe/mtix/internal/oplocal"
)

func originTestState(t *testing.T) *oplocal.State {
	t.Helper()
	root, err := filepath.EvalSymlinks(operatorTestHome(t))
	require.NoError(t, err)
	return oplocal.New(operatorTestEnv(root))
}

func TestHooksAllowOrigin_Registered_TakesOneURLAndPrivateFlag(t *testing.T) {
	cmd := newHooksCmd()
	allow, _, err := cmd.Find([]string{"allow-origin"})
	require.NoError(t, err)
	require.Equal(t, "allow-origin", allow.Name())
	require.NoError(t, allow.Args(allow, []string{"https://hooks.example/x"}))
	require.Error(t, allow.Args(allow, nil))
	require.Error(t, allow.Args(allow, []string{"a", "b"}))
	require.NotNil(t, allow.Flags().Lookup("allow-private"))
}

func TestHooksAllowOrigin_Gates_NoTerminalOrSessionWritesNothing(t *testing.T) {
	saveAndResetApp(t)
	state := originTestState(t)
	require.Error(t, runHooksAllowOrigin(state, "https://hooks.example", false, false, false))
	require.Error(t, runHooksAllowOrigin(state, "https://hooks.example", false, true, true))
	list, err := hooks.ReadOrigins(state)
	require.NoError(t, err)
	require.Empty(t, list.Origins)
}

func TestHooksAllowOrigin_Terminal_ReportsAndStores(t *testing.T) {
	saveAndResetApp(t)
	state := originTestState(t)
	text := captureStdout(t, func() {
		require.NoError(t, runHooksAllowOrigin(state, "https://Hooks.Example/x", false, true, false))
	})
	require.Contains(t, text, "https://hooks.example:443")
	app.jsonOutput = true
	text = captureStdout(t, func() {
		require.NoError(t, runHooksAllowOrigin(state, "http://127.0.0.1:9000/x", true, true, false))
	})
	var got hooks.Origin
	require.NoError(t, json.Unmarshal([]byte(text), &got))
	require.True(t, got.AllowPrivate)
	list, err := hooks.ReadOrigins(state)
	require.NoError(t, err)
	require.Len(t, list.Origins, 2)
}

func TestHooksStatus_ReportsUnconsentedAndPrivateWebhookOrigins(t *testing.T) {
	saveAndResetApp(t)
	state := originTestState(t)
	require.NoError(t, state.Ensure())
	_, err := hooks.AllowOrigin(state, "https://93.184.216.34", false, true, false)
	require.NoError(t, err)
	_, err = hooks.AllowOrigin(state, "http://127.0.0.1:7", false, true, false)
	require.NoError(t, err)
	project := t.TempDir()
	mtixDir := filepath.Join(project, ".mtix")
	require.NoError(t, os.Mkdir(mtixDir, 0o700))
	yaml := "hooks:\n" +
		"  - {name: ok, match: {events: [node.created]}, deliver: [webhook], webhook: {url: 'https://93.184.216.34/h'}}\n" +
		"  - {name: fresh, match: {events: [node.created]}, deliver: [webhook], webhook: {url: 'https://fresh.example/h'}}\n" +
		"  - {name: loud, match: {events: [node.created]}, deliver: [webhook], webhook: {url: 'http://127.0.0.1:7/h'}}\n" +
		"  - {name: local, match: {events: [node.created]}, deliver: [webhook], webhook: {url: 'http://127.0.0.1:9/h'}}\n"
	require.NoError(t, os.WriteFile(filepath.Join(mtixDir, "hooks.yaml"), []byte(yaml), 0o600))
	app.mtixDir = mtixDir

	app.jsonOutput = true
	text := captureStdout(t, func() { require.NoError(t, runHooksStatus(state)) })
	var report struct {
		Origins []hooks.OriginIssue `json:"origins"`
	}
	require.NoError(t, json.Unmarshal([]byte(text), &report))
	require.Len(t, report.Origins, 3)
	require.Equal(t, "fresh", report.Origins[0].Hook)
	require.Contains(t, report.Origins[0].Fix, "mtix hooks allow-origin 'https://fresh.example:443'")
	require.Equal(t, "loud", report.Origins[1].Hook, "consented loopback without the private opt-in")
	require.Contains(t, report.Origins[1].Reason, "not explicitly allowed")
	require.Equal(t, "mtix hooks allow-origin --allow-private 'http://127.0.0.1:7'", report.Origins[1].Fix)
	require.Equal(t, "local", report.Origins[2].Hook)
	require.Contains(t, report.Origins[2].Fix, "--allow-private")

	app.jsonOutput = false
	text = captureStdout(t, func() { require.NoError(t, runHooksStatus(state)) })
	require.Contains(t, text, `hook "fresh" webhook:`)
	require.Contains(t, text, "Fix: mtix hooks allow-origin")
	require.Contains(t, text, `hook "loud" webhook: private, loopback or link-local address not explicitly allowed`)
	require.Contains(t, text, "Fix: mtix hooks allow-origin --allow-private 'http://127.0.0.1:7'")
	require.NotContains(t, text, `hook "ok" webhook`)
}

func TestHooksStatus_NoWebhooks_OriginsEmptyArray(t *testing.T) {
	saveAndResetApp(t)
	state := originTestState(t)
	require.NoError(t, state.Ensure())
	app.mtixDir = filepath.Join(t.TempDir(), ".mtix")
	app.jsonOutput = true
	text := captureStdout(t, func() { require.NoError(t, runHooksStatus(state)) })
	require.Contains(t, text, `"origins": []`)
}
