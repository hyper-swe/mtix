// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Exact contract controls reject missing or masked alert runtime evidence.
package mtix_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestCI_WindowsAlertsWorkflow_ExactControls(t *testing.T) {
	for _, tc := range []struct{ name, old, next string }{{"run ignored", "$ran.Count -ne 1", "$false"}, {"pass ignored", "$passed.Count -ne 1", "$false"}, {"fail-skip ignored", "$badEvents.Count -ne 0", "$false"}, {"package ignored", "$packagePassed.Count -ne 1", "$false"}, {"exit ignored", "$alertsExit = $LASTEXITCODE", "$alertsExit = 0"}, {"doctor exit ignored", "$doctorExit = $LASTEXITCODE", "$doctorExit = 0"}, {"subcases omitted", "@($suite.names) + @($suite.subcases)", "@($suite.names)"}, {"package binding omitted", "$_.Package -eq $package", "$true"}, {"parser error ignored", "ConvertFrom-Json", "ConvertFrom-Json -ErrorAction SilentlyContinue"}, {"race omitted", "-race -count=1", "-count=1"}, {"only list", "-json -run", "-list"}, {"extra timeout", "-timeout=5m", "-timeout=30m"}} {
		t.Run(tc.name, func(t *testing.T) {
			wf := windowsRuntimeExpectedWorkflow()
			step := windowsRuntimeJob(wf)["steps"].([]any)[6].(map[string]any)
			run := step["run"].(string)
			require.Contains(t, run, tc.old)
			step["run"] = strings.Replace(run, tc.old, tc.next, 1)
			src, err := yaml.Marshal(wf)
			require.NoError(t, err)
			require.Error(t, windowsRuntimeWorkflowError(src))
		})
	}
	for _, file := range []string{"alerts.json", "alerts-doctor.json"} {
		t.Run("artifact-"+file, func(t *testing.T) {
			wf := windowsRuntimeExpectedWorkflow()
			artifact := windowsRuntimeStep(wf, 7)["with"].(map[string]any)
			artifact["path"] = strings.Replace(artifact["path"].(string), windowsRuntimeDir+"/"+file+"\n", "", 1)
			src, err := yaml.Marshal(wf)
			require.NoError(t, err)
			require.Error(t, windowsRuntimeWorkflowError(src))
		})
	}
	for _, kind := range []string{"missing", "duplicate", "masked"} {
		t.Run(kind, func(t *testing.T) {
			wf := windowsRuntimeExpectedWorkflow()
			steps := windowsRuntimeJob(wf)["steps"].([]any)
			switch kind {
			case "missing":
				steps = append(steps[:6], steps[7:]...)
			case "duplicate":
				steps = append(steps, steps[6])
			case "masked":
				steps[6].(map[string]any)["continue-on-error"] = true
			}
			windowsRuntimeJob(wf)["steps"] = steps
			src, err := yaml.Marshal(wf)
			require.NoError(t, err)
			require.Error(t, windowsRuntimeWorkflowError(src))
		})
	}
}
