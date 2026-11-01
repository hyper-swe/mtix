// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
)

func serveRecordIgnoreRow(t *testing.T) service.DoctorResult {
	t.Helper()
	svc, err := newConfiguredDoctorService()
	require.NoError(t, err)
	rows, err := svc.Run(context.Background(), service.DoctorRunOptions{})
	require.NoError(t, err)
	for _, row := range rows {
		if row.ID == serveRecordIgnoreCheckID {
			return row
		}
	}
	t.Fatal("serve record ignore row missing")
	return service.DoctorResult{}
}

func gitInit(t *testing.T, dir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput()
	require.NoError(t, err, string(out))
}

func TestDoctor_ServeRecordIgnore_GitRepoNotIgnored_WarnsWithExactFix(t *testing.T) {
	reviewProject(t)
	root := filepath.Dir(app.mtixDir)
	gitInit(t, root)

	row := serveRecordIgnoreRow(t)
	require.Equal(t, service.DoctorWarn, row.Status)
	require.Contains(t, row.Detail, ".mtix/serve.json")
	require.Equal(t, "echo '.mtix/serve.json' >> .gitignore", row.Fix)
	raw, err := json.Marshal(row)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"fix":"echo '.mtix/serve.json' \u003e\u003e .gitignore"`)
}

func TestDoctor_ServeRecordIgnore_Ignored_Passes(t *testing.T) {
	reviewProject(t)
	root := filepath.Dir(app.mtixDir)
	gitInit(t, root)
	require.NoError(t, os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".mtix/serve.json\n"), 0o600))
	require.Equal(t, service.DoctorPass, serveRecordIgnoreRow(t).Status)
}

func TestDoctor_ServeRecordIgnore_NotAGitRepo_NoRow(t *testing.T) {
	reviewProject(t)
	svc, err := newConfiguredDoctorService()
	require.NoError(t, err)
	rows, err := svc.Run(context.Background(), service.DoctorRunOptions{})
	require.NoError(t, err)
	for _, row := range rows {
		require.NotEqual(t, serveRecordIgnoreCheckID, row.ID)
	}
}
