// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// runCLI runs the command and returns its exit code, stdout and stderr.
func runCLI(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = run(args, &out, &errb)
	return code, out.String(), errb.String()
}

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
	return p
}

func TestRun_Report_PrintsTableFromStampFiles(t *testing.T) {
	dir := t.TempDir()
	one := writeFile(t, dir, "one.log", "100 send B/idle 1\n101 turn B/idle 1\n")
	two := writeFile(t, dir, "two.log", "200 send B/idle 2\n202 turn B/idle 2\n")
	code, out, errs := runCLI(t, "report", one, two)
	require.Zero(t, code, errs)
	require.Contains(t, out, "| B/idle |")
	require.Contains(t, out, "| 2 | 2 | 0 | 0 | 0 |")
	require.Contains(t, out, "NOT RUN")
}

func TestRun_Report_FailureCases_ExitNonZeroWithMessage(t *testing.T) {
	dir := t.TempDir()
	bad := writeFile(t, dir, "bad.log", "nonsense\n")
	unknown := writeFile(t, dir, "unknown.log", "1 send Z/idle 1\n")
	tests := []struct {
		name string
		args []string
		code int
		want string
	}{
		{"no files", []string{"report"}, 2, "usage"},
		{"missing file", []string{"report", filepath.Join(dir, "absent.log")}, 1, "absent.log"},
		{"malformed stamp", []string{"report", bad}, 1, "line 1"},
		{"unknown cell", []string{"report", unknown}, 1, "Z/idle"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, _, errs := runCLI(t, tt.args...)
			require.Equal(t, tt.code, code)
			require.Contains(t, errs, tt.want)
		})
	}
}

func TestRun_Registry_PassAndContractFailureExitCodes(t *testing.T) {
	code, out, errs := runCLI(t, "registry", "-pid", "4242", filepath.Join("testdata", "registry-pass.jsonl"))
	require.Zero(t, code, errs)
	require.Contains(t, out, "ok")
	code, _, errs = runCLI(t, "registry", "-pid", "4242", filepath.Join("testdata", "registry-missing-key.jsonl"))
	require.Equal(t, 1, code)
	require.Contains(t, errs, "messagingSocketPath")
	code, _, errs = runCLI(t, "registry", filepath.Join("testdata", "registry-pass.jsonl"))
	require.Equal(t, 2, code, "the child pid flag is mandatory")
	require.Contains(t, errs, "usage")
}

func TestRun_Launch_AcceptsScratchPlanAndRefusesBypass(t *testing.T) {
	root, outside := scratch(t)
	settings := filepath.Join(root, "cfg", "s.json")
	code, out, errs := runCLI(t, "launch", "-root", root, "-cwd", filepath.Join(root, "proj"), "--", "--model", "haiku", "--settings", settings)
	require.Zero(t, code, errs)
	require.Contains(t, out, "ok")
	code, _, errs = runCLI(t, "launch", "-root", root, "-cwd", filepath.Join(root, "proj"), "--", "--permission-mode", "bypassPermissions", "--settings", settings)
	require.Equal(t, 1, code)
	require.Contains(t, errs, "permission")
	code, _, errs = runCLI(t, "launch", "-root", root, "-cwd", outside, "--", "--settings", settings)
	require.Equal(t, 1, code)
	require.Contains(t, errs, "cwd")
}

func TestRun_UnknownOrMissingCommand_ExitsWithUsage(t *testing.T) {
	for _, args := range [][]string{{}, {"frobnicate"}} {
		code, _, errs := runCLI(t, args...)
		require.Equal(t, 2, code)
		require.Contains(t, strings.ToLower(errs), "usage")
	}
}

func TestShippedBuilds_ExcludeTheListenerSpikeTool(t *testing.T) {
	releaser, err := os.ReadFile(filepath.Join("..", "..", ".goreleaser.yaml"))
	require.NoError(t, err)
	require.Contains(t, string(releaser), "main: ./cmd/mtix/", "releases must build the mtix command only")
	require.NotContains(t, string(releaser), "listener-spike")
	makefile, err := os.ReadFile(filepath.Join("..", "..", "Makefile"))
	require.NoError(t, err)
	require.NotContains(t, string(makefile), "listener-spike", "no make target may build or ship the spike tool")
}
