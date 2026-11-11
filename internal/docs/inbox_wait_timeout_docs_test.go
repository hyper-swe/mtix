// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// MTIX-245: every copy of the inbox wait guidance states that --timeout is
// wall-clock time that survives sleep, and how the idle wait polls.
func TestInboxWaitTimeoutDocs_AllCopiesStateWallClockSemantics(t *testing.T) {
	paths := []string{
		"internal/docs/templates/skills/multi_agent.md.tmpl",
		".claude-plugin/skills/mtix-multi-agent.md",
		".codex-plugin/skills/multi-agent/SKILL.md",
		"internal/docs/templates/skill.md.tmpl",
		"docs/SKILL.md",
		"USERMANUAL.md",
	}
	for _, path := range paths {
		raw, err := os.ReadFile(filepath.Join("..", "..", path))
		require.NoError(t, err)
		text := strings.Join(strings.Fields(string(raw)), " ")
		for _, phrase := range []string{
			"The timeout is wall-clock time, not run time",
			"a sleeping machine does not stretch it",
			"exits 5 within about one tick of the deadline",
			"at least every 2 seconds",
		} {
			require.Contains(t, text, phrase, path)
		}
	}
}
