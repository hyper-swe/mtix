// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
)

// TestRunAgentRegister registers an agent through the command path.
func TestRunAgentRegister(t *testing.T) {
	initTestApp(t)
	out := captureStdout(t, func() { require.NoError(t, runAgentRegister("worker-1")) })
	assert.Contains(t, out, "worker-1")
}

// TestRunUnblock refreshes a node's blocked state and reports it. A node with
// no dependencies is unblocked.
func TestRunUnblock(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	node, err := app.nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{Project: "TEST", Title: "t", Creator: "u"})
	require.NoError(t, err)

	out := captureStdout(t, func() { require.NoError(t, runUnblock(node.ID)) })
	assert.Contains(t, out, node.ID)

	require.Error(t, runUnblock("NOPE-999"), "a missing node errors")
}

// TestCreateFileLogger points logs at the project's .mtix/logs and returns a
// usable logger (the MCP server's log setup).
func TestCreateFileLogger(t *testing.T) {
	initTestApp(t)
	lg, err := createFileLogger()
	require.NoError(t, err)
	require.NotNil(t, lg)
	// The log directory is created under the project root.
	assert.DirExists(t, filepath.Join(app.mtixDir, "logs"))
}
