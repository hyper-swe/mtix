// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package scripts_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGoWorkflow_RelayQuietGateIsRequiredAfterGoSetup(t *testing.T) {
	jobs := readCIJobs(t)
	job := jobs["build"]
	require.Equal(t, "ubuntu-22.04", job.RunsOn)
	require.Empty(t, job.If)
	require.Nil(t, job.ContinueOnError)
	require.Contains(t, job.Needs, "test-go")
	setup, found := false, 0
	for _, step := range job.Steps {
		if strings.HasPrefix(step.Uses, "actions/setup-go@") {
			setup = true
		}
		if !strings.Contains(step.Run, "relay-quiet") {
			continue
		}
		found++
		require.True(t, setup, "quiet check needs the declared Go toolchain")
		require.Equal(t, "go test ./benchmarks -run '^TestRelayPublish_MeetsTheLatencyTarget$' -bench '^BenchmarkRelay(Publish|Ingest)$' -benchtime=200x -count=1 -timeout=5m -v -args -relay-quiet", step.Run)
		require.Empty(t, step.If)
		require.Empty(t, step.Env)
		require.Nil(t, step.ContinueOnError)
	}
	require.Equal(t, 1, found, "build must run exactly one blocking quiet relay check")
}
