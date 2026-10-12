// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// These independent guards inspect actual YAML environment placement against
// GitHub's context-availability table, without using the expected workflow model.
// Expression recognition is deliberately limited to dot references joined by
// || inside ${{ }} and literal surrounding text. Other syntax fails closed;
// this is neither the complete Actions expression parser nor hosted validation.
package mtix_test

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

type windowsContextWorkflow struct {
	Jobs map[string]windowsContextJob `yaml:"jobs"`
}

type windowsContextJob struct {
	Env   map[string]any       `yaml:"env"`
	Steps []windowsContextStep `yaml:"steps"`
}

type windowsContextStep struct {
	Name string         `yaml:"name"`
	Run  string         `yaml:"run"`
	Uses string         `yaml:"uses"`
	Env  map[string]any `yaml:"env"`
}

func TestCI_WindowsRuntimeContext_ActualJobEnvironmentAllowed(t *testing.T) {
	source, err := os.ReadFile(".github/workflows/windows-runtime.yml")
	require.NoError(t, err)
	workflow, err := windowsContextParse(source)
	require.NoError(t, err)
	require.NoError(t, windowsContextJobsError(workflow))
}

func TestCI_WindowsRuntimeContext_ActualCaptureDirectoryAtRunSteps(t *testing.T) {
	source, err := os.ReadFile(".github/workflows/windows-runtime.yml")
	require.NoError(t, err)
	workflow, err := windowsContextParse(source)
	require.NoError(t, err)
	require.NoError(t, windowsContextCapturePlacementError(workflow.Jobs["windows-runtime"]))
}

func windowsContextParse(source []byte) (windowsContextWorkflow, error) {
	var workflow windowsContextWorkflow
	if err := yaml.Unmarshal(source, &workflow); err != nil {
		return workflow, fmt.Errorf("parse context workflow: %w", err)
	}
	if _, exists := workflow.Jobs["windows-runtime"]; !exists {
		return workflow, errors.New("actual windows-runtime job missing")
	}
	return workflow, nil
}

func windowsContextJobsError(workflow windowsContextWorkflow) error {
	var failures []error
	for name, job := range workflow.Jobs {
		if err := windowsContextEnvError(job.Env, "job.env"); err != nil {
			failures = append(failures, fmt.Errorf("jobs.%s: %w", name, err))
		}
	}
	return errors.Join(failures...)
}

// Official table: https://docs.github.com/en/actions/reference/workflows-and-actions/contexts
// The secret context is syntactically available here; existing spending/security
// guards separately prohibit secret exposure in this concrete workflow.
func windowsContextAllowed(location string) []string {
	if location == "job.env" {
		return []string{"github", "needs", "strategy", "matrix", "vars", "secrets", "inputs"}
	}
	return []string{"github", "needs", "strategy", "matrix", "job", "runner", "env", "vars", "secrets", "steps", "inputs"}
}

func windowsContextEnvError(environment map[string]any, location string) error {
	var failures []error
	for name, value := range environment {
		text, isString := value.(string)
		if !isString {
			failures = append(failures, fmt.Errorf("%s.%s: expected string in targeted env contract", location, name))
			continue
		}
		if err := windowsContextExpressionsError(text, windowsContextAllowed(location)); err != nil {
			failures = append(failures, fmt.Errorf("%s.%s: %w", location, name, err))
		}
	}
	return errors.Join(failures...)
}

func windowsContextExpressionsError(text string, allowed []string) error {
	for {
		start := strings.Index(text, "${{")
		if start == -1 {
			return nil
		}
		text = text[start+3:]
		end := strings.Index(text, "}}")
		if end == -1 {
			return errors.New("unterminated Actions expression")
		}
		if err := windowsContextReferencesError(text[:end], allowed); err != nil {
			return err
		}
		text = text[end+2:]
	}
}

func windowsContextReferencesError(expression string, allowed []string) error {
	reference := regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)+$`)
	for _, part := range strings.Split(expression, "||") {
		part = strings.TrimSpace(part)
		if !reference.MatchString(part) {
			return fmt.Errorf("unsupported targeted Actions reference %q", part)
		}
		context := strings.SplitN(part, ".", 2)[0]
		if !windowsContextContains(allowed, context) {
			return fmt.Errorf("context %q unavailable at this environment location", context)
		}
	}
	return nil
}

func windowsContextContains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func windowsContextCaptureStepNames() []string {
	return []string{"Start attempt accounting", "Verify Windows race compiler", "Run required native ACL and hooks status tests", "Record attempt duration"}
}

func windowsContextCapturePlacementError(job windowsContextJob) error {
	var failures []error
	if _, exists := job.Env["MTIX_WINDOWS_CAPTURE_DIR"]; exists {
		failures = append(failures, errors.New("capture directory must be absent from job.env"))
	}
	for _, name := range windowsContextCaptureStepNames() {
		matches := 0
		for _, step := range job.Steps {
			if step.Name != name {
				continue
			}
			matches++
			if err := windowsContextCaptureStepError(step); err != nil {
				failures = append(failures, fmt.Errorf("step %q: %w", name, err))
			}
		}
		if matches != 1 {
			failures = append(failures, fmt.Errorf("required run step %q must occur exactly once (got %d)", name, matches))
		}
	}
	return errors.Join(failures...)
}

func windowsContextCaptureStepError(step windowsContextStep) error {
	if strings.TrimSpace(step.Run) == "" || step.Uses != "" {
		return errors.New("capture environment must belong to an executable run step")
	}
	if step.Env["MTIX_WINDOWS_CAPTURE_DIR"] != "${{ runner.temp }}/mtix-windows-runtime" {
		return errors.New("exact capture directory expression missing or misplaced at step.env")
	}
	return windowsContextEnvError(step.Env, "step.env")
}
