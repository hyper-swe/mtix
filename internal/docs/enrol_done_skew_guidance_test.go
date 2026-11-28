// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// MTIX-112.17.16.19.1: every shipped sync runbook says that enrollment accepts
// the one-second completion-time skew of a plain old done, its limit, and that
// an event carrying a closed_at snapshot is never excused.
func TestEnrolDoneSkewGuidance_AllSyncSurfaces(t *testing.T) {
	paths := []string{
		"templates/skills/sync.md.tmpl",
		"../../.claude-plugin/skills/mtix-sync.md",
		"../../.codex-plugin/skills/sync/SKILL.md",
		"../../USERMANUAL.md",
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			body, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Contains(t, string(body), "same terminal status, and the two times are at most one second apart. It")
			require.Contains(t, string(body), "cannot tell this skew from a real change of one second or less to the same")
			// MTIX-112.17.16.32: the silent-by-design rule and its snapshot limit.
			require.Contains(t, string(body), "An event that carries a `closed_at` snapshot is never excused, even when the gap is one second or less.")
		})
	}
}
