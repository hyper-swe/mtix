// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// MTIX-112.17.42: pull_request runs keep the full Linux matrix plus exactly
// one macOS job; push to main and the nightly schedule run every macOS leg.
// These tests read the real ci.yml and pin the per-event job counts, the
// event-conditioned OS axis, the absent-leg aggregate behavior and the
// pull_request-only main-macOS health step.
package scripts_test

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// ciOSAxis is a matrix os axis: either a literal list or a single
// fromJSON expression choosing a list by event. For returns the legs a run
// of the given event generates.
type ciOSAxis struct {
	list []string
	expr string
}

var ciOSExprLiteral = regexp.MustCompile(`'(\[[^']*\])'`)

func (a *ciOSAxis) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		a.expr = node.Value
		return nil
	}
	return node.Decode(&a.list)
}

// For returns the OS legs generated for event ("pull_request", "push",
// "schedule"). A literal list applies to every event.
func (a ciOSAxis) For(event string) []string {
	if a.expr == "" {
		return a.list
	}
	// The expression is: event_name == 'pull_request' && '[PR]' || '[FULL]'.
	lits := ciOSExprLiteral.FindAllStringSubmatch(a.expr, -1)
	if len(lits) != 2 {
		return nil
	}
	pick := lits[1][1]
	if event == "pull_request" {
		pick = lits[0][1]
	}
	var legs []string
	if err := yaml.Unmarshal([]byte(pick), &legs); err != nil {
		return nil
	}
	return legs
}

func countCIChecks(t *testing.T, event string) int {
	t.Helper()
	count := 0
	for _, job := range readCIJobs(t) {
		combinations := 1
		m := job.Strategy.Matrix
		for _, axis := range [][]string{m.OS.For(event), m.Shard, m.PG, m.Pkg} {
			if len(axis) > 0 {
				combinations *= len(axis)
			}
		}
		count += combinations
	}
	return count
}

func countCIMacOSLegs(t *testing.T, event string) int {
	t.Helper()
	count := 0
	for _, job := range readCIJobs(t) {
		m := job.Strategy.Matrix
		for _, os := range m.OS.For(event) {
			if strings.HasPrefix(os, "macos") {
				combinations := 1
				for _, axis := range [][]string{m.Shard, m.PG, m.Pkg} {
					if len(axis) > 0 {
						combinations *= len(axis)
					}
				}
				count += combinations
			}
		}
	}
	return count
}

func TestCIWorkflow_PullRequest_RunsFullLinuxPlusExactlyOneMacOSJob(t *testing.T) {
	jobs := readCIJobs(t)
	require.Equal(t, 23, countCIChecks(t, "pull_request"))
	require.Equal(t, 1, countCIMacOSLegs(t, "pull_request"))
	shards := jobs["test-go-shards"].Strategy.Matrix
	require.Equal(t, []string{"ubuntu-22.04", "macos-latest"}, shards.OS.For("pull_request"))
	require.Equal(t, []string{"other"}, shards.Shard)
	for _, name := range []string{"test-go-sqlite", "test-go-cmd"} {
		m := jobs[name].Strategy.Matrix
		require.Equal(t, []string{"ubuntu-22.04"}, m.OS.For("pull_request"), name)
		require.Equal(t, []string{"1", "2", "3"}, m.Shard, name)
	}
}

func TestCIWorkflow_PushAndSchedule_RunEveryMacOSLeg(t *testing.T) {
	for _, event := range []string{"push", "schedule"} {
		require.Equal(t, 29, countCIChecks(t, event), event)
		require.Equal(t, 7, countCIMacOSLegs(t, event), event)
		for _, name := range []string{"test-go-shards", "test-go-sqlite", "test-go-cmd"} {
			require.Equal(t, []string{"ubuntu-22.04", "macos-latest"},
				readCIJobs(t)[name].Strategy.Matrix.OS.For(event), event+"/"+name)
		}
	}
}

func TestCIWorkflow_Triggers_IncludeNightlyScheduleAndMainPush(t *testing.T) {
	source, err := os.ReadFile("../.github/workflows/ci.yml")
	require.NoError(t, err)
	var workflow struct {
		On struct {
			Push struct{ Branches []string }
			PR   struct{ Branches []string } `yaml:"pull_request"`
			Cron []struct{ Cron string }     `yaml:"schedule"`
		} `yaml:"on"`
	}
	require.NoError(t, yaml.Unmarshal(source, &workflow))
	require.Equal(t, []string{"main"}, workflow.On.Push.Branches)
	require.Equal(t, []string{"main"}, workflow.On.PR.Branches)
	require.Len(t, workflow.On.Cron, 1)
	require.Regexp(t, `^\d+ \d+ \* \* \*$`, workflow.On.Cron[0].Cron)
}

func TestCIWorkflow_OSAxis_OnlyTheTwoSQLiteAndCmdJobsAreEventConditioned(t *testing.T) {
	for name, job := range readCIJobs(t) {
		m := job.Strategy.Matrix
		if name == "test-go-sqlite" || name == "test-go-cmd" {
			require.NotEmpty(t, m.OS.expr, name)
			require.Contains(t, m.OS.expr, "github.event_name == 'pull_request'", name)
			continue
		}
		require.Empty(t, m.OS.expr, name+" must not be event-conditioned")
	}
}

func TestCIWorkflow_TestGo_KeepsNameNeedsAndStrictAggregateOnEveryEvent(t *testing.T) {
	job := readCIJobs(t)["test-go"]
	require.Equal(t, "test-go", job.Name)
	require.Equal(t, []string{"test-go-shards", "test-go-sqlite", "test-go-cmd"}, job.Needs)
	require.Equal(t, "${{ always() }}", job.If)
	var aggregate *ciGoStep
	for i := range job.Steps {
		if job.Steps[i].Name == "Require every Go shard to succeed" {
			aggregate = &job.Steps[i]
		}
	}
	require.NotNil(t, aggregate)
	require.Empty(t, aggregate.If, "absent legs are absent from the matrix; the aggregate never tolerates skipped needs")
	for _, bad := range []string{"skipped", "cancelled", "failure", ""} {
		cmd := exec.Command("bash", "-e", "-o", "pipefail", "-c", aggregate.Run)
		cmd.Env = append(os.Environ(), "SHARD_RESULT=success", "SQLITE_RESULT="+bad, "CMD_RESULT=success")
		out, err := cmd.CombinedOutput()
		require.Error(t, err, fmt.Sprintf("sqlite=%q: %s", bad, out))
	}
}

func TestCIWorkflow_MainMacOSHealthStep_RunsOnlyOnPullRequestsAndIsLeastPrivilege(t *testing.T) {
	source, err := os.ReadFile("../.github/workflows/ci.yml")
	require.NoError(t, err)
	var workflow struct {
		Permissions map[string]string `yaml:"permissions"`
		Jobs        map[string]struct {
			Permissions map[string]string `yaml:"permissions"`
		}
	}
	require.NoError(t, yaml.Unmarshal(source, &workflow))
	require.Equal(t, map[string]string{"contents": "read"}, workflow.Permissions)
	for name, job := range workflow.Jobs {
		if name == "test-go" {
			require.Equal(t, map[string]string{"contents": "read", "actions": "read"}, job.Permissions)
		} else {
			require.Empty(t, job.Permissions, name+" must not widen permissions")
		}
	}
	var health *ciGoStep
	for _, step := range readCIJobs(t)["test-go"].Steps {
		if strings.Contains(step.Run, "scripts/main-macos-health.sh") {
			health = &step
		}
	}
	require.NotNil(t, health, "test-go must run the main macOS health check")
	require.Equal(t, "github.event_name == 'pull_request'", strings.Trim(health.If, "${} "))
	require.Nil(t, health.ContinueOnError)
	require.Equal(t, "${{ github.token }}", health.Env["GH_TOKEN"])
	require.Equal(t, "${{ github.repository }}", health.Env["GITHUB_REPOSITORY"])
	require.Equal(t, "${{ toJSON(github.event.pull_request.labels.*.name) }}", health.Env["PR_LABELS"])
}
