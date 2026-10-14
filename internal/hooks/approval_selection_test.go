// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hooks

import (
	"crypto/sha1"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/hyper-swe/mtix/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestApprovals_HookInput_IndependentDecisions(t *testing.T) {
	for _, input := range []string{"path", "record"} {
		t.Run(input, func(t *testing.T) {
			dir, script := setupExecHook(t, "#!/bin/sh\nexit 0\n")
			raw, err := os.ReadFile(filepath.Join(dir, "hooks.yaml"))
			require.NoError(t, err)
			raw = append(raw, []byte("  - name: other\n    match: {events: [comment.addressed]}\n    deliver: [exec]\n    exec: {command: [/bin/true]}\n")...)
			require.NoError(t, os.WriteFile(filepath.Join(dir, "hooks.yaml"), raw, 0600))
			state := testutil.OperatorState(t)
			a := NewApprovals(dir, state, false)
			require.NoError(t, a.Approve("wake", true))
			require.NoError(t, a.Approve("other", true))
			if input == "path" {
				replacement := filepath.Join(t.TempDir(), "replacement.sh")
				require.NoError(t, os.WriteFile(replacement, []byte("#!/bin/sh\nexit 0\n"), 0700))
				require.NoError(t, os.Remove(script))
				require.NoError(t, os.Symlink(replacement, script))
			} else {
				project, err := projectIdentity(dir)
				require.NoError(t, err)
				require.NoError(t, state.WriteJSON(recordKey("hook-", project, "wake"), map[string]any{}))
			}
			decisions, _ := a.Check()
			require.False(t, decisions["wake"].Trusted)
			require.True(t, decisions["other"].Trusted)
			require.NoError(t, a.Approve("other", true))
		})
	}
}

func TestApprovals_AncestorIndex_ReportsTrackedInput(t *testing.T) {
	dir, _ := setupExecHook(t, "#!/bin/sh\nexit 0\n")
	root := filepath.Dir(dir)
	ancestor := filepath.Dir(root)
	gitdir := filepath.Join(ancestor, ".git")
	require.NoError(t, os.Mkdir(gitdir, 0700))
	rel, err := filepath.Rel(ancestor, filepath.Join(dir, "hooks.trust"))
	require.NoError(t, err)
	data := make([]byte, 12)
	copy(data, "DIRC")
	binary.BigEndian.PutUint32(data[4:8], 2)
	binary.BigEndian.PutUint32(data[8:12], 1)
	entry := make([]byte, 62)
	name := filepath.ToSlash(rel)
	binary.BigEndian.PutUint16(entry[60:62], uint16(len(name)))
	entry = append(entry, []byte(name)...)
	entry = append(entry, 0)
	for len(entry)%8 != 0 {
		entry = append(entry, 0)
	}
	data = append(data, entry...)
	sum := sha1.Sum(data)
	data = append(data, sum[:]...)
	require.NoError(t, os.WriteFile(filepath.Join(gitdir, "index"), data, 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hooks.trust"), []byte(ConfigHash(dir)), 0600))
	a := NewApprovals(dir, testutil.OperatorState(t), false)
	require.NoError(t, a.Migrate())
	decisions, err := a.Check()
	require.NoError(t, err)
	require.False(t, decisions["wake"].Trusted)
	require.Contains(t, decisions["wake"].Detail, "tracked")
}

func TestApprovals_SelectedStanza_RequiresCurrentInput(t *testing.T) {
	dir, _ := setupExecHook(t, "#!/bin/sh\nexit 0\n")
	cfg, _ := Load(dir)
	selected := cfg.Hooks[0]
	state := testutil.OperatorState(t)
	a := NewApprovals(dir, state, false)
	raw, err := os.ReadFile(filepath.Join(dir, "hooks.yaml"))
	require.NoError(t, err)
	raw = append(raw, []byte("    include-synced: true\n")...)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hooks.yaml"), raw, 0600))
	_, err = a.ApproveSelected(selected, true)
	require.Error(t, err)
	decisions, err := a.Check()
	require.NoError(t, err)
	require.False(t, decisions["wake"].Trusted)
	current, _ := Load(dir)
	_, err = a.ApproveSelected(current.Hooks[0], true)
	require.NoError(t, err)
	decisions, err = a.Check()
	require.NoError(t, err)
	require.True(t, decisions["wake"].Trusted)
}

func TestApprovals_ProjectPath_RequiresDirectComponents(t *testing.T) {
	dir, script := setupExecHook(t, "#!/bin/sh\nexit 0\n")
	require.NoError(t, os.Remove(script))
	require.NoError(t, os.Symlink(filepath.Join(t.TempDir(), "absent"), script))
	a := NewApprovals(dir, testutil.OperatorState(t), false)
	require.Error(t, a.Approve("wake", true))
}
