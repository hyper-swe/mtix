// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// These tests verify approval decisions against a dispatch configuration.
package service

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/hooks"
	"github.com/hyper-swe/mtix/internal/testutil"
)

func TestHookDecisions_ConfigInput_RequiresMatchingStanza(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".mtix")
	require.NoError(t, os.Mkdir(dir, 0700))
	path := filepath.Join(dir, "hooks.yaml")
	config := "hooks:\n  - name: wake\n    match: {events: [comment.addressed]}\n    deliver: [exec]\n    exec: {command: [/bin/true]}\n"
	require.NoError(t, os.WriteFile(path, []byte(config), 0600))
	previous, _ := hooks.Load(dir)
	config += "    include-synced: true\n"
	require.NoError(t, os.WriteFile(path, []byte(config), 0600))
	approvals := hooks.NewApprovals(dir, testutil.OperatorState(t), false)
	require.NoError(t, approvals.Approve("wake", true))
	d := NewHooksDispatcher(nil, dir, slog.Default())
	d.SetApprovals(approvals)
	require.False(t, d.hookDecisions(previous)["wake"].Trusted)
	current, _ := hooks.Load(dir)
	require.True(t, d.hookDecisions(current)["wake"].Trusted)
}
