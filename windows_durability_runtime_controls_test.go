// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mtix_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestCI_WindowsDurabilityWorkflow_StepControls(t *testing.T) {
	for _, kind := range []string{"missing", "duplicate", "masked", "skipped"} {
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
			case "skipped":
				steps[7].(map[string]any)["if"] = false
			}
			windowsRuntimeJob(wf)["steps"] = steps
			src, err := yaml.Marshal(wf)
			require.NoError(t, err)
			require.Error(t, windowsRuntimeWorkflowError(src))
		})
	}
	wf := windowsRuntimeExpectedWorkflow()
	require.Equal(t, "Record attempt duration", windowsRuntimeStep(wf, 6)["name"])
	require.Equal(t, "Preserve explicit runtime evidence", windowsRuntimeStep(wf, 7)["name"])
}

func TestCI_WindowsDurabilityWorkflow_ArtifactControls(t *testing.T) {
	for _, file := range []string{"durability.json", "durability.cover", "durability-process.json", "durability-runtime.json"} {
		t.Run(file, func(t *testing.T) {
			wf := windowsRuntimeExpectedWorkflow()
			artifact := windowsRuntimeStep(wf, 7)["with"].(map[string]any)
			path := artifact["path"].(string)
			entry := windowsRuntimeDir + "/" + file + "\n"
			require.Contains(t, path, entry)
			artifact["path"] = strings.Replace(path, entry, "", 1)
			src, err := yaml.Marshal(wf)
			require.NoError(t, err)
			require.Error(t, windowsRuntimeWorkflowError(src))
		})
	}
}

func TestCI_WindowsDurabilityWorkflow_ExecutableControls(t *testing.T) {
	for _, tc := range windowsDurabilityScriptMutations() {
		t.Run(tc.name, func(t *testing.T) {
			wf := windowsRuntimeExpectedWorkflow()
			step := windowsRuntimeJob(wf)["steps"].([]any)[7].(map[string]any)
			run := step["run"].(string)
			require.Contains(t, run, tc.old)
			step["run"] = strings.Replace(run, tc.old, tc.next, 1)
			src, err := yaml.Marshal(wf)
			require.NoError(t, err)
			require.Error(t, windowsRuntimeWorkflowError(src))
		})
	}
}

type windowsDurabilityScriptMutation struct{ name, old, next string }

func windowsDurabilityScriptMutations() []windowsDurabilityScriptMutation {
	return []windowsDurabilityScriptMutation{
		{"run ignored", "$ran.Count -ne 1", "$false"},
		{"pass ignored", "$passed.Count -ne 1", "$false"},
		{"fail or skip ignored", "$bad.Count -ne 0", "$false"},
		{"process ignored", "$durabilityExit = $LASTEXITCODE", "$durabilityExit = 0"},
		{"package ignored", "$packagePassed.Count -ne 1", "$false"},
		{"package binding omitted", "$_.Package -eq $package", "$true"},
		{"subcases omitted", "@($required) + @($subcases)", "@($required)"},
		{"parser failure masked", "ConvertFrom-Json", "ConvertFrom-Json -ErrorAction SilentlyContinue"},
		{"race omitted", "-race -count=1", "-count=1"},
		{"coverage omitted", "-covermode=atomic", ""},
		{"full suite expansion", "./internal/oplocal", "./..."},
		{"unbounded timeout", "-timeout=5m", "-timeout=30m"},
		{"list only", "-json -run", "-list"},
		{"missing top-level requirement", "'TestWriteJSON_Windows_UsesWriteThrough', ", ""},
		{"flush count ignored", "$native.successful_flush_file_buffers -ne 2", "$false"},
		{"power loss invented", "$native.power_loss_tested -ne $false", "$false"},
		{"write-through ignored", "$observation.write_through -ne $true", "$false"},
		{"filesystem evidence ignored", "-or !$observation.filesystem", ""},
		{"source identity ignored", "if (!$env:MTIX_WINDOWS_HEAD_SHA -or !$env:MTIX_WINDOWS_CHECKOUT_SHA)", "if ($false)"},
	}
}
