// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

// Parsed command modes decide whether CLI JSON describes a cached read.
package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type freshnessModeCase struct {
	name string
	path string
	args []string
	read bool
}

func freshnessModeCases() []freshnessModeCase {
	return []freshnessModeCase{
		{"state-get", "agent state", []string{"writer"}, true},
		{"state-empty-set-is-get", "agent state", []string{"writer", "--set="}, true},
		{"state-set", "agent state", []string{"writer", "--set=idle"}, false},
		{"agent-register", "agent register", []string{"writer"}, false},
		{"agent-unregister", "agent unregister", []string{"writer"}, false},
		{"agent-heartbeat", "agent heartbeat", []string{"writer"}, false},
		{"session-start", "session start", []string{"writer"}, false},
		{"session-end", "session end", []string{"writer"}, false},
		{"session-summary", "session summary", []string{"writer"}, true},
		{"backfill-preview", "sync backfill", []string{"--dry-run"}, true},
		{"backfill-write", "sync backfill", nil, false},
		{"repair-preview", "sync repair", []string{"--status"}, true},
		{"repair-write", "sync repair", []string{"--status", "--apply"}, false},
		{"reconcile-default-preview", "sync reconcile", []string{"--discard-local"}, true},
		{"reconcile-write", "sync reconcile", []string{"--discard-local", "--yes"}, false},
		{"reconcile-explicit-preview-wins", "sync reconcile", []string{"--discard-local", "--dry-run", "--yes"}, true},
		{"migration-local-uid-write", "sync migrate", nil, false},
		{"migration-apply", "sync migrate", []string{"--yes"}, false},
		{"uid-repair-preview-still-backfills-local", "sync repair-uids", []string{"--dry-run"}, false},
		{"uid-repair-write", "sync repair-uids", nil, false},
		{"inbox-ack", "inbox ack", []string{"1", "--agent=writer"}, false},
		{"inbox-status", "inbox status", []string{"--agent=writer"}, true},
		{"lease-acquire", "inbox lease acquire", []string{"--agent=writer"}, false},
		{"lease-release", "inbox lease release", []string{"--agent=writer", "--holder=fixture-holder"}, false},
		{"configuration-set", "config set", []string{"sync.auto_sync", "true"}, false},
		{"configuration-delete", "config delete", []string{"sync.auto_sync"}, false},
		{"prompt-update", "prompt", []string{"TEST-1", "Updated prompt"}, false},
		{"annotation", "annotate", []string{"TEST-1", "Progress"}, false},
		{"dependency-add", "dep add", []string{"TEST-1", "TEST-2", "--type=related"}, false},
		{"unclassified-future-command", "future-output", nil, false},
		{"server-protocol", "serve", nil, false},
		{"mcp-protocol", "mcp", nil, false},
		{"daemon-protocol", "daemon", nil, false},
		{"sync-daemon-protocol", "sync daemon", nil, false},
		{"dependency-remove", "dep remove", []string{"TEST-1", "TEST-2", "--type=related"}, false},
	}
}

func TestCommandMode_BusyFreshnessMatchesParsedReadOrWrite(t *testing.T) {
	for _, tc := range freshnessModeCases() {
		t.Run(tc.name, func(t *testing.T) { checkCommandModeFreshness(t, tc) })
	}
}

func checkCommandModeFreshness(t *testing.T, tc freshnessModeCase) {
	t.Helper()
	pulledProject(t, "prefix: TEST\n")
	holdFastCLIImportLock(t)
	root := newRootCmd()
	if tc.path == "future-output" {
		root.AddCommand(&cobra.Command{Use: "future-output", RunE: func(*cobra.Command, []string) error { return nil }})
	}
	cmd, _, err := root.Find(strings.Fields(tc.path))
	require.NoError(t, err)
	require.Equal(t, "mtix "+tc.path, cmd.CommandPath())
	require.NoError(t, cmd.ParseFlags(tc.args))
	cmd.SetContext(testContext(t))
	app.jsonOutput = true
	notice := captureStderr(t, func() {
		require.NoError(t, persistentPreRun(cmd, cmd.Flags().Args(), ""))
	})
	require.Equal(t, "sync_lock_busy", app.staleReason)
	var output bytes.Buffer
	require.NoError(t, newJSONOutputWriter(&output).WriteJSON(map[string]string{"payload": "retained"}))
	var value map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(output.Bytes(), &value))
	require.JSONEq(t, `"retained"`, string(value["payload"]))
	if tc.read {
		assert.JSONEq(t, "true", string(value["stale"]))
		assert.JSONEq(t, `"sync_lock_busy"`, string(value["stale_reason"]))
		return
	}
	assert.NotContains(t, value, "stale")
	assert.NotContains(t, value, "stale_reason")
	assert.NotContains(t, strings.ToLower(notice), "retry this command")
	assert.NotContains(t, strings.ToLower(notice), "write succeeded", "a pre-run has not executed a write")
}
