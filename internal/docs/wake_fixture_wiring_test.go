// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"os"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/testutil/hostguard"
)

// MTIX-112.26 exercises the actual fixture assertion in a child test process.
func TestWakeFixture_NormalLaunch_RejectsExternalCommands(t *testing.T) {
	if probe := os.Getenv("MTIX_WAKE_FIXTURE_PROBE"); probe != "" {
		prefix := probe + " fixture-operand\n"
		if probe == "builtin" {
			prefix = "printf '%s\\n' fixture-operand >/dev/null\n"
		}
		runWakeFixture(t, prefix+wakeSource(t, "examples/hooks/wake-agent.sh"), "fixture input")
		return
	}
	for _, tt := range []struct{ name, command string }{
		{"printf", "/usr/bin/env printf"}, {"env", "env"}, {"xargs", "xargs"}, {"builtin", "builtin"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestWakeFixture_NormalLaunch_RejectsExternalCommands$", "-test.count=1")
			cmd.Env = append(hostguard.Environ(), "MTIX_WAKE_FIXTURE_PROBE="+tt.command)
			output, err := cmd.CombinedOutput()
			if tt.name == "builtin" {
				require.NoError(t, err, "%s", output)
				return
			}
			require.Error(t, err, "%s", output)
			require.Contains(t, string(output), "external "+tt.name+" invoked")
			require.Contains(t, string(output), "--- FAIL: TestWakeFixture_NormalLaunch_RejectsExternalCommands")
		})
	}
}
