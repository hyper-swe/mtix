// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/testutil"
)

// The parent holds the real lock; the production binary is a separate process.
// No injected adapter can hide alert coalescing with the no-adapter failure.
func TestEnrolGuard_RealBinary_AlertCauseAndFixSurvive(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "mtix")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, ".")
	out, err := build.CombinedOutput()
	require.NoError(t, err, "build real binary: %s", out)
	for _, harness := range []string{"claude-code", "codex", "pi"} {
		for _, mode := range []string{"running", "interrupted"} {
			t.Run(harness+"/"+mode, func(t *testing.T) { binaryEnrolAlertCase(t, binary, harness, mode) })
		}
	}
}

func binaryEnrolAlertCase(t *testing.T, binary, harness, mode string) {
	t.Helper()
	st := testutil.OperatorState(t)
	root := listedBoardCheckout(t, st)
	release := func() {}
	if mode == "running" {
		release = heldEnrolTestLock(t, root)
	} else {
		prepareEnrolGuardCase(t, root, mode)
	}
	before := enrolDataSnapshot(t, root)
	cmd := exec.CommandContext(t.Context(), binary, "attach", "session-start", "--harness", harness, "--protocol", "1")
	cmd.Dir = root
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	require.NoError(t, cmd.Run(), stderr.String())
	assert.Empty(t, stdout.String())
	assert.Contains(t, stderr.String(), "enrolment")
	assert.Equal(t, before, enrolDataSnapshot(t, root))
	found := false
	for _, alert := range attachAlerts(t, st) {
		if alert.Class == "attach.enrol-blocked" {
			found = true
			assert.Contains(t, alert.Detail, "enrolment")
			assert.Contains(t, alert.Fix, "mtix doctor")
			assert.NotEqual(t, operatorStateFix, alert.Fix)
			assert.Equal(t, uint64(1), alert.Count)
		}
	}
	assert.True(t, found, "a distinct enrolment alert must survive every production adapter outcome")
	release() // doctor may inspect an interrupted marker, but never an active lock
	assertBinaryEnrolDoctor(t, binary, root)
}

func assertBinaryEnrolDoctor(t *testing.T, binary, root string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), binary, "doctor", "--json")
	cmd.Dir = root
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		require.ErrorAs(t, err, &exit, stderr.String()) // other doctor FAIL rows may set exit 1
	}
	var rows []struct{ ID, Detail, Fix string }
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &rows), "doctor output: %s; stderr: %s", stdout.String(), stderr.String())
	found := false
	for _, row := range rows {
		if row.ID == "attach.failures" {
			found = true
			assert.Contains(t, row.Detail, "attach.enrol-blocked")
			assert.Contains(t, row.Detail, "enrolment")
			assert.Contains(t, row.Fix, "mtix doctor")
		}
	}
	assert.True(t, found, "doctor must expose the enrolment alert")
}
