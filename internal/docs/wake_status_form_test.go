// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// The wake-guidance scan approves the documented inbox status form (MTIX-112.40.6).
package docs

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWakeExample_InboxStatusForm_IsApprovedNonReader(t *testing.T) {
	forms := []string{
		`mtix inbox status --agent <agent>`,
		`mtix inbox status --agent <agent> [--json] [--limit N]`,
		`mtix inbox status --agent worker-1 --json`,
		`mtix inbox status --agent worker-1 --limit 5`,
		`mtix inbox status --agent worker-1 --json --limit 5`,
		`/usr/local/bin/mtix inbox status --agent worker-1`,
	}
	for _, form := range forms {
		t.Run(form, func(t *testing.T) {
			require.False(t, wakeReaderSource(form), "status form reads no events")
			require.Empty(t, wakeLaunchProblems("Run `"+form+"` to see it.", true), "documented span")
		})
	}
}

func TestWakeExample_InboxStatusForm_KeepsEveryReaderRefused(t *testing.T) {
	forms := []string{
		`mtix inbox --agent worker-1`,
		`mtix inbox --agent worker-1 --format prompt`,
		`mtix inbox status --agent worker-1 --format prompt`,
		`mtix inbox status --agent worker-1 --unknown`,
		`mtix inbox status`,
		`mtix inbox status --agent "$AGENT" --json`,
		`mtix inbox status --agent worker-1; mtix inbox --agent worker-1`,
		`$M inbox status; $M inbox`,
		`mtix inbox status --agent worker-1 && mtix inbox`,
		`mtix inbox status --agent worker-1 | claude -p`,
		`mtix inbox status --agent worker-1 $(mtix inbox --agent worker-1)`,
		`echo inbox status`,
		`mtix inbox statusx --agent worker-1`,
		`mtix inbox --agent w; mtix inbox status --agent w`,
		`mtix inbox --agent w && mtix inbox status --agent w`,
		`mtix inbox --agent w | mtix inbox status --agent w`,
		`$(mtix inbox --agent w) mtix inbox status --agent w`,
		"`mtix inbox --agent w` mtix inbox status --agent w",
		"mtix\tinbox status --agent w",
		"mtix inbox status --agent w \\\n--format prompt",
		"mtix inbox status --agent w\nmtix inbox --agent w",
		`mtix inbox status --agent w --limit 5 --json`,
		`$MTIX_BIN inbox status --agent w`,
	}
	for _, form := range forms {
		t.Run(form, func(t *testing.T) {
			require.True(t, wakeReaderSource(form), "reader shape stays refused")
		})
	}
}
