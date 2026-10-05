// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Workflow toolchain guards (MTIX-116) run in the ordinary Go test bar, without
// e2e tags or provider credentials. Pin every real setup-go job and the paired
// blocking govulncheck command so a removed setup or textual decoy cannot pass.
package mtix_test

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

type toolchainWorkflow struct {
	Jobs map[string]toolchainJob `yaml:"jobs"`
}

type toolchainJob struct {
	Steps []toolchainStep `yaml:"steps"`
	Extra map[string]any  `yaml:",inline"`
}

type toolchainStep struct {
	Name  string            `yaml:"name"`
	Uses  string            `yaml:"uses"`
	With  map[string]string `yaml:"with"`
	Run   string            `yaml:"run"`
	Extra map[string]any    `yaml:",inline"`
}

type toolchainWorkflowCase struct {
	file string
	jobs []string
}

func toolchainWorkflowCases() []toolchainWorkflowCase {
	return []toolchainWorkflowCase{
		{"ci.yml", []string{"test-go", "lint", "test-go-postgres-docker", "test-fault-injection", "build"}},
		{"release.yml", []string{"preflight", "release"}},
		{"cloud-contract.yml", []string{"cloud-contract"}},
	}
}

func readToolchainWorkflow(t *testing.T, file string) []byte {
	t.Helper()
	src, err := os.ReadFile(".github/workflows/" + file)
	require.NoError(t, err)
	return src
}

func moduleToolchainVersion(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile("go.mod")
	require.NoError(t, err)
	for _, line := range strings.Split(string(src), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "toolchain" {
			parts := strings.Split(strings.TrimPrefix(fields[1], "go"), ".")
			require.GreaterOrEqual(t, len(parts), 2)
			return strings.Join(parts[:2], ".")
		}
	}
	t.Fatal("go.mod must declare a toolchain")
	return ""
}

// TestCI_WorkflowGoVersions follows the patched toolchain major/minor, keeping
// the module language line independent. All eight setup-go occurrences matter.
func TestCI_WorkflowGoVersions(t *testing.T) {
	version := moduleToolchainVersion(t)
	require.Equal(t, "1.26", version)
	for _, wf := range toolchainWorkflowCases() {
		t.Run(wf.file, func(t *testing.T) {
			require.NoError(t, workflowToolchainError(readToolchainWorkflow(t, wf.file), wf.jobs, version))
		})
	}
}

func workflowToolchainError(src []byte, jobs []string, version string) error {
	var wf toolchainWorkflow
	if err := yaml.Unmarshal(src, &wf); err != nil {
		return fmt.Errorf("parse workflow toolchain: %w", err)
	}
	counts := map[string]int{}
	for name, job := range wf.Jobs {
		for _, step := range job.Steps {
			if !strings.HasPrefix(step.Uses, "actions/setup-go@") {
				continue
			}
			counts[name]++
			if step.With["go-version"] != version {
				return fmt.Errorf("%s setup-go must select %s", name, version)
			}
		}
	}
	for _, name := range jobs {
		if counts[name] != 1 {
			return fmt.Errorf("%s must contain exactly one setup-go step", name)
		}
	}
	return nil
}

// TestCI_ReleaseGovulncheckPin pairs the verified current v1.8.0 release with
// Go 1.26. Exact executable run text and default failure handling prevent an
// advisory, comment-only or install-only substitute from masquerading as a scan.
func TestCI_ReleaseGovulncheckPin(t *testing.T) {
	require.NoError(t, releaseGovulncheckError(readToolchainWorkflow(t, "release.yml")))
}

func releaseGovulncheckError(src []byte) error {
	var wf toolchainWorkflow
	if err := yaml.Unmarshal(src, &wf); err != nil {
		return fmt.Errorf("parse release vulnerability gate: %w", err)
	}
	job := wf.Jobs["preflight"]
	if err := toolchainDefaultGateError(job.Extra); err != nil {
		return fmt.Errorf("preflight: %w", err)
	}
	const want = "go install golang.org/x/vuln/cmd/govulncheck@v1.8.0\n\"$(go env GOPATH)/bin/govulncheck\" $(go list ./... | grep -v node_modules)"
	count := 0
	for _, step := range job.Steps {
		if step.Name != "Go vulnerability scan" {
			continue
		}
		count++
		if strings.TrimSpace(step.Run) != want {
			return fmt.Errorf("vulnerability gate must install v1.8.0 and execute its scan")
		}
		if err := toolchainDefaultGateError(step.Extra); err != nil {
			return fmt.Errorf("vulnerability scan: %w", err)
		}
	}
	if count != 1 {
		return fmt.Errorf("preflight must contain exactly one Go vulnerability scan")
	}
	return nil
}

func toolchainDefaultGateError(extra map[string]any) error {
	for _, key := range []string{"if", "continue-on-error"} {
		if _, exists := extra[key]; exists {
			return fmt.Errorf("blocking gate must not declare %s", key)
		}
	}
	return nil
}

// TestCI_WorkflowGoVersions_MutatedSetupRejected covers each of the eight
// individual setup steps: old Go, missing version, or a different action with
// the desired version as a decoy must fail the same validator used by CI.
func TestCI_WorkflowGoVersions_MutatedSetupRejected(t *testing.T) {
	for _, tc := range toolchainWorkflowCases() {
		src := readToolchainWorkflow(t, tc.file)
		for _, job := range tc.jobs {
			for _, mutation := range []string{"old version", "missing version", "decoy action"} {
				t.Run(tc.file+"/"+job+"/"+mutation, func(t *testing.T) {
					mutated := mutateToolchainSetup(t, src, job, mutation)
					require.Error(t, workflowToolchainError(mutated, tc.jobs, "1.26"))
				})
			}
		}
	}
}

func mutateToolchainSetup(t *testing.T, src []byte, name, mutation string) []byte {
	t.Helper()
	var wf toolchainWorkflow
	require.NoError(t, yaml.Unmarshal(src, &wf))
	job, ok := wf.Jobs[name]
	require.True(t, ok)
	found := false
	for i := range job.Steps {
		step := &job.Steps[i]
		if !strings.HasPrefix(step.Uses, "actions/setup-go@") {
			continue
		}
		found = true
		switch mutation {
		case "old version":
			step.With["go-version"] = "1.25"
		case "missing version":
			delete(step.With, "go-version")
		case "decoy action":
			step.Uses = "actions/setup-node@decoy"
		default:
			t.Fatalf("unknown mutation %s", mutation)
		}
	}
	require.True(t, found)
	wf.Jobs[name] = job
	out, err := yaml.Marshal(wf)
	require.NoError(t, err)
	return out
}

// TestCI_ReleaseGovulncheckPin_MutatedGateRejected proves the release scanner
// remains pinned and blocking, even when install text survives in comments.
func TestCI_ReleaseGovulncheckPin_MutatedGateRejected(t *testing.T) {
	src := string(readToolchainWorkflow(t, "release.yml"))
	const install = "go install golang.org/x/vuln/cmd/govulncheck@v1.8.0"
	const scan = `"$(go env GOPATH)/bin/govulncheck" $(go list ./... | grep -v node_modules)`
	mutations := []struct{ name, old, replacement string }{
		{"old pin", install, strings.Replace(install, "v1.8.0", "v1.7.0", 1)},
		{"floating pin", install, strings.Replace(install, "v1.8.0", "latest", 1)},
		{"install comment decoy", install, "# " + install},
		{"missing scan", scan, "echo skipped"},
		{"scan comment decoy", scan, "# " + scan},
		{"masked scan", scan, scan + " || true"},
		{"conditional scan", "- name: Go vulnerability scan", "- name: Go vulnerability scan\n        if: false"},
		{"tolerated scan failure", "- name: Go vulnerability scan", "- name: Go vulnerability scan\n        continue-on-error: true"},
		{"missing gate", "- name: Go vulnerability scan", "- name: Retired vulnerability scan"},
		{"conditional preflight", "  preflight:\n", "  preflight:\n    if: false\n"},
		{"tolerated preflight failure", "  preflight:\n", "  preflight:\n    continue-on-error: ${{ always() }}\n"},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			require.Contains(t, src, mutation.old)
			mutated := strings.Replace(src, mutation.old, mutation.replacement, 1)
			require.Error(t, releaseGovulncheckError([]byte(mutated)))
		})
	}
}
