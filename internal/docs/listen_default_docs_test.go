// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
)

// TestListenDefault_DocsStateTheConstant keeps the shipped sync.listen
// default in the docs equal to service.DefaultListenHold (MTIX-112.20.1,
// founder question Q1): flipping the constant must fail here until the
// checked-in docs are regenerated.
func TestListenDefault_DocsStateTheConstant(t *testing.T) {
	want := "shipped default: `" + service.DefaultListenValue() + "`"
	for _, path := range []string{
		"../../.claude-plugin/skills/mtix-sync.md",
		"../../.codex-plugin/skills/sync/SKILL.md",
	} {
		body, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Contains(t, string(body), want, path)
	}
	body, err := os.ReadFile("../../USERMANUAL.md")
	require.NoError(t, err)
	require.Contains(t, string(body), want, "USERMANUAL.md")
	req, err := os.ReadFile("../../REQUIREMENTS.md")
	require.NoError(t, err)
	require.Contains(t, string(req), "listen: "+service.DefaultListenValue()+" ", "REQUIREMENTS.md FR-11.2")
	for _, path := range []string{
		"templates/skills/sync.md.tmpl", "templates/skills/multi_agent.md.tmpl",
		"templates/claude.md.tmpl", "templates/agents.md.tmpl",
	} {
		body, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Contains(t, string(body), "shipped default: `{{ .ListenDefault }}`", path)
	}
}
