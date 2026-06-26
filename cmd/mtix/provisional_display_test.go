// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/format"
)

// TestGeneratedAgentsGuide_HasNodeIDSemantics verifies the generated AGENTS.md
// teaches dot-path-only references and the provisional-vs-settled
// externalization rule, without leaking internal uid mechanics (ADR-003 §8,
// §14).
func TestGeneratedAgentsGuide_HasNodeIDSemantics(t *testing.T) {
	initTestApp(t)
	_ = captureRunPluginInstall(t, "codex")

	cwd, err := os.Getwd()
	require.NoError(t, err)
	body, err := os.ReadFile(filepath.Join(cwd, "AGENTS.md"))
	require.NoError(t, err)
	guide := string(body)

	assert.Contains(t, guide, "Node ID semantics",
		"generated guide must have a Node ID semantics section")
	assert.Contains(t, guide, "dot-path",
		"guide must instruct agents to reference nodes by dot-path")
	assert.Contains(t, strings.ToLower(guide), "provisional",
		"guide must explain provisional ids")
	assert.Contains(t, strings.ToLower(guide), "settled",
		"guide must explain settled ids are safe to externalize")
	assert.Contains(t, strings.ToLower(guide), "re-resolve",
		"guide must tell agents to re-resolve after a renumber")
	// ADR-003 §14: the agent-facing doc must not leak internal uid mechanics.
	// (Matched case-sensitively against distinctive internal terms so ordinary
	// English like "guide" — which contains the substring "uid" — is not a hit.)
	for _, leak := range []string{"UID", "UUID", "event_id", "RenderUIDSegment", "create_node"} {
		assert.NotContains(t, guide, leak,
			"agent guide must not leak internal mechanic %q (ADR-003 §14)", leak)
	}
}

// TestFormatNodeRow_SettledID_NoMarker verifies the list table shows a settled
// id unchanged — CORNER: settled id shows no marker (ADR-003 §8).
func TestFormatNodeRow_SettledID_NoMarker(t *testing.T) {
	row := FormatNodeRow("PROJ-1.4", "open", 2, "Title", 0.5, false)
	assert.Equal(t, "PROJ-1.4", row[0])
	assert.NotContains(t, row[0], format.ProvisionalMarker)
}

// TestFormatNodeRow_ProvisionalID_Marked verifies the list table flags a
// provisional id, single-level and deeply-nested (CORNER/EDGE, ADR-003 §8).
func TestFormatNodeRow_ProvisionalID_Marked(t *testing.T) {
	for _, id := range []string{"PROJ-1.u0a1b2c3d4e5", "PROJ-1.u0a1b2c3d4e5.2.3"} {
		row := FormatNodeRow(id, "open", 2, "Title", 0.5, false)
		assert.True(t, strings.HasPrefix(row[0], id), "id must be preserved verbatim")
		assert.Contains(t, row[0], format.ProvisionalMarker,
			"provisional id %q must be marked in the list table", id)
	}
}

// TestTreeLine_SettledID_NoMarker verifies the tree view shows a settled id
// unchanged — CORNER: settled id shows no marker (ADR-003 §8).
func TestTreeLine_SettledID_NoMarker(t *testing.T) {
	line := TreeLine("PROJ-1.4", "open", "Title", 0.0, "", true, 0, false)
	assert.Contains(t, line, "PROJ-1.4")
	assert.NotContains(t, line, format.ProvisionalMarker)
}

// TestTreeLine_ProvisionalID_Marked verifies the tree view flags a provisional
// id — CORNER: single-level provisional shows the marker (ADR-003 §8).
func TestTreeLine_ProvisionalID_Marked(t *testing.T) {
	line := TreeLine("PROJ-1.u0a1b2c3d4e5", "open", "Title", 0.0, "", true, 0, false)
	assert.Contains(t, line, "PROJ-1.u0a1b2c3d4e5"+format.ProvisionalMarker)
}
