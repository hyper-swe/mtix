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

// ciOSExprForm is the one accepted shape of the event-conditioned axis:
// fromJSON(github.event_name == 'E' && '[A]' || '[B]'). Anything else (an
// or in place of the and, an extra operand) does not match, so the test fails.
var ciOSExprForm = regexp.MustCompile(
	`^\$\{\{ fromJSON\(github\.event_name == '([a-z_]+)' && '(\[[^']*\])' \|\| '(\[[^']*\])'\) \}\}$`)

func (a *ciOSAxis) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		a.expr = node.Value
		return nil
	}
	return node.Decode(&a.list)
}

// For evaluates the axis for event ("pull_request", "push", "schedule"). A
// literal list applies to every event. An expression is evaluated with the
// GitHub semantics of cond && A || B for non-empty A; an unrecognised
// expression yields nil so the calling test fails.
func (a ciOSAxis) For(event string) []string {
	if a.expr == "" {
		return a.list
	}
	m := ciOSExprForm.FindStringSubmatch(a.expr)
	if m == nil {
		return nil
	}
	pick := m[3]
	if event == m[1] {
		pick = m[2]
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
	require.Equal(t, 25, countCIChecks(t, "pull_request"))
	require.Equal(t, 1, countCIMacOSLegs(t, "pull_request"))
	shards := jobs["test-go-shards"].Strategy.Matrix
	require.Equal(t, []string{"ubuntu-22.04", "macos-latest"}, shards.OS.For("pull_request"))
	require.Equal(t, []string{"other"}, shards.Shard)
	for _, name := range []string{"test-go-sqlite", "test-go-cmd", "test-go-service"} {
		m := jobs[name].Strategy.Matrix
		require.Equal(t, []string{"ubuntu-22.04"}, m.OS.For("pull_request"), name)
		wantShards := []string{"1", "2", "3"}
		if name == "test-go-service" {
			wantShards = []string{"1", "2"}
		}
		require.Equal(t, wantShards, m.Shard, name)
	}
}

func TestCIWorkflow_PushAndSchedule_RunEveryMacOSLeg(t *testing.T) {
	for _, event := range []string{"push", "schedule"} {
		require.Equal(t, 33, countCIChecks(t, event), event)
		require.Equal(t, 9, countCIMacOSLegs(t, event), event)
		for _, name := range []string{"test-go-shards", "test-go-sqlite", "test-go-cmd", "test-go-service"} {
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

func TestCIWorkflow_OSAxis_OnlyTheSQLiteCmdAndServiceJobsAreEventConditioned(t *testing.T) {
	for name, job := range readCIJobs(t) {
		m := job.Strategy.Matrix
		if name == "test-go-sqlite" || name == "test-go-cmd" || name == "test-go-service" {
			require.Equal(t, `${{ fromJSON(github.event_name == 'pull_request' && '["ubuntu-22.04"]' || '["ubuntu-22.04","macos-latest"]') }}`,
				m.OS.expr, name)
			require.Equal(t, []string{"ubuntu-22.04"}, m.OS.For("pull_request"), name)
			require.Equal(t, []string{"ubuntu-22.04", "macos-latest"}, m.OS.For("push"), name)
			require.Equal(t, []string{"ubuntu-22.04", "macos-latest"}, m.OS.For("schedule"), name)
			require.Equal(t, []string{"ubuntu-22.04", "macos-latest"}, m.OS.For("workflow_dispatch"), name)
			continue
		}
		require.Empty(t, m.OS.expr, name+" must not be event-conditioned")
	}
}

func TestCIWorkflow_TestGo_KeepsNameNeedsAndStrictAggregateOnEveryEvent(t *testing.T) {
	job := readCIJobs(t)["test-go"]
	require.Equal(t, "test-go", job.Name)
	require.Equal(t, []string{"test-go-shards", "test-go-sqlite", "test-go-cmd", "test-go-service"}, job.Needs)
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
		cmd.Env = append(os.Environ(), "SHARD_RESULT=success", "SQLITE_RESULT="+bad, "CMD_RESULT=success", "SERVICE_RESULT=success")
		out, err := cmd.CombinedOutput()
		require.Error(t, err, fmt.Sprintf("sqlite=%q: %s", bad, out))
	}
}

const ciOverrideLabel = "main-macos-fix"

func ciTestGoSteps(t *testing.T) map[string]int {
	t.Helper()
	index := map[string]int{}
	for i, step := range readCIJobs(t)["test-go"].Steps {
		index[step.Name] = i
	}
	return index
}

func TestCIWorkflow_MainMacOSHealth_RunsBaseScriptOnlyOnPullRequestsWithoutOverride(t *testing.T) {
	job := readCIJobs(t)["test-go"]
	idx := ciTestGoSteps(t)
	checkout, ok := idx["Check out the base branch's health script"]
	require.True(t, ok)
	health, ok := idx["Require main macOS CI to be green"]
	require.True(t, ok)
	note, ok := idx["Note the main macOS override"]
	require.True(t, ok)
	require.Less(t, checkout, health, "the script must be checked out before it runs")

	notOverridden := "${{ github.event_name == 'pull_request' && !contains(github.event.pull_request.labels.*.name, '" + ciOverrideLabel + "') }}"
	overridden := "${{ github.event_name == 'pull_request' && contains(github.event.pull_request.labels.*.name, '" + ciOverrideLabel + "') }}"
	require.Equal(t, notOverridden, job.Steps[checkout].If)
	require.Equal(t, notOverridden, job.Steps[health].If)
	require.Equal(t, overridden, job.Steps[note].If)

	var names []string
	for _, step := range job.Steps {
		names = append(names, step.Name)
	}
	require.Equal(t, []string{
		"Check out the base branch's health script", "Require every Go shard to succeed",
		"Note the main macOS override", "Require main macOS CI to be green",
	}, names, "test-go must hold exactly these steps in this order; no other checkout may replace the base script")
	co := job.Steps[checkout]
	require.True(t, strings.HasPrefix(co.Uses, "actions/checkout@"))
	require.Equal(t, map[string]any{
		"ref":                 "${{ github.event.pull_request.base.sha }}",
		"persist-credentials": false,
	}, co.With, "gate script must come from the base, with no path, repository or token override")

	h := job.Steps[health]
	require.Nil(t, h.ContinueOnError)
	require.Equal(t, "bash scripts/main-macos-health.sh", h.Run)
	require.Equal(t, map[string]string{
		"GH_TOKEN":          "${{ github.token }}",
		"GITHUB_REPOSITORY": "${{ github.repository }}",
	}, h.Env, "no secret and no other value reaches the gate")
}

func TestCIWorkflow_Permissions_OnlyTestGoReadsActions(t *testing.T) {
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
}

func TestMainMacOSExpectedJobs_TrackTheWorkflowMatrix(t *testing.T) {
	raw, err := os.ReadFile("main-macos-expected-jobs.txt")
	require.NoError(t, err)
	listed := strings.Split(strings.TrimSpace(string(raw)), "\n")
	var rendered []string
	jobs := readCIJobs(t)
	for _, key := range []string{"test-go-shards", "test-go-sqlite", "test-go-cmd", "test-go-service"} {
		job := jobs[key]
		for _, osName := range job.Strategy.Matrix.OS.For("push") {
			if !strings.HasPrefix(osName, "macos") {
				continue
			}
			for _, shard := range job.Strategy.Matrix.Shard {
				name := strings.ReplaceAll(job.Name, "${{ matrix.os }}", osName)
				rendered = append(rendered, strings.ReplaceAll(name, "${{ matrix.shard }}", shard))
			}
		}
	}
	require.ElementsMatch(t, rendered, listed, "expected-jobs list must equal the macOS legs the workflow generates")
	require.Len(t, listed, countCIMacOSLegs(t, "push"))
}
