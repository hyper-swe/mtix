// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
)

// TestBoardHubFinding_LegacyClosedAtSkew_OnlyOnEvidenceWithinOneSecond pins
// when a closed_at difference is the clock skew of a plain old terminal event
// (MTIX-112.17.16.19.1) and when it stays a difference.
func TestBoardHubFinding_LegacyClosedAtSkew_OnlyOnEvidenceWithinOneSecond(t *testing.T) {
	node := func(status, closed string) string {
		c := ""
		if closed != "" {
			c = `,"closed_at":"` + closed + `"`
		}
		return `{"nodes":[{"id":"TEST-1","status":"` + status + `"` + c + `}]}`
	}
	const at, next, later = "2026-01-01T00:00:05Z", "2026-01-01T00:00:06Z", "2026-01-01T00:00:07Z"
	evidence := func(stamp string) *service.CloneEvidence {
		return &service.CloneEvidence{LegacyClosedAt: map[string]string{"TEST-1": stamp}}
	}
	tests := []struct {
		name      string
		local     string
		hub       string
		ev        *service.CloneEvidence
		wantEqual bool
	}{
		{"hub one second later, legacy", node("done", at), node("done", next), evidence(next), true},
		{"hub one second earlier, legacy", node("done", next), node("done", at), evidence(at), true},
		{"cancelled, legacy", node("cancelled", at), node("cancelled", next), evidence(next), true},
		{"no evidence at all", node("done", at), node("done", next), nil, false},
		{"empty evidence", node("done", at), node("done", next), &service.CloneEvidence{}, false},
		{"evidence for another stamp", node("done", at), node("done", next), evidence(later), false},
		{"two seconds apart, legacy", node("done", at), node("done", later), evidence(later), false},
		{"hub two seconds earlier, legacy", node("done", later), node("done", at), evidence(at), false},
		{"hub just over one second later", node("done", at), node("done", "2026-01-01T00:00:06.5Z"), evidence("2026-01-01T00:00:06.5Z"), false},
		{"different status", node("done", at), node("cancelled", next), evidence(next), false},
		{"non-terminal status", node("open", at), node("open", next), evidence(next), false},
		{"stamp missing on the store", node("done", ""), node("done", next), evidence(next), false},
		{"stamp missing on the hub", node("done", at), node("done", ""), evidence(next), false},
		{"unparseable stamp", node("done", "soon"), node("done", next), evidence(next), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, err := service.BoardHubFinding([]byte(tt.local), []byte(tt.hub), tt.ev)
			require.NoError(t, err)
			if tt.wantEqual {
				assert.Nil(t, f)
				return
			}
			require.NotNil(t, f)
			assert.Contains(t, f.Diff["TEST-1"], "closed_at")
		})
	}
}

// The evidence names a node; a different node with the same stamps is not
// excused by it, and the raw comparison never uses evidence.
func TestBoardHubFinding_LegacyClosedAtSkew_IsPerNodeAndNotInRawComparison(t *testing.T) {
	local := `{"nodes":[{"id":"TEST-2","status":"done","closed_at":"2026-01-01T00:00:05Z"}]}`
	hub := `{"nodes":[{"id":"TEST-2","status":"done","closed_at":"2026-01-01T00:00:06Z"}]}`
	ev := &service.CloneEvidence{LegacyClosedAt: map[string]string{"TEST-1": "2026-01-01T00:00:06Z"}}
	f, err := service.BoardHubFinding([]byte(local), []byte(hub), ev)
	require.NoError(t, err)
	require.NotNil(t, f)
	raw, err := service.CompareBoardDocs([]byte(local), []byte(hub))
	require.NoError(t, err)
	assert.Equal(t, service.BoardDiff{"TEST-2": {"closed_at"}}, raw)
}

// The lost-local check and the snapshot rule use the same evidence.
func TestFindLostLocalAndSnapshot_LegacyClosedAtSkew_UseEvidence(t *testing.T) {
	local := `{"nodes":[{"id":"TEST-1","status":"done","closed_at":"2026-01-01T00:00:05Z"}]}`
	hub := `{"nodes":[{"id":"TEST-1","status":"done","closed_at":"2026-01-01T00:00:06Z"}]}`
	ev := &service.CloneEvidence{LegacyClosedAt: map[string]string{"TEST-1": "2026-01-01T00:00:06Z"}}
	lost, err := service.FindLostLocal([]byte(local), []byte(hub), []string{"TEST-1"}, ev)
	require.NoError(t, err)
	assert.Nil(t, lost)
	lost, err = service.FindLostLocal([]byte(local), []byte(hub), []string{"TEST-1"}, nil)
	require.NoError(t, err)
	assert.NotNil(t, lost)
	snap, err := service.BoardSnapshotHubFinding([]byte(local), []byte(hub), ev)
	require.NoError(t, err)
	assert.Nil(t, snap)
	snap, err = service.BoardSnapshotHubFinding([]byte(local), []byte(hub), nil)
	require.NoError(t, err)
	assert.NotNil(t, snap)
}
