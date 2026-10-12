// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/hyper-swe/mtix/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestApprovals_Migration_RecordsFirstEligibleDecision(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "matching", true: "changed"}[changed], func(t *testing.T) {
			dir, script := setupExecHook(t, "#!/bin/sh\nexit 0\n")
			pin := ConfigHash(dir)
			require.NoError(t, os.WriteFile(filepath.Join(dir, "hooks.trust"), []byte(pin), 0600))
			if changed {
				require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nexit 1\n"), 0700))
			}
			state := testutil.OperatorState(t)
			a := NewApprovals(dir, state, false)
			require.NoError(t, a.Migrate())
			decisions, err := a.Check()
			require.NoError(t, err)
			require.Equal(t, !changed, decisions["wake"].Trusted)
			require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0700))
			require.NoError(t, a.Migrate())
			decisions, err = a.Check()
			require.NoError(t, err)
			require.Equal(t, !changed, decisions["wake"].Trusted)
		})
	}
}

func TestApprovals_SessionInput_DefersMigrationAndKeepsApproval(t *testing.T) {
	dir, _ := setupExecHook(t, "#!/bin/sh\nexit 0\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hooks.trust"), []byte(ConfigHash(dir)), 0600))
	state := testutil.OperatorState(t)
	marked := NewApprovals(dir, state, true)
	require.NoError(t, marked.Migrate())
	status, err := state.Inspect()
	require.NoError(t, err)
	require.NotEqual(t, "ready", status.Status)
	require.Error(t, marked.Approve("wake", true))
	ordinary := NewApprovals(dir, state, false)
	require.NoError(t, ordinary.Approve("wake", true))
	decisions, err := marked.Check()
	require.NoError(t, err)
	require.True(t, decisions["wake"].Trusted)
}

func TestApprovals_ExplicitHostInput_DoesNotUseMigrationFallback(t *testing.T) {
	dir, _ := setupExecHook(t, "#!/bin/sh\nexit 0\n")
	state := testutil.OperatorState(t)
	a := NewApprovals(dir, state, false)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hooks.trust"), []byte(ConfigHash(dir)), 0600))
	require.NoError(t, a.Migrate())
	require.NoError(t, a.Approve("wake", true))
	project, err := projectIdentity(dir)
	require.NoError(t, err)
	path, err := state.Path()
	require.NoError(t, err)
	record := filepath.Join(path, recordKey("hook-", project, "wake")+".json")
	raw, err := os.ReadFile(record)
	require.NoError(t, err)
	var envelope map[string]any
	require.NoError(t, json.Unmarshal(raw, &envelope))
	envelope["host_id"] = strings.Repeat("a", 32)
	raw, err = json.Marshal(envelope)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(record, raw, 0600))
	decisions, err := a.Check()
	require.NoError(t, err)
	require.False(t, decisions["wake"].Trusted)
	require.Contains(t, decisions["wake"].Detail, "another host")
}

func TestApprovals_ExternalScriptInput_DoesNotChangeOwnDecision(t *testing.T) {
	dir, _ := setupExecHook(t, "#!/bin/sh\nexit 0\n")
	external := filepath.Join(t.TempDir(), "interpreter")
	require.NoError(t, os.WriteFile(external, []byte("first"), 0700))
	raw, err := os.ReadFile(filepath.Join(dir, "hooks.yaml"))
	require.NoError(t, err)
	raw = []byte(strings.ReplaceAll(string(raw), `["wake.sh"]`, `["`+external+`", "wake.sh"]`))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hooks.yaml"), raw, 0600))
	a := NewApprovals(dir, testutil.OperatorState(t), false)
	require.NoError(t, a.Approve("wake", true))
	require.NoError(t, os.WriteFile(external, []byte("second"), 0700))
	decisions, err := a.Check()
	require.NoError(t, err)
	require.True(t, decisions["wake"].Trusted)
}

func TestApprovals_Migration_ConcurrentCallsKeepFirstRecord(t *testing.T) {
	dir, _ := setupExecHook(t, "#!/bin/sh\nexit 0\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hooks.trust"), []byte(ConfigHash(dir)), 0600))
	state := testutil.OperatorState(t)
	a := NewApprovals(dir, state, false)
	var group sync.WaitGroup
	reports := make(chan error, 8)
	for range 8 {
		group.Add(1)
		go func() { defer group.Done(); reports <- a.Migrate() }()
	}
	group.Wait()
	close(reports)
	for err := range reports {
		require.NoError(t, err)
	}
	decisions, err := a.Check()
	require.NoError(t, err)
	require.True(t, decisions["wake"].Trusted)
}

func TestTrustFix_HookName_QuotesInput(t *testing.T) {
	require.Equal(t, `mtix hooks trust -- '-wake'`, TrustFix("-wake"))
	require.Equal(t, `mtix hooks trust -- 'wake'"'"'s'`, TrustFix("wake's"))
}

func TestApprovals_NoExecInput_SkipsOperatorState(t *testing.T) {
	dir, _ := setupExecHook(t, "#!/bin/sh\nexit 0\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hooks.yaml"), []byte("hooks: []\n"), 0600))
	a := NewApprovals(dir, nil, false)
	require.NoError(t, a.Migrate())
	decisions, err := a.Check()
	require.NoError(t, err)
	require.Empty(t, decisions)
}

func TestSessionMarked_ProcessInputs_ReportEligibility(t *testing.T) {
	markers := []string{"CODEX_THREAD_ID", "CODEX_SANDBOX", "CODEX_SANDBOX_NETWORK_DISABLED", "CLAUDECODE", "CLAUDE_CODE_MESSAGING_SOCKET", "CLAUDE_ENV_FILE", "PI_SESSION_ID", "PI_SESSION_FILE", "PI_CODING_AGENT"}
	for _, key := range markers {
		t.Setenv(key, "")
	}
	require.False(t, SessionMarked())
	for _, key := range markers {
		t.Setenv(key, "present")
		require.True(t, SessionMarked())
		t.Setenv(key, "")
	}
}
