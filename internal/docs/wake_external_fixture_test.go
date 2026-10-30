// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// External command fixtures verify PATH selection independently of shell builtins.
package docs

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/testutil/hostguard"
)

func installWakeRecorders(t *testing.T, dir string) {
	t.Helper()
	for _, name := range []string{"printf", "env", "xargs"} {
		body := "#!/bin/sh\nprintf 'called\\n' >> \"$WAKE_DIR/external-" + name + "\"\n"
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o700))
	}
}

func assertWakeRecordersUnused(t *testing.T, dir string) {
	t.Helper()
	for _, name := range []string{"printf", "env", "xargs"} {
		_, err := os.Stat(filepath.Join(dir, "external-"+name))
		require.True(t, os.IsNotExist(err), "external %s invoked", name)
	}
}

func TestWakeFixture_ExternalCommands_RecordInvocation(t *testing.T) {
	got := runWakeFixture(t, wakeSource(t, "examples/hooks/wake-agent.sh"), "fixture input")
	for _, name := range []string{"printf", "env", "xargs"} {
		t.Run(name, func(t *testing.T) {
			_, err := os.Stat(filepath.Join(got.dir, name))
			require.NoError(t, err)
			cmd := exec.CommandContext(t.Context(), "/usr/bin/env", name, "fixture-operand")
			cmd.Env = append(hostguard.Environ(), "PATH="+got.dir+":/usr/bin:/bin", "WAKE_DIR="+got.dir)
			output, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", output)
			marker, err := os.ReadFile(filepath.Join(got.dir, "external-"+name))
			require.NoError(t, err)
			require.Equal(t, "called\n", string(marker))
		})
	}
}
