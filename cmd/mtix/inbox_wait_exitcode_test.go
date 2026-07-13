// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// MTIX-50: regression guard for the `mtix inbox --wait` empty-timeout exit
// contract (FR-19.4). A field report (seq 209, preview build) observed exit 0
// with an empty `[]` instead of the documented exit 5 on a first park. The
// contract was verified correct and not reproduced; these tests pin it at the
// cmd level — the exit-code mapping was previously untested end-to-end — so a
// real regression here can never ship silently.
package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureStdout (defined in projects_scope_test.go) redirects os.Stdout so
// these tests can assert the exact bytes `inbox` writes.

// TestRunInbox_WaitEmpty_JSON_ReturnsWaitEmptyErrorAndEmptyArray: the exact
// field-report shape — `inbox --agent X --wait --json` on an empty inbox. It
// must print `[]` AND return errInboxWaitEmpty, which maps to exit 5 (NOT 0).
func TestRunInbox_WaitEmpty_JSON_ReturnsWaitEmptyErrorAndEmptyArray(t *testing.T) {
	initTestApp(t)
	app.jsonOutput = true

	var err error
	out := captureStdout(t, func() {
		// timeout 0 collapses InboxWait to an immediate empty return, so the
		// test does not block; the empty-timeout path is identical.
		err = runInbox("nobody", true, 0, "")
	})

	require.ErrorIs(t, err, errInboxWaitEmpty, "an empty --wait timeout is the exit-5 sentinel, not success")
	assert.Equal(t, exitCodeInboxEmpty, exitCodeForError(err), "empty --wait must exit 5")
	assert.Equal(t, "[]", strings.TrimSpace(out), "JSON mode prints an empty array, never null")
}

// TestRunInbox_WaitEmpty_Human_ReturnsWaitEmptyError: the same contract in
// human mode — the message is friendly, the exit code is still 5.
func TestRunInbox_WaitEmpty_Human_ReturnsWaitEmptyError(t *testing.T) {
	initTestApp(t)
	app.jsonOutput = false

	var err error
	out := captureStdout(t, func() { err = runInbox("nobody", true, 0, "") })

	require.ErrorIs(t, err, errInboxWaitEmpty)
	assert.Equal(t, exitCodeInboxEmpty, exitCodeForError(err))
	assert.Contains(t, out, "inbox empty")
}

// TestRunInbox_NoWait_EmptyList_Exit0 documents the deliberate distinction that
// the field confusion likely stemmed from: WITHOUT --wait, a plain list of an
// empty inbox is a SUCCESS (exit 0) that prints `[]`. Only --wait carries the
// exit-5 sentinel. If a harness drops --wait, exit 0 + [] is correct behavior,
// not the bug — which is why the durable wake path is the MCP tool (MTIX-51).
func TestRunInbox_NoWait_EmptyList_Exit0(t *testing.T) {
	initTestApp(t)
	app.jsonOutput = true

	var err error
	out := captureStdout(t, func() { err = runInbox("nobody", false, 0, "") })

	require.NoError(t, err, "listing an empty inbox is success, not the wait sentinel")
	assert.Equal(t, 0, exitCodeForError(err))
	assert.Equal(t, "[]", strings.TrimSpace(out))
}
