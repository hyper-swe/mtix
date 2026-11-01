// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Windows process-liveness runtime contract (MTIX-233.1): the job runs the
// liveness tests natively and requires each to execute and PASS.
package mtix_test

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

const windowsLivenessStepName = "Run required native process liveness tests"
const windowsLivenessFilter = "^Test(IsProcessAlive_Table_ReportsPerPID|ProcessAliveFromProbe_Outcomes_ClassifiedConservatively)$"

func windowsLivenessNames() []string {
	return []string{"TestIsProcessAlive_Table_ReportsPerPID", "TestProcessAliveFromProbe_Outcomes_ClassifiedConservatively"}
}

func windowsLivenessScript() string {
	return strings.NewReplacer("@FILTER@", windowsLivenessFilter, "@NAMES@", windowsRuntimeQuotedNames(windowsLivenessNames())).Replace(windowsLivenessTemplate)
}

const windowsLivenessTemplate = `$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $false
& go test ./cmd/mtix -race -count=1 -timeout=5m -json -run '@FILTER@' > "$env:MTIX_WINDOWS_CAPTURE_DIR/liveness.json"
$livenessExit = $LASTEXITCODE
$requiredLiveness = @NAMES@
$package = 'github.com/hyper-swe/mtix/cmd/mtix'
$events = Get-Content -LiteralPath "$env:MTIX_WINDOWS_CAPTURE_DIR/liveness.json" | ForEach-Object { $_ | ConvertFrom-Json }
$badEvents = @($events | Where-Object { $_.Package -eq $package -and ($_.Action -eq 'fail' -or $_.Action -eq 'skip') })
if ($badEvents.Count -ne 0) { throw "Native liveness FAIL or SKIP: $package" }
foreach ($test in $requiredLiveness) {
  $ran = @($events | Where-Object { $_.Package -eq $package -and $_.Test -eq $test -and $_.Action -eq 'run' })
  $passed = @($events | Where-Object { $_.Package -eq $package -and $_.Test -eq $test -and $_.Action -eq 'pass' })
  if ($ran.Count -ne 1 -or $passed.Count -ne 1) { throw "Required native liveness test did not execute and PASS: $package/$test" }
}
$packagePassed = @($events | Where-Object { $_.Package -eq $package -and !$_.Test -and $_.Action -eq 'pass' })
if ($packagePassed.Count -ne 1) { throw "Required native liveness package did not PASS: $package" }
if ($livenessExit -ne 0) { throw "Native liveness test failure: liveness=$livenessExit" }
`

// The actual workflow runs the liveness tests in one step that names both.
func TestCI_WindowsLivenessWorkflow_ActualStepRunsBothTests(t *testing.T) {
	src, err := os.ReadFile(".github/workflows/windows-runtime.yml")
	require.NoError(t, err)
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string `yaml:"name"`
				Run  string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(src, &workflow))
	runs := []string{}
	for _, step := range workflow.Jobs["windows-runtime"].Steps {
		if step.Name == windowsLivenessStepName {
			runs = append(runs, step.Run)
		}
	}
	require.Len(t, runs, 1, "exactly one liveness step")
	require.Equal(t, windowsLivenessScript(), runs[0])
	for _, name := range windowsLivenessNames() {
		require.Contains(t, runs[0], name)
	}
}

// Each name the step requires must exist as a test in the package it runs.
func TestCI_WindowsLivenessWorkflow_NamedTestsExist(t *testing.T) {
	src, err := os.ReadFile("cmd/mtix/process_alive_test.go")
	require.NoError(t, err)
	for _, name := range windowsLivenessNames() {
		require.Contains(t, string(src), "func "+name+"(t *testing.T)")
	}
}

func TestCI_WindowsLivenessWorkflow_ExactControls(t *testing.T) {
	for _, tc := range []struct{ name, old, next string }{
		{"run ignored", "$ran.Count -ne 1", "$false"},
		{"pass ignored", "$passed.Count -ne 1", "$false"},
		{"fail-skip ignored", "$badEvents.Count -ne 0", "$false"},
		{"package ignored", "$packagePassed.Count -ne 1", "$false"},
		{"exit ignored", "$livenessExit -ne 0", "$false"},
		{"race omitted", "-race -count=1", "-count=1"},
		{"list only", "-json -run", "-list"},
		{"filter broadened", windowsLivenessFilter, "^Test.*$"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wf := windowsRuntimeExpectedWorkflow()
			step := windowsRuntimeStepNamed(t, wf, windowsLivenessStepName)
			run := step["run"].(string)
			require.Contains(t, run, tc.old)
			step["run"] = strings.Replace(run, tc.old, tc.next, 1)
			src, err := yaml.Marshal(wf)
			require.NoError(t, err)
			require.Error(t, windowsRuntimeWorkflowError(src))
		})
	}
	t.Run("artifact liveness.json", func(t *testing.T) {
		wf := windowsRuntimeExpectedWorkflow()
		artifact := windowsRuntimeStepNamed(t, wf, "Preserve explicit runtime evidence")["with"].(map[string]any)
		artifact["path"] = strings.Replace(artifact["path"].(string), windowsRuntimeDir+"/liveness.json\n", "", 1)
		src, err := yaml.Marshal(wf)
		require.NoError(t, err)
		require.Error(t, windowsRuntimeWorkflowError(src))
	})
	for _, kind := range []string{"missing", "duplicate", "masked"} {
		t.Run(kind, func(t *testing.T) {
			wf := windowsRuntimeExpectedWorkflow()
			steps := windowsRuntimeJob(wf)["steps"].([]any)
			switch kind {
			case "missing":
				steps = append(steps[:7], steps[8:]...)
			case "duplicate":
				steps = append(steps, steps[7])
			case "masked":
				steps[7].(map[string]any)["continue-on-error"] = true
			}
			windowsRuntimeJob(wf)["steps"] = steps
			src, err := yaml.Marshal(wf)
			require.NoError(t, err)
			require.Error(t, windowsRuntimeWorkflowError(src))
		})
	}
}
