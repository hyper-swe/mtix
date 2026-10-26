// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// MTIX-112.17.22: verify the real CI workflow's complete matrix, unchanged
// invocation and aggregate failure behavior, including skipped dependencies.
package scripts_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

type ciGoStep struct {
	Run             string
	Uses            string
	If              string
	With            map[string]any
	Env             map[string]string
	ContinueOnError any `yaml:"continue-on-error"`
}

type ciGoJob struct {
	Name            string
	Needs           []string
	If              string
	RunsOn          string `yaml:"runs-on"`
	ContinueOnError any    `yaml:"continue-on-error"`
	Strategy        struct {
		FailFast *bool `yaml:"fail-fast"`
		Matrix   struct {
			OS      []string
			Shard   []string
			PG      []string
			Pkg     []string
			Include []map[string]any
			Exclude []map[string]any
		}
	}
	Steps []ciGoStep
}

func readCIJobs(t *testing.T) map[string]ciGoJob {
	t.Helper()
	source, err := os.ReadFile("../.github/workflows/ci.yml")
	require.NoError(t, err)
	var workflow struct{ Jobs map[string]ciGoJob }
	require.NoError(t, yaml.Unmarshal(source, &workflow))
	return workflow.Jobs
}

func TestGoWorkflow_Shards_CoverBothOSesWithoutOptionalCases(t *testing.T) {
	jobs := readCIJobs(t)
	require.Contains(t, jobs, "test-go-shards")
	job := jobs["test-go-shards"]
	require.Equal(t, "test-go-shard (${{ matrix.os }}, ${{ matrix.shard }})", job.Name)
	require.Equal(t, "${{ matrix.os }}", job.RunsOn)
	require.Equal(t, []string{"ubuntu-22.04", "macos-latest"}, job.Strategy.Matrix.OS)
	require.Equal(t, []string{"sqlite", "cli", "other"}, job.Strategy.Matrix.Shard)
	require.Empty(t, job.Strategy.Matrix.Include)
	require.Empty(t, job.Strategy.Matrix.Exclude)
	require.NotNil(t, job.Strategy.FailFast)
	require.False(t, *job.Strategy.FailFast)
	require.Empty(t, job.If)
	require.Nil(t, job.ContinueOnError)
	var guard, race, fullHistory bool
	for _, step := range job.Steps {
		require.Empty(t, step.If)
		require.Nil(t, step.ContinueOnError)
		guard = guard || strings.Contains(step.Run, "bash scripts/ci-go-shards.sh check")
		race = race || strings.Contains(step.Run, `go test "${packages[@]}" -race -count=1 -timeout 20m`)
		if strings.Contains(step.Run, "go test") {
			require.Contains(t, step.Run, `bash scripts/ci-go-shards.sh list "$SHARD"`)
			require.Equal(t, "${{ matrix.shard }}", step.Env["SHARD"])
			require.NotContains(t, step.Run, "-skip")
		}
		if strings.HasPrefix(step.Uses, "actions/checkout@") {
			fullHistory = step.With["fetch-depth"] == 0
		}
	}
	require.True(t, guard)
	require.True(t, race)
	require.True(t, fullHistory)
	require.Contains(t, jobs["build"].Needs, "test-go")
}

func TestGoWorkflow_Aggregate_RejectsEveryNonSuccessResult(t *testing.T) {
	job := readCIJobs(t)["test-go"]
	require.Equal(t, "test-go", job.Name)
	require.Equal(t, []string{"test-go-shards"}, job.Needs)
	require.Equal(t, "${{ always() }}", job.If)
	require.Nil(t, job.ContinueOnError)
	require.Len(t, job.Steps, 1)
	step := job.Steps[0]
	require.Empty(t, step.If)
	require.Nil(t, step.ContinueOnError)
	require.Equal(t, "${{ needs.test-go-shards.result }}", step.Env["SHARD_RESULT"])
	for _, result := range []string{"success", "failure", "cancelled", "skipped", "", "unknown"} {
		t.Run("result="+result, func(t *testing.T) {
			cmd := exec.Command("bash", "-e", "-o", "pipefail", "-c", step.Run)
			cmd.Env = append(os.Environ(), "SHARD_RESULT="+result)
			out, err := cmd.CombinedOutput()
			if result == "success" {
				require.NoError(t, err, string(out))
			} else {
				require.Error(t, err, string(out))
			}
		})
	}
}

func TestGoWorkflow_CheckCount_MatchesTwentyOneChecks(t *testing.T) {
	count := 0
	for _, job := range readCIJobs(t) {
		combinations := 1
		m := job.Strategy.Matrix
		for _, axis := range [][]string{m.OS, m.Shard, m.PG, m.Pkg} {
			if len(axis) > 0 {
				combinations *= len(axis)
			}
		}
		count += combinations
	}
	require.Equal(t, 21, count)
}

// MTIX-112.17.24: cmd/mtix runs as three shards per PostgreSQL version.
func TestGoWorkflow_PostgresCmdShards_RunEveryShardOnBothVersionsWithUnchangedFlags(t *testing.T) {
	jobs := readCIJobs(t)
	job, ok := jobs["test-go-postgres-cmd"]
	require.True(t, ok)
	require.Equal(t, "pg-${{ matrix.pg }} cmd/mtix shard ${{ matrix.shard }}", job.Name)
	require.Equal(t, []string{"16", "17"}, job.Strategy.Matrix.PG)
	require.Equal(t, []string{"1", "2", "3"}, job.Strategy.Matrix.Shard)
	require.Empty(t, job.Strategy.Matrix.Include)
	require.Empty(t, job.Strategy.Matrix.Exclude)
	require.False(t, *job.Strategy.FailFast)
	require.Empty(t, job.If)
	require.Nil(t, job.ContinueOnError)
	require.Contains(t, jobs["build"].Needs, "test-go-postgres-cmd")
	var guard, run bool
	for _, step := range job.Steps {
		require.Empty(t, step.If)
		require.Nil(t, step.ContinueOnError)
		guard = guard || step.Run == "bash scripts/cmd-mtix-shards.sh check"
		if strings.Contains(step.Run, "go test") {
			run = true
			require.Equal(t, "${{ matrix.shard }}", step.Env["SHARD"])
			require.Contains(t, step.Run, `bash scripts/cmd-mtix-shards.sh pattern "$SHARD"`)
			require.Contains(t, step.Run, `-race -count=1 -p 1 -timeout 20m -run "$pattern"`)
			require.NotContains(t, step.Run, "-skip")
		}
	}
	require.True(t, guard)
	require.True(t, run)
	// The unsharded jobs must no longer run cmd/mtix, or it would run twice.
	require.NotContains(t, jobs["test-go-postgres-docker"].Strategy.Matrix.Pkg, "./cmd/mtix/")
}
