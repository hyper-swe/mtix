// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Windows runtime workflow guards pin the founder-approved bounded capture
// contract. They validate YAML and executable text without spending CI minutes.
package mtix_test

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

const windowsRuntimeGate = "${{ github.event_name == 'pull_request' && github.event.action == 'labeled' && github.event.label.name == 'windows-ci' && github.event.pull_request.draft == false }}"
const windowsRuntimeGroup = "windows-runtime-${{ github.event.pull_request.number || github.ref }}"
const windowsRuntimeDir = "${{ runner.temp }}/mtix-windows-runtime"
const windowsRuntimeACLFilter = "^Test(State_ProtectedAccess_RoundTrips|ReadJSON_WideAccess_Refuses|Ancestor_TrusteeAndOwnerAccess_ValidatesPlatformPrincipals|Ancestor_RepresentativeAclRows_AllowSafeAncestorsAndRefuseHostileRights|Ensure_CanonicalHandleAlias_Refuses|State_ExclusionPrefixSpellings_RefusesBeforeCreation|OpenDirectory_ProspectiveExclusionRoot_RefusesBeforeCreation|State_DanglingExclusionAlias_AllowsUnrelatedState|State_RelativeDotExclusionSibling_AllowsAbsentReadAndFirstUse|Ancestor_StockWindowsAclCapture_AcceptsPolicy|Ancestor_CapturedStockWindowsFixture_ReplaysPolicy)$"

func TestCI_WindowsRuntimeWorkflow_ApprovedScope(t *testing.T) {
	src, err := os.ReadFile(".github/workflows/windows-runtime.yml")
	require.NoError(t, err, "separate bounded Windows capture workflow is required")
	require.NoError(t, windowsRuntimeWorkflowError(src))
}

func windowsRuntimeWorkflowError(src []byte) error {
	var wf map[string]any
	if err := yaml.Unmarshal(src, &wf); err != nil {
		return fmt.Errorf("parse Windows workflow: %w", err)
	}
	expected := windowsRuntimeExpectedWorkflow()
	return windowsRuntimeCompare("workflow", wf, expected)
}

// Exact executable scripts and structural keys intentionally pin this small,
// spending-sensitive workflow. Comments, decoy commands, failure masking and
// added step settings cannot substitute for the reviewed executable contract.
func windowsRuntimeCompare(path string, got, want any) error {
	if reflect.DeepEqual(got, want) {
		return nil
	}
	actual, isMap := got.(map[string]any)
	expected, wantMap := want.(map[string]any)
	if !isMap || !wantMap {
		return fmt.Errorf("%s differs from the approved executable contract", path)
	}
	if len(actual) != len(expected) {
		return fmt.Errorf("%s has missing or extra settings", path)
	}
	for key, value := range expected {
		if err := windowsRuntimeCompare(path+"."+key, actual[key], value); err != nil {
			return err
		}
	}
	return nil
}

func windowsRuntimeExpectedWorkflow() map[string]any {
	return map[string]any{
		"name":        "Windows operator-local runtime",
		"on":          map[string]any{"pull_request": map[string]any{"branches": []any{"main"}, "types": []any{"labeled"}}},
		"permissions": map[string]any{"contents": "read"},
		"concurrency": map[string]any{"group": windowsRuntimeGroup, "cancel-in-progress": true},
		"defaults":    map[string]any{"run": map[string]any{"shell": "pwsh"}},
		"jobs": map[string]any{"windows-runtime": map[string]any{
			"if": windowsRuntimeGate, "runs-on": "windows-latest", "timeout-minutes": 15,
			"env": windowsRuntimeEnvironment(), "steps": windowsRuntimeSteps(),
		}},
	}
}

func windowsRuntimeEnvironment() map[string]any {
	return map[string]any{
		"CGO_ENABLED":           "1",
		"MTIX_WINDOWS_HEAD_SHA": "${{ github.event.pull_request.head.sha || github.sha }}",
		"MTIX_WINDOWS_RUN_ID":   "${{ github.run_id }}", "MTIX_WINDOWS_RUN_ATTEMPT": "${{ github.run_attempt }}",
		"MTIX_WINDOWS_RUN_URL": "${{ github.server_url }}/${{ github.repository }}/actions/runs/${{ github.run_id }}",
		"MTIX_WINDOWS_REF":     "${{ github.ref }}",
	}
}

func windowsRuntimeSteps() []any {
	return []any{
		map[string]any{"name": "Start attempt accounting", "env": map[string]any{"MTIX_WINDOWS_CAPTURE_DIR": windowsRuntimeDir}, "run": windowsRuntimeStartScript},
		map[string]any{"name": "Checkout reviewed integration source", "uses": "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1", "with": map[string]any{"persist-credentials": false}},
		map[string]any{"name": "Set up module Go toolchain", "uses": "actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e", "with": map[string]any{"go-version-file": "go.mod"}},
		map[string]any{"name": "Verify Windows race compiler", "env": map[string]any{"MTIX_WINDOWS_CAPTURE_DIR": windowsRuntimeDir}, "run": windowsRuntimeCompilerScript},
		map[string]any{"name": "Run required native named-lock tests", "env": map[string]any{"MTIX_WINDOWS_CAPTURE_DIR": windowsRuntimeDir}, "run": windowsRuntimeNamedTestScript},
		map[string]any{"name": "Run required native ACL and hooks status tests", "env": map[string]any{"MTIX_WINDOWS_CAPTURE_DIR": windowsRuntimeDir}, "run": windowsRuntimeTestScript()},
		map[string]any{"name": windowsAlertsStepName, "env": map[string]any{"MTIX_WINDOWS_CAPTURE_DIR": windowsRuntimeDir}, "run": windowsAlertsScript()},
		map[string]any{"name": windowsLivenessStepName, "env": map[string]any{"MTIX_WINDOWS_CAPTURE_DIR": windowsRuntimeDir}, "run": windowsLivenessScript()},
		map[string]any{"name": "Record attempt duration", "env": map[string]any{"MTIX_WINDOWS_CAPTURE_DIR": windowsRuntimeDir}, "if": "${{ always() }}", "run": windowsRuntimeDurationScript},
		map[string]any{"name": "Preserve explicit runtime evidence", "if": "${{ always() }}", "uses": "actions/upload-artifact@cf430e030ddbb5b0abf93d22962f4752f3646cd9", "with": windowsRuntimeArtifactInputs()},
	}
}

func windowsRuntimeArtifactInputs() map[string]any {
	paths := []string{"capture.json", "oplocal.json", "hooks.json", "hooks-launch.json", "toolchain.json", "duration.json", "started.txt", "named-locks.json", "dispatch-lock.json", "named-lock-exits.json", "alerts.json", "alerts-doctor.json", "liveness.json"}
	for i := range paths {
		paths[i] = windowsRuntimeDir + "/" + paths[i]
	}
	return map[string]any{"name": "windows-runtime-${{ github.run_id }}-${{ github.run_attempt }}", "path": strings.Join(paths, "\n") + "\n", "if-no-files-found": "error", "retention-days": 14}
}

func TestCI_WindowsRuntimeWorkflow_ApprovedFixture(t *testing.T) {
	src, err := yaml.Marshal(windowsRuntimeExpectedWorkflow())
	require.NoError(t, err)
	require.NoError(t, windowsRuntimeWorkflowError(src))
}

type windowsRuntimeMutation struct {
	name   string
	mutate func(map[string]any)
}

func windowsRuntimeSpendingMutations() []windowsRuntimeMutation {
	return []windowsRuntimeMutation{
		{"push spend", func(w map[string]any) { w["on"].(map[string]any)["push"] = nil }},
		{"synchronize spend", func(w map[string]any) {
			w["on"].(map[string]any)["pull_request"].(map[string]any)["types"] = []any{"labeled", "synchronize"}
		}},
		{"wrong branch", func(w map[string]any) {
			w["on"].(map[string]any)["pull_request"].(map[string]any)["branches"] = []any{"*"}
		}},
		{"unrelated label", func(w map[string]any) {
			windowsRuntimeJob(w)["if"] = strings.Replace(windowsRuntimeGate, "github.event.label.name == 'windows-ci'", "contains(github.event.pull_request.labels.*.name, 'windows-ci')", 1)
		}},
		{"draft bypass", func(w map[string]any) {
			windowsRuntimeJob(w)["if"] = strings.Replace(windowsRuntimeGate, " && github.event.pull_request.draft == false", "", 1)
		}},
		{"write token", func(w map[string]any) { w["permissions"] = map[string]any{"contents": "write"} }},
		{"secret exposure", func(w map[string]any) { windowsRuntimeJob(w)["env"].(map[string]any)["TOKEN"] = "${{ secrets.TOKEN }}" }},
		{"job mask", func(w map[string]any) { windowsRuntimeJob(w)["continue-on-error"] = true }},
		{"job unbounded", func(w map[string]any) { delete(windowsRuntimeJob(w), "timeout-minutes") }},
		{"matrix expansion", func(w map[string]any) {
			windowsRuntimeJob(w)["strategy"] = map[string]any{"matrix": map[string]any{"go": []any{"1.25", "1.26"}}}
		}},
		{"PG services", func(w map[string]any) {
			windowsRuntimeJob(w)["services"] = map[string]any{"postgres": map[string]any{"image": "postgres:17"}}
		}},
		{"race disabled", func(w map[string]any) { windowsRuntimeJob(w)["env"].(map[string]any)["CGO_ENABLED"] = "0" }},
		{"overlapping spends", func(w map[string]any) { w["concurrency"].(map[string]any)["cancel-in-progress"] = false }},
	}
}

func windowsRuntimeExecutionMutations() []windowsRuntimeMutation {
	return []windowsRuntimeMutation{
		{"persistent credentials", func(w map[string]any) {
			windowsRuntimeStep(w, 1)["with"].(map[string]any)["persist-credentials"] = true
		}},
		{"floating action", func(w map[string]any) { windowsRuntimeStep(w, 7)["uses"] = "actions/upload-artifact@v7" }},
		{"toolchain override", func(w map[string]any) { windowsRuntimeStep(w, 2)["with"].(map[string]any)["go-version"] = "1.25" }},
		{"test skip", func(w map[string]any) { windowsRuntimeStep(w, 5)["if"] = false }},
		{"test failure mask", func(w map[string]any) { windowsRuntimeStep(w, 5)["continue-on-error"] = true }},
		{"duration skipped on failure", func(w map[string]any) { delete(windowsRuntimeStep(w, 6), "if") }},
		{"raw capture not preserved", func(w map[string]any) { delete(windowsRuntimeStep(w, 7), "if") }},
		{"arbitrary artifact dump", func(w map[string]any) {
			windowsRuntimeStep(w, 7)["with"].(map[string]any)["path"] = "${{ github.workspace }}/**"
		}},
		{"missing provenance", func(w map[string]any) { delete(windowsRuntimeJob(w)["env"].(map[string]any), "MTIX_WINDOWS_HEAD_SHA") }},
	}
}

func TestCI_WindowsRuntimeWorkflow_HostileSettingsRejected(t *testing.T) {
	cases := append(windowsRuntimeSpendingMutations(), windowsRuntimeExecutionMutations()...)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wf := windowsRuntimeExpectedWorkflow()
			tc.mutate(wf)
			src, err := yaml.Marshal(wf)
			require.NoError(t, err)
			require.Error(t, windowsRuntimeWorkflowError(src))
		})
	}
}

func windowsRuntimeJob(wf map[string]any) map[string]any {
	return wf["jobs"].(map[string]any)["windows-runtime"].(map[string]any)
}

func windowsRuntimeStep(wf map[string]any, index int) map[string]any {
	// Existing contract controls retain their original duration/artifact identities;
	// the alerts and liveness steps sit between the ACL step and them.
	if index >= 6 {
		index += 2
	}
	return windowsRuntimeJob(wf)["steps"].([]any)[index].(map[string]any)
}

func TestCI_WindowsRuntimeWorkflow_HostileScriptsRejected(t *testing.T) {
	cases := []struct {
		name             string
		step             int
		old, replacement string
	}{
		{"comment-only native invocation", 5, "& go test", "# & go test"},
		{"full suite expansion", 5, "./internal/oplocal", "./..."},
		{"broad Hooks selection", 5, "^TestHooksStatus_", "Hooks"},
		{"capture omitted", 5, "|Ancestor_StockWindowsAclCapture_AcceptsPolicy", ""},
		{"native exit ignored", 5, "$oplocalExit = $LASTEXITCODE", "$oplocalExit = 0"},
		{"race omitted", 5, "-race -count=1", "-count=1"},
		{"package timeout omitted", 5, "-timeout=8m", ""},
		{"only list tests", 5, "-json -run", "-list"},
		{"required top-level PASS omitted", 5, "Action -eq 'pass'", "Action -ne 'fail'"},
		{"package completion ignored", 5, "$packagePassed.Count -ne 1", "$false"},
		{"malformed JSON tolerated", 5, "ConvertFrom-Json", "ConvertFrom-Json -ErrorAction SilentlyContinue"},
		{"hooks exit ignored", 5, "$hooksExit = $LASTEXITCODE", "$hooksExit = 0"},
		{"package binding omitted", 5, "Package -eq $package", "Package -ne ''"},
		{"empty capture accepted", 5, "if (!(Test-Path", "if ($false -and !(Test-Path"},
		{"compiler library unverified", 3, "Test-Path $library", "$true"},
		{"compiler runtime floor weakened", 3, "-lt 8", "-lt 1"},
		{"Go compiler exit ignored", 3, "if ($LASTEXITCODE -ne 0)", "if ($false)"},
		{"checkout provenance faked", 5, "& git rev-parse HEAD", "'fake-sha'"},
		{"duration artifact not written", 6, "Set-Content", "Write-Output"},
		{"unbounded retries", 5, "$ErrorActionPreference = 'Stop'", "while ($true) {"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wf := windowsRuntimeExpectedWorkflow()
			step := windowsRuntimeStep(wf, tc.step)
			run := step["run"].(string)
			require.Contains(t, run, tc.old)
			step["run"] = strings.Replace(run, tc.old, tc.replacement, 1)
			src, err := yaml.Marshal(wf)
			require.NoError(t, err)
			require.Error(t, windowsRuntimeWorkflowError(src))
		})
	}
}

func TestCI_WindowsRuntimeWorkflow_InvalidYAMLRejected(t *testing.T) {
	for _, src := range []string{"", "jobs: [", "on: null\non: null\n", "jobs: {}"} {
		t.Run(src, func(t *testing.T) { require.Error(t, windowsRuntimeWorkflowError([]byte(src))) })
	}
}

// Only the existing 8.3 alias case permits a documented platform SKIP. The
// live capture, protected state and all other selected top-level cases require
// PASS, and the alias remains selected with exact execution/result accounting.
func TestCI_WindowsRuntimeWorkflow_OptionalAliasContractRejected(t *testing.T) {
	cases := []struct{ name, old, replacement string }{
		{"optional case not selected", "|Ensure_CanonicalHandleAlias_Refuses", ""},
		{"optional manifest disappeared", "$optionalAlias = 'TestEnsure_CanonicalHandleAlias_Refuses'", "$optionalAlias = ''"},
		{"missing optional execution accepted", "$aliasRun.Count -ne 1", "$false"},
		{"missing optional result accepted", "$aliasPassed.Count + $aliasSkipped.Count -ne 1", "$aliasPassed.Count + $aliasSkipped.Count -eq 2"},
		{"undocumented optional SKIP accepted", "$aliasSkipped.Count -eq 1 -and $aliasReason.Count -eq 0", "$false"},
		{"live capture or protected state SKIP accepted", "$passed.Count -ne 1 -or $skipped.Count -ne 0", "$passed.Count + $skipped.Count -ne 1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wf := windowsRuntimeExpectedWorkflow()
			step := windowsRuntimeStep(wf, 5)
			run := step["run"].(string)
			require.Contains(t, run, tc.old)
			step["run"] = strings.Replace(run, tc.old, tc.replacement, 1)
			src, err := yaml.Marshal(wf)
			require.NoError(t, err)
			require.Error(t, windowsRuntimeWorkflowError(src))
		})
	}
}
