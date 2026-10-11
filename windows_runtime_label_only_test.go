// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Label-only workflow checks inspect actual YAML independently of the complete
// expected workflow model. Synthetic controls never stand in for hosted events.
package mtix_test

import (
	"fmt"
	"os"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

const windowsRuntimeLabelOnlyGate = "${{ github.event_name == 'pull_request' && github.event.action == 'labeled' && github.event.label.name == 'windows-ci' && github.event.pull_request.draft == false }}"

func windowsRuntimeLabelOnlyError(src []byte) error {
	var workflow map[string]any
	if err := yaml.Unmarshal(src, &workflow); err != nil {
		return fmt.Errorf("parse label-only Windows workflow: %w", err)
	}
	expectedOn := map[string]any{"pull_request": map[string]any{"branches": []any{"main"}, "types": []any{"labeled"}}}
	if !reflect.DeepEqual(workflow["on"], expectedOn) {
		return fmt.Errorf("Windows workflow must have only the main PR labeled trigger")
	}
	jobs, ok := workflow["jobs"].(map[string]any)
	if !ok {
		return fmt.Errorf("Windows workflow jobs missing")
	}
	job, ok := jobs["windows-runtime"].(map[string]any)
	if !ok {
		return fmt.Errorf("Windows runtime job missing")
	}
	if job["if"] != windowsRuntimeLabelOnlyGate {
		return fmt.Errorf("Windows runtime job must require exact label addition on a non-draft PR")
	}
	return nil
}

func TestCI_WindowsRuntimeLabelOnly_ActualSourceRequiresLabelGate(t *testing.T) {
	source, err := os.ReadFile(".github/workflows/windows-runtime.yml")
	require.NoError(t, err)
	require.NoError(t, windowsRuntimeLabelOnlyError(source))
}

func syntheticWindowsLabelOnlyWorkflow() map[string]any {
	return map[string]any{
		"on":   map[string]any{"pull_request": map[string]any{"branches": []any{"main"}, "types": []any{"labeled"}}},
		"jobs": map[string]any{"windows-runtime": map[string]any{"if": windowsRuntimeLabelOnlyGate}},
	}
}

func TestCI_WindowsRuntimeLabelOnly_SYNTHETICControls(t *testing.T) {
	cases := []struct {
		name   string
		change func(map[string]any)
		want   string
	}{
		{"valid label-only", func(map[string]any) {}, ""},
		{"manual dispatch", func(w map[string]any) { w["on"].(map[string]any)["workflow_dispatch"] = nil }, "only the main PR"},
		{"push", func(w map[string]any) { w["on"].(map[string]any)["push"] = nil }, "only the main PR"},
		{"schedule", func(w map[string]any) { w["on"].(map[string]any)["schedule"] = []any{} }, "only the main PR"},
		{"wrong branch", func(w map[string]any) {
			w["on"].(map[string]any)["pull_request"].(map[string]any)["branches"] = []any{"release"}
		}, "only the main PR"},
		{"synchronize", func(w map[string]any) {
			w["on"].(map[string]any)["pull_request"].(map[string]any)["types"] = []any{"labeled", "synchronize"}
		}, "only the main PR"},
		{"dispatch OR bypass", func(w map[string]any) {
			windowsRuntimeJob(w)["if"] = "${{ github.event_name == 'workflow_dispatch' || true }}"
		}, "exact label addition"},
		{"draft bypass", func(w map[string]any) {
			windowsRuntimeJob(w)["if"] = "${{ github.event_name == 'pull_request' && github.event.action == 'labeled' && github.event.label.name == 'windows-ci' }}"
		}, "exact label addition"},
		{"unrelated label", func(w map[string]any) {
			windowsRuntimeJob(w)["if"] = "${{ github.event_name == 'pull_request' && github.event.action == 'labeled' && github.event.label.name == 'other' && github.event.pull_request.draft == false }}"
		}, "exact label addition"},
		{"wrong event", func(w map[string]any) { windowsRuntimeJob(w)["if"] = "${{ github.event_name == 'push' }}" }, "exact label addition"},
		{"missing jobs", func(w map[string]any) { delete(w, "jobs") }, "jobs missing"},
		{"missing runtime job", func(w map[string]any) { delete(w["jobs"].(map[string]any), "windows-runtime") }, "job missing"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			workflow := syntheticWindowsLabelOnlyWorkflow()
			tc.change(workflow)
			source, err := yaml.Marshal(workflow)
			require.NoError(t, err)
			err = windowsRuntimeLabelOnlyError(source)
			if tc.want == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.want)
			}
		})
	}
}
