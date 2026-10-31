// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

// startFakeServe records a live serve (this test process) on the given port.
func startFakeServe(t *testing.T, port int) {
	t.Helper()
	mtixDir, err := findMtixDir()
	require.NoError(t, err)
	require.NoError(t, writePIDLock(mtixDir, port))
	t.Cleanup(func() { removePIDLock(mtixDir) })
}

func newURLTestNode(t *testing.T) string {
	t.Helper()
	node, err := app.nodeSvc.CreateNode(context.Background(), &service.CreateNodeRequest{
		Project: "TEST", Title: "Link target", Creator: "tester",
	})
	require.NoError(t, err)
	return node.ID
}

func TestURL_ServeRunning_PrintsAbsoluteTicketURL(t *testing.T) {
	initTestApp(t)
	id := newURLTestNode(t)
	startFakeServe(t, 6849)

	out := captureStdout(t, func() { require.NoError(t, runURL(id)) })
	assert.Equal(t, "http://127.0.0.1:6849/ticket/"+id+"\n", out)
}

func TestURL_ServeRunningJSON_EmitsIDAndURL(t *testing.T) {
	initTestApp(t)
	id := newURLTestNode(t)
	startFakeServe(t, 6849)
	app.jsonOutput = true

	out := captureStdout(t, func() { require.NoError(t, runURL(id)) })
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	assert.Equal(t, id, got["id"])
	assert.Equal(t, "http://127.0.0.1:6849/ticket/"+id, got["url"])
}

func TestURL_NoServe_FailsNamingServeWithEmptyStdout(t *testing.T) {
	initTestApp(t)
	id := newURLTestNode(t)

	var err error
	out := captureStdout(t, func() { err = runURL(id) })
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mtix serve")
	assert.Empty(t, out)
}

func TestURL_StaleLock_FailsNamingServe(t *testing.T) {
	initTestApp(t)
	id := newURLTestNode(t)
	mtixDir, err := findMtixDir()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(mtixDir+"/"+pidLockFile, []byte("999999999\n6849\n"), 0o600))

	var runErr error
	out := captureStdout(t, func() { runErr = runURL(id) })
	require.Error(t, runErr)
	assert.Contains(t, runErr.Error(), "mtix serve")
	assert.Empty(t, out)
}

func TestURL_BadIDs_RejectedOrNotFoundWithEmptyStdout(t *testing.T) {
	initTestApp(t)
	startFakeServe(t, 6849)
	tests := []struct {
		name    string
		id      string
		wantErr error
	}{
		{"unknown id", "TEST-9999", model.ErrNotFound},
		{"empty", "", model.ErrInvalidInput},
		{"script", "<script>alert(1)</script>", model.ErrInvalidInput},
		{"traversal", "../../etc/passwd", model.ErrInvalidInput},
		{"query injection", "TEST-1?x=1#frag", model.ErrInvalidInput},
		{"trailing newline", "TEST-1\n", model.ErrInvalidInput},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var err error
			out := captureStdout(t, func() { err = runURL(tt.id) })
			require.ErrorIs(t, err, tt.wantErr)
			assert.Empty(t, out)
		})
	}
}

func TestURL_Command_RegisteredExemptFromRoutingAndDeclaredRead(t *testing.T) {
	root := newRootCmd()
	cmd, _, err := root.Find([]string{"url"})
	require.NoError(t, err)
	require.Equal(t, "url", cmd.Name())
	assert.True(t, isExemptCommand(cmd), "url must run locally even while serve is running")
	assert.Equal(t, freshnessRead, commandFreshness(cmd, nil))
	assert.Error(t, cmd.Args(cmd, []string{}))
	assert.NoError(t, cmd.Args(cmd, []string{"TEST-1"}))
}

func TestURL_ExtraArgs_Rejected(t *testing.T) {
	cmd := newURLCmd()
	assert.Error(t, cmd.Args(cmd, []string{"TEST-1", "TEST-2"}))
}

func TestURL_NotInProject_FailsWithEmptyStdout(t *testing.T) {
	saveAndResetApp(t)
	var err error
	out := captureStdout(t, func() { err = runURL("TEST-1") })
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not in an mtix project")
	assert.Empty(t, out)
}

func TestURL_MtixDirMissing_ReportsErrorWithEmptyStdout(t *testing.T) {
	initTestApp(t)
	id := newURLTestNode(t)
	t.Chdir(t.TempDir())

	var err error
	out := captureStdout(t, func() { err = runURL(id) })
	require.Error(t, err)
	assert.Contains(t, err.Error(), ".mtix directory not found")
	assert.Empty(t, out)
}

func TestURL_Help_DocumentsLoopbackOnlyLimit(t *testing.T) {
	help := newURLCmd().Long
	assert.Contains(t, help, "loopback")
	assert.Contains(t, help, "--insecure-bind")
}
