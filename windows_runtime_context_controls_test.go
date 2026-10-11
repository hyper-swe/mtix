// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Synthetic YAML controls exercise the independent context and four-run-step
// contract. They do not call or serialize the existing expected workflow model.
package mtix_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestCI_WindowsRuntimeContext_EnvironmentControls(t *testing.T) {
	cases := []struct {
		name, location, expression string
		wantError                  bool
	}{
		{"job github", "job.env", "${{ github.sha }}", false},
		{"job needs", "job.env", "${{ needs.build.outputs.sha }}", false},
		{"job strategy", "job.env", "${{ strategy.job_index }}", false},
		{"job matrix", "job.env", "${{ matrix.os }}", false},
		{"job vars", "job.env", "${{ vars.VALUE }}", false},
		{"job secrets availability only", "job.env", "${{ secrets.VALUE }}", false},
		{"job inputs", "job.env", "${{ inputs.ref }}", false},
		{"job literals", "job.env", "literal runner.temp", false},
		{"job permitted fallback", "job.env", "${{ github.event.pull_request.head.sha || github.sha }}", false},
		{"job embedded multiple expressions", "job.env", "${{ github.server_url }}/${{ github.repository }}", false},
		{"job runner forbidden", "job.env", "${{ runner.temp }}/capture", true},
		{"job env forbidden", "job.env", "${{ env.VALUE }}", true},
		{"job job forbidden", "job.env", "${{ job.status }}", true},
		{"job steps forbidden", "job.env", "${{ steps.capture.outputs.dir }}", true},
		{"job hidden forbidden fallback", "job.env", "${{ github.sha || runner.temp }}", true},
		{"job second expression forbidden", "job.env", "${{ github.sha }}-${{ runner.temp }}", true},
		{"step runner allowed", "step.env", "${{ runner.temp }}/capture", false},
		{"step env allowed", "step.env", "${{ env.VALUE }}", false},
		{"step job allowed", "step.env", "${{ job.status }}", false},
		{"step steps allowed", "step.env", "${{ steps.capture.outputs.dir }}", false},
		{"unknown context", "job.env", "${{ mystery.value }}", true},
		{"unterminated", "job.env", "${{ runner.temp", true},
		{"empty", "job.env", "${{ }}", true},
		{"index outside supported subset", "job.env", "${{ github['sha'] }}", true},
		{"function outside supported subset", "job.env", "${{ format('x', github.sha) }}", true},
		{"quoted context outside supported subset", "job.env", "${{ 'runner.temp' }}", true},
		{"empty fallback", "job.env", "${{ github.sha || }}", true},
		{"nested opening", "job.env", "${{ github.sha ${{ runner.temp }} }}", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := windowsContextEnvError(map[string]any{"VALUE": tc.expression}, tc.location)
			if tc.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func windowsContextFixture() windowsContextWorkflow {
	job := windowsContextJob{Env: map[string]any{"CGO_ENABLED": "1", "HEAD": "${{ github.sha }}"}}
	for _, name := range windowsContextCaptureStepNames() {
		job.Steps = append(job.Steps, windowsContextStep{
			Name: name, Run: "Write-Output 'owned synthetic control'",
			Env: map[string]any{"MTIX_WINDOWS_CAPTURE_DIR": "${{ runner.temp }}/mtix-windows-runtime"},
		})
	}
	return windowsContextWorkflow{Jobs: map[string]windowsContextJob{"windows-runtime": job}}
}

func windowsContextFixtureParse(t *testing.T, workflow windowsContextWorkflow) windowsContextWorkflow {
	t.Helper()
	source, err := yaml.Marshal(workflow)
	require.NoError(t, err)
	parsed, err := windowsContextParse(source)
	require.NoError(t, err)
	return parsed
}

func TestCI_WindowsRuntimeContext_ParsedJobControls(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(windowsContextWorkflow)
	}{
		{"actual runner reference", func(w windowsContextWorkflow) { w.Jobs["windows-runtime"].Env["CAPTURE"] = "${{ runner.temp }}" }},
		{"another job runner reference", func(w windowsContextWorkflow) {
			w.Jobs["decoy"] = windowsContextJob{Env: map[string]any{"BAD": "${{ runner.temp }}"}}
		}},
		{"non-string environment", func(w windowsContextWorkflow) { w.Jobs["windows-runtime"].Env["BAD"] = []any{"${{ runner.temp }}"} }},
	}
	require.NoError(t, windowsContextJobsError(windowsContextFixtureParse(t, windowsContextFixture())))
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			workflow := windowsContextFixture()
			tc.mutate(workflow)
			require.Error(t, windowsContextJobsError(windowsContextFixtureParse(t, workflow)))
		})
	}
}

func TestCI_WindowsRuntimeContext_FourStepPlacementControls(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*windowsContextJob, int)
	}{
		{"missing step env", func(j *windowsContextJob, i int) { j.Steps[i].Env = nil }},
		{"missing capture key", func(j *windowsContextJob, i int) { delete(j.Steps[i].Env, "MTIX_WINDOWS_CAPTURE_DIR") }},
		{"wrong directory", func(j *windowsContextJob, i int) {
			j.Steps[i].Env["MTIX_WINDOWS_CAPTURE_DIR"] = "${{ runner.temp }}/wrong"
		}},
		{"wrong context", func(j *windowsContextJob, i int) {
			j.Steps[i].Env["MTIX_WINDOWS_CAPTURE_DIR"] = "${{ github.workspace }}/mtix-windows-runtime"
		}},
		{"empty run", func(j *windowsContextJob, i int) { j.Steps[i].Run = "" }},
		{"action instead of run", func(j *windowsContextJob, i int) { j.Steps[i].Run = ""; j.Steps[i].Uses = "actions/checkout@control" }},
		{"missing named step", func(j *windowsContextJob, i int) { j.Steps[i].Name = "wrong placement" }},
		{"duplicate named step", func(j *windowsContextJob, i int) { j.Steps = append(j.Steps, j.Steps[i]) }},
		{"step name decoy env", func(j *windowsContextJob, i int) {
			j.Steps[i].Env = map[string]any{"DECOY": "${{ runner.temp }}/mtix-windows-runtime"}
		}},
	}
	valid := windowsContextFixtureParse(t, windowsContextFixture())
	require.NoError(t, windowsContextCapturePlacementError(valid.Jobs["windows-runtime"]))
	for index, name := range windowsContextCaptureStepNames() {
		for _, tc := range cases {
			t.Run(name+"/"+tc.name, func(t *testing.T) {
				workflow := windowsContextFixture()
				job := workflow.Jobs["windows-runtime"]
				tc.mutate(&job, index)
				workflow.Jobs["windows-runtime"] = job
				parsed := windowsContextFixtureParse(t, workflow)
				require.Error(t, windowsContextCapturePlacementError(parsed.Jobs["windows-runtime"]))
			})
		}
	}
}

func TestCI_WindowsRuntimeContext_JobPlacementAndMalformedControls(t *testing.T) {
	workflow := windowsContextFixture()
	workflow.Jobs["windows-runtime"].Env["MTIX_WINDOWS_CAPTURE_DIR"] = "${{ github.workspace }}/mtix-windows-runtime"
	require.NoError(t, windowsContextJobsError(workflow), "available context does not make job-level capture placement valid")
	require.Error(t, windowsContextCapturePlacementError(windowsContextFixtureParse(t, workflow).Jobs["windows-runtime"]))
	for _, source := range []string{"", "jobs: [", "jobs: {}", "jobs:\n  windows-runtime:\n    env: []", "jobs:\n  windows-runtime:\n    env:\n      KEY: x\n      KEY: y"} {
		t.Run(source, func(t *testing.T) {
			_, err := windowsContextParse([]byte(source))
			require.Error(t, err)
		})
	}
}
