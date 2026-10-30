// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package scripts_test

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// jqDecision is what a gh-stub test does about the jq tool (MTIX-218).
type jqDecision int

const (
	// jqProceed means jq is installed and the stub tests run.
	jqProceed jqDecision = iota
	// jqSkip means jq is missing in a local run: skip with a stated reason.
	jqSkip
	// jqFail means jq is missing in CI: a silent skip would hide the gate.
	jqFail
)

// decideJQ maps jq availability and the environment to the action a gh-stub
// test takes. A missing jq fails when CI=true or GITHUB_ACTIONS=true, because a
// gate that can silently no-op is worse than none; a local run skips with a
// reason that names jq and the CI behaviour.
func decideJQ(jqFound bool, getenv func(string) string) (jqDecision, string) {
	if jqFound {
		return jqProceed, ""
	}
	if getenv("CI") == "true" || getenv("GITHUB_ACTIONS") == "true" {
		return jqFail, "jq is not installed in CI: the gh stub cannot apply its --jq filters, so the remote-bar stub tests would silently check nothing; install jq on the runner"
	}
	return jqSkip, "jq not installed: the gh stub cannot apply its --jq filters, so this remote-bar check is SKIPPED (CI fails instead of skipping); install jq to run it"
}

// TestDecideJQ_MissingTool_FailsInCIAndSkipsLocally pins MTIX-218: CI or
// GITHUB_ACTIONS set to true turns a missing jq into a failure, a local run
// skips with a reason, and an installed jq always proceeds.
func TestDecideJQ_MissingTool_FailsInCIAndSkipsLocally(t *testing.T) {
	cases := []struct {
		name    string
		found   bool
		env     map[string]string
		want    jqDecision
		mention []string
	}{
		{"jq present, no CI", true, nil, jqProceed, nil},
		{"jq present, CI", true, map[string]string{"CI": "true"}, jqProceed, nil},
		{"jq missing, no CI", false, nil, jqSkip, []string{"jq", "SKIPPED", "CI fails"}},
		{"jq missing, CI true", false, map[string]string{"CI": "true"}, jqFail, []string{"jq", "CI"}},
		{"jq missing, GITHUB_ACTIONS true", false, map[string]string{"GITHUB_ACTIONS": "true"}, jqFail, []string{"jq"}},
		{"jq missing, CI false", false, map[string]string{"CI": "false"}, jqSkip, []string{"jq"}},
		{"jq missing, CI empty", false, map[string]string{"CI": ""}, jqSkip, []string{"jq"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := decideJQ(tc.found, func(k string) string { return tc.env[k] })
			require.Equal(t, tc.want, got)
			if tc.want == jqProceed {
				require.Empty(t, reason)
			}
			for _, m := range tc.mention {
				require.Contains(t, reason, m)
			}
		})
	}
}
