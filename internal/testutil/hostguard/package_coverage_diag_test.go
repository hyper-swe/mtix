// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hostguard_test

import (
	"os/exec"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRunGoList_FailureNamesStderrAndEnvironment pins MTIX-283: a failing
// go list used to surface as a bare "exit status 1", hiding the box
// environment (GOFLAGS, GOPROXY, GOMODCACHE) that makes the walk fail.
func TestRunGoList_FailureNamesStderrAndEnvironment(t *testing.T) {
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go tool not on PATH")
	}
	_, err = runGoList(goTool, t.TempDir())
	require.Error(t, err)
	require.Contains(t, err.Error(), "stderr: pattern", "go list stderr must be in the error")
	for _, name := range []string{"GOFLAGS=", "GOPROXY=", "GOMODCACHE=", "GOTOOLCHAIN="} {
		require.Contains(t, err.Error(), name, "environment assumption must be named")
	}
}

// TestReachesOplocal_FollowsTransitiveImportsAndTestVariants covers the walk
// that decides which test binaries need the guard.
func TestReachesOplocal_FollowsTransitiveImportsAndTestVariants(t *testing.T) {
	pkgs := map[string]listedPackage{
		"m/a.test":                      {imports: []string{"m/a [m/a.test]"}},
		"m/a [m/a.test]":                {imports: []string{"m/b"}},
		"m/b":                           {imports: []string{"m/internal/oplocal"}},
		"m/internal/oplocal":            {},
		"m/c.test":                      {imports: []string{"m/c [m/c.test]"}},
		"m/c [m/c.test]":                {imports: []string{"m/d"}},
		"m/d":                           {},
		"m/e.test":                      {imports: []string{"m/internal/oplocal [m/e.test]"}},
		"m/internal/oplocal [m/e.test]": {},
	}
	require.True(t, reachesOplocal(pkgs, "m/a.test"))
	require.False(t, reachesOplocal(pkgs, "m/c.test"))
	require.True(t, reachesOplocal(pkgs, "m/e.test"))
}

// TestCheckedCountMessage_NamesTheEnvironment: a zero count is an environment
// problem (tags, GOFLAGS, repo path), and the failure text must say so.
func TestCheckedCountMessage_NamesTheEnvironment(t *testing.T) {
	msg := zeroCheckedMessage("/repo")
	for _, want := range []string{"/repo", "GOFLAGS", "-tags", "go list"} {
		require.Contains(t, msg, want)
	}
}
