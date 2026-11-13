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
	Name            string
	Shell           string
	Run             string
	Uses            string
	If              string
	With            map[string]any
	Env             map[string]string
	ContinueOnError any `yaml:"continue-on-error"`
}

type ciGoJob struct {
	Env             map[string]string
	Defaults        map[string]any
	Name            string
	Needs           []string
	If              string
	RunsOn          string `yaml:"runs-on"`
	ContinueOnError any    `yaml:"continue-on-error"`
	Strategy        struct {
		FailFast *bool `yaml:"fail-fast"`
		Matrix   struct {
			OS      ciOSAxis
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
	var workflow struct {
		Jobs     map[string]ciGoJob
		Env      map[string]string
		Defaults map[string]any
	}
	require.NoError(t, yaml.Unmarshal(source, &workflow))
	require.Empty(t, workflow.Env, "workflow env may alter race test selection")
	require.Empty(t, workflow.Defaults, "workflow defaults may override fail-fast shells")
	return workflow.Jobs
}

func requireRaceShardEnvironment(t *testing.T, job ciGoJob) {
	t.Helper()
	require.Empty(t, job.Env, "job env may alter test selection")
	require.Empty(t, job.Defaults, "job defaults may override fail-fast shells")
	for _, step := range job.Steps {
		require.Empty(t, step.Shell, "retain the runner fail-fast shell")
		if strings.Contains(step.Run, "go test") {
			require.Equal(t, map[string]string{"SHARD": "${{ matrix.shard }}"}, step.Env)
		} else {
			require.Empty(t, step.Env)
		}
	}
}

func TestGoWorkflow_Shards_CoverBothOSesWithoutOptionalCases(t *testing.T) {
	jobs := readCIJobs(t)
	require.Contains(t, jobs, "test-go-shards")
	job := jobs["test-go-shards"]
	requireRaceShardEnvironment(t, job)
	require.Equal(t, "test-go-shard (${{ matrix.os }}, ${{ matrix.shard }})", job.Name)
	require.Equal(t, "${{ matrix.os }}", job.RunsOn)
	require.Equal(t, []string{"ubuntu-22.04", "macos-latest"}, job.Strategy.Matrix.OS.For("push"))
	// MTIX-112.17.22.1: cmd/mtix moved to its own test-go-cmd matrix.
	require.Equal(t, []string{"other"}, job.Strategy.Matrix.Shard)
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
	require.Equal(t, []string{"test-go-shards", "test-go-sqlite", "test-go-cmd", "test-go-service"}, job.Needs)
	require.Equal(t, "${{ always() }}", job.If)
	require.Nil(t, job.ContinueOnError)
	// MTIX-112.17.42: the aggregate step is still the one that judges the
	// four needs; the pull_request-only health step is pinned in
	// ci_pr_macos_capacity_test.go.
	var step ciGoStep
	for _, candidate := range job.Steps {
		if candidate.Name == "Require every Go shard to succeed" {
			step = candidate
		}
	}
	require.Equal(t, "Require every Go shard to succeed", step.Name)
	require.Equal(t, "${{ needs.test-go-service.result }}", step.Env["SERVICE_RESULT"])
	require.Empty(t, step.If)
	require.Nil(t, step.ContinueOnError)
	for env, need := range map[string]string{"SHARD_RESULT": "test-go-shards", "SQLITE_RESULT": "test-go-sqlite", "CMD_RESULT": "test-go-cmd", "SERVICE_RESULT": "test-go-service"} {
		require.Equal(t, "${{ needs."+need+".result }}", step.Env[env])
	}
	results := []string{"success", "failure", "cancelled", "skipped", "", "unknown"}
	keys := []string{"SHARD_RESULT", "SQLITE_RESULT", "CMD_RESULT", "SERVICE_RESULT"}
	// Every Cartesian combination is checked; no subtests add overhead to 1296 shells.
	for combination := range 1296 {
		encoded := combination
		env := os.Environ()
		values := make([]string, len(keys))
		for i, key := range keys {
			values[i] = results[encoded%len(results)]
			encoded /= len(results)
			env = append(env, key+"="+values[i])
		}
		cmd := exec.Command("bash", "-e", "-o", "pipefail", "-c", step.Run)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if combination == 0 {
			require.NoError(t, err, "results=%v output=%s", values, out)
		} else {
			require.Error(t, err, "results=%v output=%s", values, out)
		}
	}
}

func TestGoWorkflow_CheckCount_MatchesThirtyFiveChecks(t *testing.T) {
	// MTIX-112.17.22.1: 25 - 2 (cli shard) + 6 (test-go-cmd) = 29.
	// MTIX-112.51: plus two service shards on each OS = 33.
	// MTIX-270: a fourth SQLite shard on each OS = 35.
	// MTIX-112.17.42: that is the push and schedule count; pull_request runs
	// omit the nine macOS SQLite, cmd/mtix and service legs (see
	// ci_pr_macos_capacity_test.go).
	require.Equal(t, 35, countCIChecks(t, "push"))
	require.Equal(t, 35, countCIChecks(t, "schedule"))
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

// MTIX-112.17.22.1: cmd/mtix runs as three hash shards on each OS, with the
// same script, shard count and flags as the PostgreSQL and remote-bar shards.
func TestGoWorkflow_CmdShards_RunThreeHashShardsOnBothOSesWithUnchangedFlags(t *testing.T) {
	jobs := readCIJobs(t)
	job, ok := jobs["test-go-cmd"]
	require.True(t, ok)
	requireRaceShardEnvironment(t, job)
	require.Equal(t, "test-go-cmd (${{ matrix.os }}, shard ${{ matrix.shard }})", job.Name)
	require.Equal(t, "${{ matrix.os }}", job.RunsOn)
	require.Equal(t, []string{"ubuntu-22.04", "macos-latest"}, job.Strategy.Matrix.OS.For("push"))
	require.Equal(t, []string{"1", "2", "3"}, job.Strategy.Matrix.Shard)
	require.Empty(t, job.Strategy.Matrix.Include)
	require.Empty(t, job.Strategy.Matrix.Exclude)
	require.False(t, *job.Strategy.FailFast)
	require.Empty(t, job.If)
	require.Nil(t, job.ContinueOnError)
	require.Contains(t, jobs["build"].Needs, "test-go")
	var guard, pkgGuard, run, fullHistory bool
	for _, step := range job.Steps {
		require.Empty(t, step.If)
		require.Nil(t, step.ContinueOnError)
		guard = guard || step.Run == "bash scripts/cmd-mtix-shards.sh check"
		pkgGuard = pkgGuard || step.Run == "bash scripts/ci-go-shards.sh check"
		if strings.Contains(step.Run, "go test") {
			run = true
			require.Equal(t, "${{ matrix.shard }}", step.Env["SHARD"])
			require.Contains(t, step.Run, `bash scripts/cmd-mtix-shards.sh pattern "$SHARD"`)
			require.Contains(t, step.Run, `go test ./cmd/mtix/ -race -count=1 -timeout 20m -run "$pattern"`)
			require.NotContains(t, step.Run, "-skip")
		}
		if strings.HasPrefix(step.Uses, "actions/checkout@") {
			fullHistory = step.With["fetch-depth"] == 0
		}
	}
	require.True(t, guard)
	require.True(t, pkgGuard)
	require.True(t, run)
	require.True(t, fullHistory)
	// The shard count in CI must equal the script's, or tests would go unrun.
	script, err := os.ReadFile("cmd-mtix-shards.sh")
	require.NoError(t, err)
	require.Contains(t, string(script), "\nSHARDS=3\n")
	pg := jobs["test-go-postgres-cmd"].Strategy.Matrix.Shard
	require.Equal(t, job.Strategy.Matrix.Shard, pg)
}
