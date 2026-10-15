// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/stretchr/testify/require"
)

func TestHooksStatus_ReportsAnAppendFilePathThatWouldBeRefused(t *testing.T) {
	saveAndResetApp(t)
	root, err := filepath.EvalSymlinks(operatorTestHome(t))
	require.NoError(t, err)
	state := oplocal.New(operatorTestEnv(root))
	require.NoError(t, state.Ensure())
	project := t.TempDir()
	mtixDir := filepath.Join(project, ".mtix")
	require.NoError(t, os.Mkdir(mtixDir, 0o700))
	require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(project, "out")))
	yaml := "hooks:\n  - name: audit\n    match: {events: [node.created]}\n    deliver: [append-file]\n    append-file: {path: out/events.log}\n  - name: fine\n    match: {events: [node.created]}\n    deliver: [append-file]\n    append-file: {path: events.log}\n"
	require.NoError(t, os.WriteFile(filepath.Join(mtixDir, "hooks.yaml"), []byte(yaml), 0o600))
	app.mtixDir = mtixDir
	app.jsonOutput = true
	text := captureStdout(t, func() { require.NoError(t, runHooksStatus(state)) })
	var report struct {
		AppendFile []struct {
			Hook, Path, Reason, Fix string
			Refused                 bool
		} `json:"append_file"`
	}
	require.NoError(t, json.Unmarshal([]byte(text), &report))
	require.Len(t, report.AppendFile, 1, "only the refused hook is listed")
	require.Equal(t, "audit", report.AppendFile[0].Hook)
	require.True(t, report.AppendFile[0].Refused)
	require.NotEmpty(t, report.AppendFile[0].Reason)
	require.Contains(t, report.AppendFile[0].Fix, "remove the link")
	app.jsonOutput = false
	text = captureStdout(t, func() { require.NoError(t, runHooksStatus(state)) })
	require.Contains(t, text, `hook "audit" append-file:`)
	require.Contains(t, text, "Fix: remove the link")
}
