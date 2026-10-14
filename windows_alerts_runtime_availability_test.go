// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Independently bind actual five-minute alert commands to Windows source tests.
package mtix_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func windowsAlertsActualRun(src []byte) (string, error) {
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string
				Run  string
			}
		}
	}
	if err := yaml.Unmarshal(src, &workflow); err != nil {
		return "", fmt.Errorf("parse alerts workflow: %w", err)
	}
	runs := []string{}
	for _, job := range workflow.Jobs {
		for _, step := range job.Steps {
			if step.Name == windowsAlertsStepName {
				runs = append(runs, step.Run)
			}
		}
	}
	if len(runs) != 1 || runs[0] == "" {
		return "", errors.New("actual workflow needs one executable alerts step")
	}
	return runs[0], nil
}

func windowsAlertsLiteralCommands(run string) (map[string]windowsNativeCommand, error) {
	pattern := regexp.MustCompile(`(?m)^& go test (\./[A-Za-z0-9_/]+) -race -count=1 -timeout=5m -json -run '([^']+)' > "\$env:MTIX_WINDOWS_CAPTURE_DIR/([A-Za-z0-9_-]+\.json)"$`)
	matches := pattern.FindAllStringSubmatch(run, -1)
	if len(matches) != 2 || len(matches) != strings.Count(run, "& go test ") {
		return nil, errors.New("alerts invocation must use two literal bounded commands")
	}
	result := map[string]windowsNativeCommand{}
	for _, match := range matches {
		if result[match[3]].directory != "" {
			return nil, errors.New("duplicate alerts output")
		}
		result[match[3]] = windowsNativeCommand{match[1], match[2]}
	}
	return result, nil
}

func windowsAlertsActualAvailabilityError(root string, src []byte) error {
	run, err := windowsAlertsActualRun(src)
	if err != nil {
		return err
	}
	if strings.Contains(run, "$optionalAlias") {
		return errors.New("alerts permit no optional alias")
	}
	commands, err := windowsAlertsLiteralCommands(run)
	if err != nil {
		return err
	}
	arrays, err := windowsLiteralArrays(run)
	if err != nil {
		return err
	}
	if len(arrays) != 2 {
		return errors.New("alerts require two result manifests")
	}
	module, err := windowsSourceModule(root)
	if err != nil {
		return err
	}
	return windowsAlertsBoundSuites(root, module, run, commands, arrays)
}

func windowsAlertsBoundSuites(root, module, run string, commands map[string]windowsNativeCommand, arrays map[string][]string) error {
	binding := regexp.MustCompile(`@\{ file='([^']+)'; package='([^']+)'; names=\$(required[A-Za-z]+); subcases=\$(mandatory[A-Za-z]+) \}`)
	matches := binding.FindAllStringSubmatch(run, -1)
	if len(matches) != 2 {
		return errors.New("alerts bindings must be complete")
	}
	usedFiles, usedArrays := map[string]bool{}, map[string]bool{}
	for _, match := range matches {
		command, present := commands[match[1]]
		if !present || usedFiles[match[1]] || usedArrays[match[3]] || arrays[match[3]] == nil {
			return errors.New("alerts bindings conflict")
		}
		if match[2] != module+"/"+strings.TrimPrefix(command.directory, "./") {
			return errors.New("alerts source package differs from binding")
		}
		usedFiles[match[1]], usedArrays[match[3]] = true, true
		names, sourceErr := windowsAvailableTests(filepath.Join(root, command.directory))
		if sourceErr != nil {
			return sourceErr
		}
		suite := windowsAvailableSuite{directory: command.directory, packageName: match[2], filter: command.filter, required: arrays[match[3]]}
		if suiteErr := windowsSuiteAvailabilityError(suite, names); suiteErr != nil {
			return suiteErr
		}
	}
	return nil
}

func TestCI_WindowsAlertsAvailability_ActualManifestBuildable(t *testing.T) {
	src, err := os.ReadFile(".github/workflows/windows-runtime.yml")
	require.NoError(t, err)
	require.NoError(t, windowsAlertsActualAvailabilityError(".", src))
}

func TestCI_WindowsAlertsAvailability_InvalidLiteralBindingsRejected(t *testing.T) {
	cases := []struct{ name, old, next string }{{"timeout", "-timeout=5m", "-timeout=50m"}, {"comment", "& go test", "# & go test"}, {"listing", "-json -run", "-list"}, {"package", "package='github.com/hyper-swe/mtix/internal/oplocal'", "package='different/package'"}, {"filter-exclusion", "ProjectionBoundaries_ReplaysWithoutExtraCount|", ""}, {"ungated-filter", "Recent_AbsentAndLimits_ReadOnly)$", "Recent_AbsentAndLimits_ReadOnly|Recent_InvalidPreparedHash_Refuses)$"}, {"nonexistent-test", "TestAlertRaise_SameClassWithinHour_CountIncrements'", "TestDoesNotExist'"}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wf := windowsRuntimeExpectedWorkflow()
			step := windowsRuntimeStepNamed(t, wf, windowsAlertsStepName)
			run := step["run"].(string)
			require.Contains(t, run, tc.old)
			step["run"] = strings.Replace(run, tc.old, tc.next, 1)
			src, err := yaml.Marshal(wf)
			require.NoError(t, err)
			require.Error(t, windowsAlertsActualAvailabilityError(".", src))
		})
	}
}

func windowsRuntimeStepNamed(t *testing.T, wf map[string]any, name string) map[string]any {
	t.Helper()
	for _, raw := range windowsRuntimeJob(wf)["steps"].([]any) {
		if step := raw.(map[string]any); step["name"] == name {
			return step
		}
	}
	t.Fatalf("workflow step %q not found", name)
	return nil
}
