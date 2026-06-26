// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package format

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// TestProvisionalMarker_NonEmpty verifies the marker is a visible, non-empty
// token so a provisional id is distinguishable in plain-text output
// (ADR-003 §8).
func TestProvisionalMarker_NonEmpty(t *testing.T) {
	assert.NotEmpty(t, strings.TrimSpace(ProvisionalMarker))
}

// TestAnnotateID_SettledNoMarker verifies a settled (fully-numeric) id is
// returned unchanged — CORNER: settled id shows no marker (ADR-003 §8).
func TestAnnotateID_SettledNoMarker(t *testing.T) {
	for _, id := range []string{"PRJX-1", "PRJX-1.4", "PRJX-1.2.3", "MTIX-30.12"} {
		assert.Equal(t, id, AnnotateID(id),
			"settled id %q must be returned unchanged", id)
	}
}

// TestAnnotateID_NonNodeNoMarker verifies a string with no recognizable
// PREFIX-<root> shape is returned unchanged — EDGE: detection is purely
// shape-based, so a string with no prefix-dash is never a node id (ADR-003 §8).
func TestAnnotateID_NonNodeNoMarker(t *testing.T) {
	for _, s := range []string{"", "free text", "PRJX"} {
		assert.Equal(t, s, AnnotateID(s),
			"non-node string %q must be returned unchanged", s)
	}
}

// TestAnnotateID_ProvisionalGetsMarker verifies provisional ids — single-level
// and deeply-nested — gain the visible marker (CORNER/EDGE, ADR-003 §8).
func TestAnnotateID_ProvisionalGetsMarker(t *testing.T) {
	cases := []struct {
		name string
		id   string
	}{
		{"single-level", "PRJX-1.u0a1b2c3d4e5"},
		{"deeply-nested", "PRJX-1.u0a1b2c3d4e5.2.3"},
		{"nested-uid-mid-path", "PRJX-1.2.u0a1b2c3d4e5.4"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := AnnotateID(tc.id)
			assert.True(t, strings.HasPrefix(got, tc.id),
				"annotated id must keep the original id verbatim")
			assert.Contains(t, got, ProvisionalMarker,
				"provisional id %q must carry the marker", tc.id)
		})
	}
}

// TestCheckExternalizable_SettledPasses verifies a settled id is allowed to be
// externalized — CORNER: settled id passes the guardrail (ADR-003 §8).
func TestCheckExternalizable_SettledPasses(t *testing.T) {
	require.NoError(t, CheckExternalizable("PRJX-1.4", "PRJX-2", "MTIX-30.12"))
}

// TestCheckExternalizable_NonNodePasses verifies non-node strings do not trip
// the guardrail — EDGE: only true node-id shapes are gated (ADR-003 §8).
func TestCheckExternalizable_NonNodePasses(t *testing.T) {
	require.NoError(t, CheckExternalizable("", "just a message", "PRJX"))
}

// TestCheckExternalizable_ProvisionalRefused verifies a provisional id is
// refused before externalization, detectable from the string alone — CORNER:
// provisional id flagged before externalization (ADR-003 §8).
func TestCheckExternalizable_ProvisionalRefused(t *testing.T) {
	err := CheckExternalizable("PRJX-1.4", "PRJX-1.u0a1b2c3d4e5")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PRJX-1.u0a1b2c3d4e5",
		"error must name the offending provisional id")
}

// TestCheckExternalizable_DeeplyNestedRefused verifies a deeply-nested
// provisional id is also refused — EDGE: deeply-nested provisional (ADR-003 §8).
func TestCheckExternalizable_DeeplyNestedRefused(t *testing.T) {
	err := CheckExternalizable("PRJX-1.2.u0a1b2c3d4e5.4")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PRJX-1.2.u0a1b2c3d4e5.4")
}

// TestCheckExternalizable_ReportsAllProvisional verifies every offending id is
// reported, not just the first — EDGE: multiple provisional ids (ADR-003 §8).
func TestCheckExternalizable_ReportsAllProvisional(t *testing.T) {
	err := CheckExternalizable("PRJX-1.uaaaaaaaaaaaa", "PRJX-2.ubbbbbbbbbbbb")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PRJX-1.uaaaaaaaaaaaa")
	assert.Contains(t, err.Error(), "PRJX-2.ubbbbbbbbbbbb")
}

// TestRenderBriefing_ProvisionalIDMarked verifies the briefing ID field carries
// the provisional marker for a provisional node and stays clean for a settled
// one — the CLI/MCP briefing surface flags provisional ids (ADR-003 §8).
func TestRenderBriefing_ProvisionalIDMarked(t *testing.T) {
	settled := makeBriefingNode("PROJ-1", "settled", "", "", "")
	provisional := makeBriefingNode("PROJ-1.u0a1b2c3d4e5", "provisional", "", "", "")

	var buf bytes.Buffer
	require.NoError(t, RenderBriefing(&buf, []*model.Node{settled, provisional}, BriefingOpts{}))
	out := buf.String()

	assert.Contains(t, out, "ID: PROJ-1.u0a1b2c3d4e5"+ProvisionalMarker,
		"provisional node id must be marked in the briefing")
	assert.Contains(t, out, "ID: PROJ-1\n",
		"settled node id must not be marked in the briefing")
}
