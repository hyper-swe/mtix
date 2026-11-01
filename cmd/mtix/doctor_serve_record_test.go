// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	require.Equal(t, `printf '\n.mtix/serve.json\n' >> .gitignore`, row.Fix)
	raw, err := json.Marshal(row)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"fix":"printf '\\n.mtix/serve.json\\n' \u003e\u003e .gitignore"`)
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

func TestDoctor_ServeRecordIgnore_FixOnGitignoreWithoutFinalNewline_KeepsLastLine(t *testing.T) {
	reviewProject(t)
	root := filepath.Dir(app.mtixDir)
	gitInit(t, root)
	ignore := filepath.Join(root, ".gitignore")
	require.NoError(t, os.WriteFile(ignore, []byte("build/\n.env"), 0o600)) // no final newline

	row := serveRecordIgnoreRow(t)
	require.Equal(t, service.DoctorWarn, row.Status)
	cmd := exec.Command("sh", "-c", row.Fix)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))

	raw, err := os.ReadFile(ignore)
	require.NoError(t, err)
	lines := strings.Split(string(raw), "\n")
	require.Contains(t, lines, ".env", "the last line must not be joined")
	require.Contains(t, lines, ".mtix/serve.json")
	require.Equal(t, service.DoctorPass, serveRecordIgnoreRow(t).Status, "the printed fix really ignores the file")
}

func TestDoctor_ServeRecordIgnore_GitCannotDecide_IsInfoWithReason(t *testing.T) {
	reviewProject(t)
	root := filepath.Dir(app.mtixDir)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	// A .git directory that is not a repository: git exits 128.
	require.NoError(t, os.Mkdir(filepath.Join(root, ".git"), 0o700))

	row := serveRecordIgnoreRow(t)
	require.Equal(t, service.DoctorInfo, row.Status)
	require.Contains(t, row.Detail, "git could not check")
	require.Contains(t, row.Detail, "exit 128")
	require.NotContains(t, row.Detail, "not a git repository; nothing to ignore")
}
