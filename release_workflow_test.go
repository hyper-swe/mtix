// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Release workflow structure guards (MTIX-115). These require no database or
// provider credentials, so they deliberately have no e2e build tag: the normal
// go test ./... jobs in ci.yml and release.yml execute them on every change.
package mtix_test

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

type releaseWorkflow struct {
	On struct {
		Push struct {
			Tags []string `yaml:"tags"`
		} `yaml:"push"`
	} `yaml:"on"`
	Jobs map[string]releaseWorkflowJob `yaml:"jobs"`
}

type releaseWorkflowJob struct {
	Uses        string            `yaml:"uses"`
	Secrets     any               `yaml:"secrets"`
	Needs       any               `yaml:"needs"`
	Permissions map[string]string `yaml:"permissions"`
	Steps       []map[string]any  `yaml:"steps"`
}

func readReleaseWorkflow(t *testing.T) []byte {
	t.Helper()
	src, err := os.ReadFile(".github/workflows/release.yml")
	require.NoError(t, err)
	return src
}

func decodeReleaseWorkflow(src []byte) (releaseWorkflow, error) {
	var wf releaseWorkflow
	if err := yaml.Unmarshal(src, &wf); err != nil {
		return wf, fmt.Errorf("parse release workflow: %w", err)
	}
	return wf, nil
}

// TestCI_ReleaseNpmAuditGates pins the two blocking commands from
// QUALITY-STANDARDS.md §5.3. The separate full audit report is advisory;
// it cannot stand in for either blocking step, even if its name changes.
func TestCI_ReleaseNpmAuditGates(t *testing.T) {
	require.NoError(t, releaseNpmAuditGatesError(readReleaseWorkflow(t)))
}

func releaseNpmAuditGatesError(src []byte) error {
	wf, err := decodeReleaseWorkflow(src)
	if err != nil {
		return fmt.Errorf("check release npm audit gates: %w", err)
	}
	job, ok := wf.Jobs["preflight"]
	if !ok {
		return fmt.Errorf("release workflow must define preflight")
	}
	gates := []struct{ name, run string }{
		{"Run npm audit (shipped dependencies)", "cd web && npm audit --omit=dev --audit-level=high"},
		{"Run npm audit (build tooling, critical)", "cd web && npm audit --audit-level=critical"},
	}
	for _, gate := range gates {
		count := 0
		for _, step := range job.Steps {
			if step["name"] != gate.name {
				continue
			}
			count++
			if step["run"] != gate.run {
				return fmt.Errorf("%s must run %q without masking failures", gate.name, gate.run)
			}
			if _, exists := step["continue-on-error"]; exists {
				return fmt.Errorf("%s must not declare continue-on-error", gate.name)
			}
		}
		if count != 1 {
			return fmt.Errorf("expected exactly one %s step, got %d", gate.name, count)
		}
	}
	return nil
}

// TestCI_ReleaseJobConfigured follows the current reusable workflow call rather
// than the retired tagged provider scaffold. release.yml delegates DSN handling
// to cloud-contract.yml and must inherit secrets and wait for its result.
func TestCI_ReleaseJobConfigured(t *testing.T) {
	require.NoError(t, releaseJobConfiguredError(readReleaseWorkflow(t)))
}

func releaseJobConfiguredError(src []byte) error {
	wf, err := decodeReleaseWorkflow(src)
	if err != nil {
		return fmt.Errorf("check release job configuration: %w", err)
	}
	if len(wf.On.Push.Tags) != 1 || wf.On.Push.Tags[0] != "v*.*.*" {
		return fmt.Errorf("release must trigger on version tags")
	}
	cloud, ok := wf.Jobs["test-go-postgres-cloud"]
	if !ok {
		return fmt.Errorf("release must define test-go-postgres-cloud")
	}
	if cloud.Uses != "./.github/workflows/cloud-contract.yml" || len(cloud.Steps) != 0 {
		return fmt.Errorf("cloud job must call reusable cloud-contract.yml")
	}
	if cloud.Secrets != "inherit" {
		return fmt.Errorf("cloud job must inherit DSN secrets")
	}
	if cloud.Permissions["contents"] != "read" {
		return fmt.Errorf("cloud job must grant contents: read to reusable workflow")
	}
	if !releaseNeedsJob(cloud.Needs, "preflight") {
		return fmt.Errorf("cloud job must depend on preflight")
	}
	release, ok := wf.Jobs["release"]
	if !ok || !releaseNeedsJob(release.Needs, "preflight") || !releaseNeedsJob(release.Needs, "test-go-postgres-cloud") {
		return fmt.Errorf("release must wait for preflight and cloud contract gates")
	}
	return nil
}

func releaseNeedsJob(needs any, want string) bool {
	switch v := needs.(type) {
	case string:
		return v == want
	case []any:
		for _, dep := range v {
			if dep == want {
				return true
			}
		}
	}
	return false
}

// TestCI_ReleaseNpmAuditGates_MutatedWorkflowRejected proves red on copies:
// losing either severity flag, weakening the shipped-dependency scope, or
// masking a gate failure must be rejected. No checked-in YAML is modified.
func TestCI_ReleaseNpmAuditGates_MutatedWorkflowRejected(t *testing.T) {
	src := string(readReleaseWorkflow(t))
	gates := []struct{ name, command string }{
		{"shipped", "cd web && npm audit --omit=dev --audit-level=high"},
		{"tooling", "cd web && npm audit --audit-level=critical"},
	}
	for _, gate := range gates {
		mutations := []struct{ name, replacement string }{
			{"missing severity", "cd web && npm audit"},
			{"weaker severity", gate.command[:strings.Index(gate.command, "--audit-level=")] + "--audit-level=low"},
			{"continue-on-error", gate.command + "\n        continue-on-error: true"},
			{"conditional continue-on-error", gate.command + "\n        continue-on-error: ${{ always() }}"},
			{"masked failure", gate.command + " || true"},
			{"removed step command", "echo skipped"},
		}
		for _, mutation := range mutations {
			t.Run(gate.name+"/"+mutation.name, func(t *testing.T) {
				require.Contains(t, src, gate.command)
				mutated := strings.Replace(src, gate.command, mutation.replacement, 1)
				assert.Error(t, releaseNpmAuditGatesError([]byte(mutated)))
			})
		}
	}
	t.Run("lost shipped scope", func(t *testing.T) {
		mutated := strings.Replace(src, "npm audit --omit=dev --audit-level=high", "npm audit --audit-level=high", 1)
		assert.Error(t, releaseNpmAuditGatesError([]byte(mutated)))
	})
}

// TestCI_ReleaseJobConfigured_MutatedWorkflowRejected checks the reusable call,
// secret forwarding, permissions, tag trigger and release dependency graph.
func TestCI_ReleaseJobConfigured_MutatedWorkflowRejected(t *testing.T) {
	src := string(readReleaseWorkflow(t))
	mutations := []struct{ name, old, replacement string }{
		{"tag trigger", `- "v*.*.*"`, `- "main"`},
		{"missing cloud job", "  test-go-postgres-cloud:", "  retired-cloud:"},
		{"wrong reusable workflow", "uses: ./.github/workflows/cloud-contract.yml", "uses: ./.github/workflows/ci.yml"},
		{"missing inherited secrets", "secrets: inherit", "secrets: {}"},
		{"missing caller permissions", "    permissions:\n      contents: read\n    uses:", "    permissions: {}\n    uses:"},
		{"cloud bypasses preflight", "    needs: preflight", "    needs: []"},
		{"release bypasses cloud", "needs: [preflight, test-go-postgres-cloud]", "needs: [preflight]"},
		{"release bypasses preflight", "needs: [preflight, test-go-postgres-cloud]", "needs: [test-go-postgres-cloud]"},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			require.Contains(t, src, mutation.old)
			mutated := strings.Replace(src, mutation.old, mutation.replacement, 1)
			assert.Error(t, releaseJobConfiguredError([]byte(mutated)))
		})
	}
}
