// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// MTIX-56 command-level coverage for the FR-20 CLI surface.
package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// TestHooksExecDispatchCmd_GetSetRoundTrip drives the command RunE: default
// reads "any", a set persists, and a re-read returns it (MTIX-56.10).
func TestHooksExecDispatchCmd_GetSetRoundTrip(t *testing.T) {
	initTestApp(t)

	out, err := executeCmd(newHooksExecDispatchCmd())
	require.NoError(t, err)
	assert.Contains(t, out, "any", "default policy is any")

	_, err = executeCmd(newHooksExecDispatchCmd(), "daemon")
	require.NoError(t, err)

	out, err = executeCmd(newHooksExecDispatchCmd())
	require.NoError(t, err)
	assert.Contains(t, out, "daemon", "the set policy reads back")

	_, err = executeCmd(newHooksExecDispatchCmd(), "bogus")
	require.Error(t, err, "an invalid mode is rejected")
}

// TestWriteInbox_AllFormats exercises every rendering branch of writeInbox:
// the two agent-ready formats, JSON, the human listing, and the empty inbox.
func TestWriteInbox_AllFormats(t *testing.T) {
	events := []sqlite.InboxEvent{
		{Seq: 7, NodeID: "TEST-1", Author: "planner", Body: "start on TEST-1"},
	}

	t.Run("prompt", func(t *testing.T) {
		out := captureStdout(t, func() { require.NoError(t, writeInbox("dev", "prompt", events)) })
		assert.Contains(t, out, "start on TEST-1")
		assert.Contains(t, out, "mtix inbox ack")
	})
	t.Run("context", func(t *testing.T) {
		out := captureStdout(t, func() { require.NoError(t, writeInbox("dev", "context", events)) })
		assert.Contains(t, out, "[7] TEST-1")
	})
	t.Run("human", func(t *testing.T) {
		app.jsonOutput = false
		out := captureStdout(t, func() { require.NoError(t, writeInbox("dev", "", events)) })
		assert.Contains(t, out, "ack with: mtix inbox ack 7")
	})
	t.Run("human empty", func(t *testing.T) {
		app.jsonOutput = false
		out := captureStdout(t, func() { require.NoError(t, writeInbox("dev", "", nil)) })
		assert.Contains(t, out, "inbox empty")
	})
	t.Run("json", func(t *testing.T) {
		app.jsonOutput = true
		defer func() { app.jsonOutput = false }()
		out := captureStdout(t, func() { require.NoError(t, writeInbox("dev", "", nil)) })
		assert.Contains(t, out, "[]", "json empty inbox is an array, never null")
	})
}
