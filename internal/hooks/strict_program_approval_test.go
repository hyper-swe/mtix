// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStrict_On_KnownInterpreterWithoutAPinnedScriptNeedsApproval(t *testing.T) {
	outside := t.TempDir()
	script := filepath.Join(outside, "x.sh")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\n"), 0o700)) //nolint:gosec // test script
	for _, command := range []string{
		"[sh, " + script + "]",
		"[env, sh, " + script + "]",
		"[sh, -x, " + script + "]",
		"[sh]",
		"[env, sh]",
	} {
		t.Run(command, func(t *testing.T) {
			f := strictFixture(t, command)
			_, err := f.approvals.PrepareExec(f.hook(t))
			require.Error(t, err, "an interpreter running an unpinned or missing script needs approval")
			require.Contains(t, err.Error(), "strict program mode")
		})
	}
}

func TestStrict_ProgramRecordForAnotherHookIsRefused(t *testing.T) {
	f := strictFixture(t, "[cat, data/x]")
	_, err := f.approvals.ApproveProgram(f.hook(t), true)
	require.NoError(t, err)
	files := recordFilesContaining(t, f, `"program"`)
	require.Len(t, files, 1)
	data, err := os.ReadFile(files[0]) //nolint:gosec // test reads its own state dir
	require.NoError(t, err)
	var rec struct {
		HostID string          `json:"host_id"`
		Data   json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(data, &rec))
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Data, &body))
	require.Contains(t, body, "hook")
	body["hook"] = "another-hook"
	rec.Data, err = json.Marshal(body)
	require.NoError(t, err)
	out, err := json.Marshal(rec)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(files[0], out, 0o600))
	_, err = f.approvals.PrepareExec(f.hook(t))
	require.Error(t, err, "a record whose hook field differs is not this hook's approval")
}
