// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package scripts_test

import (
	"os/exec"
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

// jqTB is the part of testing.TB that requireJQ uses, so a fake can record
// what the helper decided.
type jqTB interface {
	Helper()
	Fatal(args ...any)
	Skip(args ...any)
}

// requireJQ applies decideJQ to the real tool lookup and environment: it fails
// the test in CI, skips it locally with a stated reason, and returns when jq
// is installed (MTIX-218).
func requireJQ(t jqTB, lookPath func(string) (string, error), getenv func(string) string) {
	t.Helper()
	_, err := lookPath("jq")
	switch decision, reason := decideJQ(err == nil, getenv); decision {
	case jqFail:
		t.Fatal(reason)
	case jqSkip:
		t.Skip(reason)
	case jqProceed:
	}
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
		{"jq missing, CI 1 is not true", false, map[string]string{"CI": "1"}, jqSkip, []string{"jq"}},
		{"jq missing, CI TRUE is not true", false, map[string]string{"CI": "TRUE"}, jqSkip, []string{"jq"}},
		{"jq missing, GITHUB_ACTIONS false", false, map[string]string{"GITHUB_ACTIONS": "false"}, jqSkip, []string{"jq"}},
		{"jq missing, GITHUB_ACTIONS empty", false, map[string]string{"GITHUB_ACTIONS": ""}, jqSkip, []string{"jq"}},
		{"jq present, GITHUB_ACTIONS true", true, map[string]string{"GITHUB_ACTIONS": "true"}, jqProceed, nil},
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

// jqOutcomeTB records what the code under test did about jq and stops it, as
// t.Fatal and t.Skip do, by panicking with jqStop.
type jqOutcomeTB struct {
	testing.TB
	outcome string
}

type jqStop struct{}

func (r *jqOutcomeTB) Fatal(...any) { r.outcome = "fatal"; panic(jqStop{}) }
func (r *jqOutcomeTB) Skip(...any)  { r.outcome = "skip"; panic(jqStop{}) }

// runStubGh runs stubGh against a recording TB and returns what it did.
func runStubGh(t *testing.T) (outcome string) {
	t.Helper()
	rec := &jqOutcomeTB{TB: t}
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(jqStop); !ok {
				panic(r)
			}
		}
		outcome = rec.outcome
	}()
	stubGh(rec)
	return ""
}

// TestStubGh_MissingJQ_FailsInCIAndSkipsLocally pins the wiring of stubGh to
// the real PATH lookup and environment on a host that has jq: an empty PATH
// hides jq, and CI or GITHUB_ACTIONS decides between fail and skip.
func TestStubGh_MissingJQ_FailsInCIAndSkipsLocally(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"CI true fails", map[string]string{"CI": "true", "GITHUB_ACTIONS": ""}, "fatal"},
		{"GITHUB_ACTIONS true fails", map[string]string{"CI": "", "GITHUB_ACTIONS": "true"}, "fatal"},
		{"neither skips", map[string]string{"CI": "", "GITHUB_ACTIONS": ""}, "skip"},
		{"CI 1 skips", map[string]string{"CI": "1", "GITHUB_ACTIONS": ""}, "skip"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PATH", t.TempDir())
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			require.Equal(t, tc.want, runStubGh(t))
		})
	}
}

// TestStubGh_InstalledJQ_ProceedsEvenInCI pins that an installed jq never
// fails or skips, whatever CI says.
func TestStubGh_InstalledJQ_ProceedsEvenInCI(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not installed: the proceed case needs it (CI fails earlier in the other tests)")
	}
	t.Setenv("CI", "true")
	t.Setenv("GITHUB_ACTIONS", "true")
	require.Empty(t, runStubGh(t))
}
