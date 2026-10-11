// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Fixture workflow guards inspect actual parsed YAML and executable selection.
// They are independent of the root package's expected workflow model and do
// not establish Windows execution or Windows-buildable test availability.
package docs

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

const stockReplayTestName = "TestAncestor_CapturedStockWindowsFixture_ReplaysPolicy"
const stockLiveCaptureTestName = "TestAncestor_StockWindowsAclCapture_AcceptsPolicy"
const stockNativeTestStepName = "Run required native ACL and hooks status tests"

func TestWindowsFixtureWorkflow_ActualSource_RequiresReplayAndLiveCapture(t *testing.T) {
	source, err := os.ReadFile("../../.github/workflows/windows-runtime.yml")
	require.NoError(t, err)
	require.NoError(t, stockFixtureWorkflowError(source))
}

func stockFixtureWorkflowError(source []byte) error {
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string `yaml:"name"`
				Run  string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(source, &workflow); err != nil {
		return fmt.Errorf("parse fixture workflow YAML: %w", err)
	}
	var scripts []string
	for _, step := range workflow.Jobs["windows-runtime"].Steps {
		if step.Name == stockNativeTestStepName {
			scripts = append(scripts, step.Run)
		}
	}
	if len(scripts) != 1 {
		return fmt.Errorf("fixture workflow must contain one named native test step")
	}
	return stockFixtureScriptError(scripts[0])
}

func stockFixtureScriptError(script string) error {
	selector := regexp.MustCompile(`^& go test ./internal/oplocal -race -count=1 -timeout=8m -json -run '([^']+)' > "\$env:MTIX_WINDOWS_CAPTURE_DIR/oplocal.json"$`)
	manifest := regexp.MustCompile(`^\$requiredOplocal = @\('([A-Za-z0-9_]+)'(?:, '[A-Za-z0-9_]+')*\)$`)
	var selections, manifests []string
	for _, line := range strings.Split(script, "\n") {
		line = strings.TrimSpace(line)
		if match := selector.FindStringSubmatch(line); match != nil {
			selections = append(selections, match[1])
		}
		if manifest.MatchString(line) {
			manifests = append(manifests, line)
		}
	}
	if len(selections) != 1 || len(manifests) != 1 {
		return fmt.Errorf("fixture workflow requires one executable oplocal selector and one requiredOplocal manifest")
	}
	selected, err := stockFixtureSelectedNames(selections[0])
	if err != nil {
		return err
	}
	var failures []error
	for _, name := range []string{stockReplayTestName, stockLiveCaptureTestName} {
		if selected[name] != 1 {
			failures = append(failures, fmt.Errorf("native selector must select %s exactly once", name))
		}
		if strings.Count(manifests[0], "'"+name+"'") != 1 {
			failures = append(failures, fmt.Errorf("requiredOplocal PASS manifest must require %s exactly once", name))
		}
	}
	return errors.Join(failures...)
}

func stockFixtureSelectedNames(selector string) (map[string]int, error) {
	if !strings.HasPrefix(selector, "^Test(") || !strings.HasSuffix(selector, ")$") {
		return nil, fmt.Errorf("fixture selector must be an anchored literal test-name alternation")
	}
	namePattern := regexp.MustCompile(`^[A-Za-z0-9_]+$`)
	names := map[string]int{}
	for _, branch := range strings.Split(strings.TrimSuffix(strings.TrimPrefix(selector, "^Test("), ")$"), "|") {
		if !namePattern.MatchString(branch) {
			return nil, fmt.Errorf("fixture selector contains nonliteral test name %q", branch)
		}
		names["Test"+branch]++
	}
	return names, nil
}

// SYNTHETIC minimal YAML is a guard control, never a workflow implementation.
func syntheticStockFixtureWorkflow(t *testing.T, script string) []byte {
	t.Helper()
	workflow := map[string]any{"jobs": map[string]any{"windows-runtime": map[string]any{
		"steps": []any{map[string]any{"name": stockNativeTestStepName, "run": script}},
	}}}
	source, err := yaml.Marshal(workflow)
	require.NoError(t, err)
	return source
}

func syntheticStockFixtureScript() string {
	return "& go test ./internal/oplocal -race -count=1 -timeout=8m -json -run '^Test(" +
		strings.TrimPrefix(stockReplayTestName, "Test") + "|" + strings.TrimPrefix(stockLiveCaptureTestName, "Test") +
		")$' > \"$env:MTIX_WINDOWS_CAPTURE_DIR/oplocal.json\"\n" +
		"$requiredOplocal = @('" + stockReplayTestName + "', '" + stockLiveCaptureTestName + "')\n"
}

func TestWindowsFixtureWorkflow_SYNTHETICControls_RejectMissingSelectorsAndManifest(t *testing.T) {
	valid := syntheticStockFixtureScript()
	replayBranch := strings.TrimPrefix(stockReplayTestName, "Test")
	liveBranch := strings.TrimPrefix(stockLiveCaptureTestName, "Test")
	tests := []struct {
		name   string
		script string
		want   string
	}{
		{"valid minimal contract", valid, ""},
		{"replay selector missing", strings.Replace(valid, replayBranch+"|", "", 1), "native selector must select " + stockReplayTestName},
		{"replay PASS missing", strings.Replace(valid, "'"+stockReplayTestName+"', ", "", 1), "PASS manifest must require " + stockReplayTestName},
		{"live selector missing", strings.Replace(valid, "|"+liveBranch, "", 1), "native selector must select " + stockLiveCaptureTestName},
		{"live PASS missing", strings.Replace(valid, ", '"+stockLiveCaptureTestName+"'", "", 1), "PASS manifest must require " + stockLiveCaptureTestName},
		{"duplicate replay selection", strings.Replace(valid, replayBranch, replayBranch+"|"+replayBranch, 1), "exactly once"},
		{"duplicate replay manifest", strings.Replace(valid, "'"+stockReplayTestName+"'", "'"+stockReplayTestName+"', '"+stockReplayTestName+"'", 1), "exactly once"},
		{"commented selector decoy", strings.Replace(valid, "& go test", "# & go test", 1), "one executable"},
		{"commented manifest decoy", strings.Replace(valid, "$requiredOplocal", "# $requiredOplocal", 1), "one executable"},
		{"unanchored selector", strings.Replace(valid, "^Test(", "Test(", 1), "anchored literal"},
		{"broad regex branch", strings.Replace(valid, replayBranch, "Ancestor_.*", 1), "nonliteral"},
		{"renamed replay selector", strings.Replace(valid, replayBranch, replayBranch+"Decoy", 1), "native selector must select"},
		{"duplicate executable invocation", valid + strings.Split(valid, "\n")[0] + "\n", "one executable"},
		{"duplicate manifest assignment", valid + strings.Split(valid, "\n")[1] + "\n", "one executable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := stockFixtureWorkflowError(syntheticStockFixtureWorkflow(t, tt.script))
			if tt.want == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tt.want)
			}
		})
	}
}

func TestWindowsFixtureWorkflow_SYNTHETICYAMLControls_RejectInvalidSource(t *testing.T) {
	tests := []struct{ name, source string }{
		{"empty", ""},
		{"malformed", "jobs: ["},
		{"missing job", "jobs: {}"},
		{"missing step", "jobs:\n  windows-runtime:\n    steps: []"},
		{"wrong step", "jobs:\n  windows-runtime:\n    steps:\n      - name: Decoy\n        run: SYNTHETIC"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Error(t, stockFixtureWorkflowError([]byte(tt.source)))
		})
	}
}
