// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// These tests verify per-hook approval records and their input boundaries.
package hooks

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/testutil"
)

func TestApprovals_ScriptEdit_ChangesDecision(t *testing.T) {
	dir, script := setupExecHook(t, "#!/bin/sh\nexit 0\n")
	a := NewApprovals(dir, testutil.OperatorState(t), false)
	require.NoError(t, a.Approve("wake", true))
	status, err := a.Check()
	require.NoError(t, err)
	require.True(t, status["wake"].Trusted)
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nexit 1\n"), 0700))
	status, err = a.Check()
	require.NoError(t, err)
	require.False(t, status["wake"].Trusted)
}

func TestApprovals_ExplicitRecordInput_RequiresValidFields(t *testing.T) {
	for _, value := range []any{map[string]any{}, nil} {
		t.Run(fmt.Sprintf("%T", value), func(t *testing.T) {
			dir, _ := setupExecHook(t, "#!/bin/sh\nexit 0\n")
			state := testutil.OperatorState(t)
			a := NewApprovals(dir, state, false)
			require.NoError(t, os.WriteFile(filepath.Join(dir, "hooks.trust"), []byte(ConfigHash(dir)+"\n"), 0600))
			require.NoError(t, a.Migrate())
			project, err := projectIdentity(dir)
			require.NoError(t, err)
			require.NoError(t, state.WriteJSON(recordKey("hook-", project, "wake"), value))
			decisions, err := a.Check()
			require.Error(t, err)
			require.False(t, decisions["wake"].Trusted)
		})
	}
}

func TestApprovals_GrantInputs_ReturnsError(t *testing.T) {
	for _, tc := range []struct {
		name              string
		terminal, session bool
	}{
		{"pipe", false, false}, {"session", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, _ := setupExecHook(t, "#!/bin/sh\nexit 0\n")
			a := NewApprovals(dir, testutil.OperatorState(t), tc.session)
			require.Error(t, a.Approve("wake", tc.terminal))
			status, err := a.Check()
			require.NoError(t, err)
			require.False(t, status["wake"].Trusted)
		})
	}
}
