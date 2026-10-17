// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

// Busy-lock mutation acknowledgements must not invite duplicate writes.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

type mutationBusyCase struct {
	name string
	args []string
	seed func(*testing.T)
}

func mutationBusyCases() []mutationBusyCase {
	return []mutationBusyCase{
		{"create", []string{"create", "Create exactly once", "--prompt", "Create once", "--acceptance", "One node"}, nil},
		{"claim", []string{"claim", "TEST-1", "--agent", "writer"}, func(t *testing.T) { registerTestAgent(t, "writer") }},
		{"unclaim", []string{"unclaim", "TEST-1", "--reason", "handoff", "--agent", "writer"}, func(t *testing.T) {
			registerTestAgent(t, "writer")
			require.NoError(t, app.nodeSvc.ClaimNode(testContext(t), "TEST-1", "writer"))
		}},
		{"done", []string{"done", "TEST-1"}, func(t *testing.T) { transitionToInProgress(t, "TEST-1") }},
		{"unblock", []string{"unblock", "TEST-1"}, nil},
		{"inbox-ack", []string{"inbox", "ack", "1", "--agent", "writer", "--through"}, func(t *testing.T) { registerTestAgent(t, "writer") }},
	}
}

// Exercise complete Cobra execution with native sync ownership contention,
// including the successful mutation's post-run and its acknowledgement.
func TestMutationBusyLock_CobraAcknowledgements(t *testing.T) {
	for _, tc := range mutationBusyCases() {
		t.Run(tc.name, func(t *testing.T) { checkMutationBusyCase(t, tc) })
	}
}

func checkMutationBusyCase(t *testing.T, tc mutationBusyCase) {
	t.Helper()
	pulledProject(t, "prefix: TEST\n")
	holdFastCLIImportLock(t)
	root := newRootCmd()
	cmd, _, err := root.Find(tc.args)
	require.NoError(t, err)
	require.NotEqual(t, root, cmd)
	var notice bytes.Buffer
	root.SetErr(&notice)
	original := cmd.RunE
	require.NotNil(t, original)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		assert.NotContains(t, strings.ToLower(notice.String()), "write succeeded", "startup cannot announce a write before it runs")
		if tc.seed != nil {
			tc.seed(t)
		}
		return original(cmd, args)
	}
	root.SetArgs(append([]string{"--json"}, tc.args...))
	ctx, cancel := context.WithTimeout(testContext(t), 5*time.Second)
	defer cancel()
	var output string
	stderr := captureStderr(t, func() {
		output = captureStdout(t, func() {
			require.NoError(t, root.ExecuteContext(ctx), "busy ownership must not fail a successful write")
		})
	})
	checkMutationBusyAcknowledgement(t, tc.name, output, notice.String()+stderr)
	if tc.name == "create" {
		checkMutationBusyCreateOnce(ctx, t)
	}
}

func checkMutationBusyAcknowledgement(t *testing.T, name, output, stderr string) {
	t.Helper()
	var ack map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(output), &ack), output)
	require.NotEmpty(t, ack, "successful acknowledgement retains its payload")
	assert.NotContains(t, ack, "stale", "freshness describes reads, not successful mutation acknowledgements")
	assert.NotContains(t, ack, "stale_reason")
	guidance := strings.ToLower(stderr)
	t.Logf("successful %s acknowledgement: %s; stderr: %s", name, output, guidance)
	assert.NotContains(t, guidance, "retry this command", "successful writes must not invite repetition")
	assert.Equal(t, 1, strings.Count(guidance, "write succeeded"), "completion reports success exactly once")
	assert.Regexp(t, `(must not|do not|never) (repeat|retry)`, guidance, "success guidance explicitly forbids repeating the write")
}

func checkMutationBusyCreateOnce(ctx context.Context, t *testing.T) {
	t.Helper()
	store, err := sqlite.New(filepath.Join(".mtix", "data"), nil)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	board, err := store.Export(ctx, "", "")
	require.NoError(t, err)
	created := 0
	for _, node := range board.Nodes {
		if node.Title == "Create exactly once" {
			created++
		}
	}
	assert.Equal(t, 1, created, "one successful execution committed exactly one node")
}

// An execution error must never receive the successful-write completion notice.
func TestMutationBusyLock_FailedCobraWriteDoesNotClaimSuccess(t *testing.T) {
	pulledProject(t, "prefix: TEST\n")
	holdFastCLIImportLock(t)
	root := newRootCmd()
	var notice bytes.Buffer
	root.SetErr(&notice)
	root.SetArgs([]string{"--json", "create", "Invalid write", "--priority", "99"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stderr := captureStderr(t, func() {
		captureStdout(t, func() { require.Error(t, root.ExecuteContext(ctx)) })
	})
	assert.NotContains(t, strings.ToLower(notice.String()+stderr), "write succeeded")
}
