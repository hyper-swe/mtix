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

func TestLearnedLaneGuidance_IdentityOrderAndDeferredDrain_MatchesAllSurfaces(t *testing.T) {
	paths := []string{"internal/docs/templates/skills/multi_agent.md.tmpl", ".claude-plugin/skills/mtix-multi-agent.md", ".codex-plugin/skills/multi-agent/SKILL.md", "USERMANUAL.md"}
	var shared string
	for _, path := range paths {
		raw, err := os.ReadFile(filepath.Join("../..", path))
		require.NoError(t, err)
		text := string(raw)
		start := strings.Index(text, "<!-- learned-lane-identity:start -->")
		end := strings.Index(text, "<!-- learned-lane-identity:end -->")
		require.GreaterOrEqual(t, start, 0, path)
		require.Greater(t, end, start, path)
		block := text[start:end]
		for _, term := range []string{"MTIX_AGENT_ID", "resumed session handle", "exactly one id was ever learned", "unknown liveness", "matching back-link", "grants nothing", "DrainAttachPending", "wiring is deferred"} {
			require.Contains(t, block, term, path)
		}
		if shared == "" {
			shared = block
		} else {
			require.Equal(t, shared, block, path)
		}
	}
}
