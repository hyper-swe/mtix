// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package format

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// honouringProbeShell is shared by the probe and its operand regression test.
func honouringProbeShell(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sh-honours-n")
	// Parse from stdin so this fake honours -n even when the host's -n -c does not.
	require.NoError(t, os.WriteFile(path, []byte(`#!/bin/sh
[ "$#" -eq 3 ] && [ "$1" = "-n" ] && [ "$2" = "-c" ] && [ "$3" = "exit 7" ] || exit 64
printf '%s\n' "$3" | /bin/sh -n
`), 0o700))
	return path
}

func TestShellHonoursSyntaxOnly_Shells_ReportsSupport(t *testing.T) {
	dir := t.TempDir()
	ignoring := filepath.Join(dir, "sh-ignores-n")
	// Mimics an older dash that drops -n when combined with -c.
	require.NoError(t, os.WriteFile(ignoring, []byte("#!/bin/sh\nshift 2\nexec /bin/sh -c \"$1\"\n"), 0o700))
	honouring := honouringProbeShell(t)

	tests := []struct {
		name  string
		shell string
		want  bool
	}{
		{"shell that honours -n", honouring, true},
		{"shell that ignores -n", ignoring, false},
		{"missing shell", filepath.Join(dir, "absent"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, shellHonoursSyntaxOnly(t, tc.shell))
		})
	}
}

func TestHonouringProbeShell_ProbeOperand_RejectsBenignCommand(t *testing.T) {
	shell := honouringProbeShell(t)
	tests := []struct {
		name     string
		operand  string
		wantExit int
	}{
		{"expected probe operand", "exit 7", 0},
		{"benign operand", "exit 0", 64},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := exec.CommandContext(t.Context(), shell, "-n", "-c", tc.operand).Run()
			if tc.wantExit == 0 {
				require.NoError(t, err)
				return
			}
			var exitError *exec.ExitError
			require.ErrorAs(t, err, &exitError)
			require.Equal(t, tc.wantExit, exitError.ExitCode())
		})
	}
}
