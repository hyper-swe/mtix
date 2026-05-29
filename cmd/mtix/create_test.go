// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// contextWarning helper tests — drive the warning text generator directly.
// Behavior:
//   - warn when --prompt is empty,
//   - warn when --acceptance is empty,
//   - warn (with both lines) when both are empty,
//   - no warning when both are populated.

func TestContextWarning_BothPopulated_ReturnsEmpty(t *testing.T) {
	got := contextWarning("PROJ-1", "the originating ask", "passing tests")
	assert.Empty(t, got)
}

func TestContextWarning_PromptEmpty_MentionsPrompt(t *testing.T) {
	got := contextWarning("PROJ-1", "", "passing tests")
	require.NotEmpty(t, got)
	assert.Contains(t, got, "PROJ-1")
	assert.Contains(t, got, "--prompt")
	assert.NotContains(t, got, "--acceptance:")
	assert.Contains(t, got, "mtix update PROJ-1 --prompt")
	assert.NotContains(t, got, "--acceptance \"")
}

func TestContextWarning_AcceptanceEmpty_MentionsAcceptance(t *testing.T) {
	got := contextWarning("PROJ-1", "the originating ask", "")
	require.NotEmpty(t, got)
	assert.Contains(t, got, "PROJ-1")
	assert.Contains(t, got, "--acceptance")
	assert.NotContains(t, got, "--prompt:")
	assert.Contains(t, got, "mtix update PROJ-1 --acceptance")
	assert.NotContains(t, got, "--prompt \"")
}

func TestContextWarning_BothEmpty_MentionsBoth(t *testing.T) {
	got := contextWarning("PROJ-1", "", "")
	require.NotEmpty(t, got)
	assert.Contains(t, got, "--prompt")
	assert.Contains(t, got, "--acceptance")
	assert.Contains(t, got, "mtix update PROJ-1 --prompt \"...\" --acceptance \"...\"")
}

func TestContextWarning_MentionsCompletenessTest(t *testing.T) {
	got := contextWarning("PROJ-1", "", "")
	assert.Contains(t, got, "Completeness test")
	assert.Contains(t, got, "zero conversation")
}

// captureStderr redirects os.Stderr for the duration of fn and returns what was written.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	orig := os.Stderr
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = orig })

	done := make(chan struct{})
	var buf bytes.Buffer
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()

	fn()
	_ = w.Close()
	<-done
	return buf.String()
}

// Integration tests — runCreate end-to-end with a real store.
// initTestApp lives in cli_edge_cases_test.go in this package.

func TestRunCreate_MissingPrompt_WarnsOnStderr(t *testing.T) {
	initTestApp(t)
	stderr := captureStderr(t, func() {
		err := runCreate("Lazy Task", "", "", 3, "some description", "", "passing tests", "", "")
		require.NoError(t, err)
	})
	assert.Contains(t, stderr, "--prompt")
	assert.NotContains(t, stderr, "--acceptance:")
}

func TestRunCreate_MissingAcceptance_WarnsOnStderr(t *testing.T) {
	initTestApp(t)
	stderr := captureStderr(t, func() {
		err := runCreate("Half-Lazy Task", "", "", 3, "desc", "originating ask", "", "", "")
		require.NoError(t, err)
	})
	assert.Contains(t, stderr, "--acceptance")
	assert.NotContains(t, stderr, "--prompt:")
}

func TestRunCreate_BothPromptAndAcceptance_NoWarning(t *testing.T) {
	initTestApp(t)
	stderr := captureStderr(t, func() {
		err := runCreate("Populated Task", "", "", 3, "desc", "originating ask", "passing tests", "", "")
		require.NoError(t, err)
	})
	assert.NotContains(t, stderr, "missing context fields")
}

func TestRunCreate_JSONMode_WarningOnStderrNotStdout(t *testing.T) {
	initTestApp(t)
	app.jsonOutput = true
	t.Cleanup(func() { app.jsonOutput = false })

	// Capture both stdout and stderr to verify warning routes to stderr only.
	rOut, wOut, err := os.Pipe()
	require.NoError(t, err)
	origOut := os.Stdout
	os.Stdout = wOut
	t.Cleanup(func() { os.Stdout = origOut })
	var stdoutBuf bytes.Buffer
	stdoutDone := make(chan struct{})
	go func() { _, _ = io.Copy(&stdoutBuf, rOut); close(stdoutDone) }()

	stderr := captureStderr(t, func() {
		err := runCreate("JSON Lazy Task", "", "", 3, "", "", "", "", "")
		require.NoError(t, err)
	})
	_ = wOut.Close()
	<-stdoutDone

	stdout := stdoutBuf.String()
	assert.Contains(t, stderr, "missing context fields")
	assert.NotContains(t, stdout, "missing context fields")
	// Stdout should still contain a JSON object.
	assert.True(t, strings.Contains(stdout, "\"id\"") || strings.Contains(stdout, "id"),
		"expected JSON node in stdout, got: %q", stdout)
}
