// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hyper-swe/mtix/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestApprovals_LegacyRecordInput_RecordsDecisionOnce(t *testing.T) {
	for _, input := range []string{"regular", "symlink", "directory", "size"} {
		t.Run(input, func(t *testing.T) {
			dir, _ := setupExecHook(t, "#!/bin/sh\nexit 0\n")
			pin := ConfigHash(dir)
			path := filepath.Join(dir, "hooks.trust")
			writeLegacyInput(t, path, pin, input)
			state := testutil.OperatorState(t)
			a := NewApprovals(dir, state, false)
			require.NoError(t, a.Migrate())
			decisions, err := a.Check()
			require.NoError(t, err)
			require.Equal(t, input == "regular", decisions["wake"].Trusted)
			project, err := projectIdentity(dir)
			require.NoError(t, err)
			var first migrationRecord
			reports, err := state.ReadJSON(recordKey("migration-", project, ""), &first)
			require.NoError(t, err)
			require.Empty(t, reports)
			require.True(t, first.Attempted)
			if input != "regular" {
				require.Empty(t, first.Pins)
			}
			require.NoError(t, os.Remove(path))
			require.NoError(t, os.WriteFile(path, []byte(pin+"\n"), 0600))
			require.NoError(t, a.Migrate())
			var second migrationRecord
			reports, err = state.ReadJSON(recordKey("migration-", project, ""), &second)
			require.NoError(t, err)
			require.Empty(t, reports)
			require.Equal(t, first, second)
			decisions, err = a.Check()
			require.NoError(t, err)
			require.Equal(t, input == "regular", decisions["wake"].Trusted)
		})
	}
}

func writeLegacyInput(t *testing.T, path, pin, input string) {
	t.Helper()
	switch input {
	case "regular":
		require.NoError(t, os.WriteFile(path, []byte(pin+"\n"), 0600))
	case "symlink":
		target := filepath.Join(t.TempDir(), "record")
		require.NoError(t, os.WriteFile(target, []byte(pin+"\n"), 0600))
		require.NoError(t, os.Symlink(target, path))
	case "directory":
		require.NoError(t, os.Mkdir(path, 0700))
	case "size":
		require.NoError(t, os.WriteFile(path, []byte(pin+strings.Repeat(" ", 65536)), 0600))
	}
}
