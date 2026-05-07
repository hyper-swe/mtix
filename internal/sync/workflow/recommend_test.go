// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Each state must produce at least one recommendation. Empty
// recommendations would defeat the tool's purpose; assert non-empty
// across the matrix.
func TestRecommendForState_AllStatesProduceRecommendations(t *testing.T) {
	for _, s := range []State{
		StateSolo,
		StateSyncConfiguredNoHub,
		StateSyncActive,
		StateDivergentPending,
		StateHubUnreachable,
	} {
		r := Report{State: s, StateName: s.String()}
		recs := RecommendForState(r)
		require.NotEmptyf(t, recs, "state %s produced no recommendations", s)
	}
}

func TestRecommendForState_Solo_SuggestsContinue(t *testing.T) {
	r := Report{State: StateSolo}
	recs := RecommendForState(r)
	require.NotEmpty(t, recs)
	require.Equal(t, SeverityInfo, recs[0].Severity)
	// First recommendation should be reassuring, not alarming.
	require.Contains(t, strings.ToLower(recs[0].Action), "continue")
}

func TestRecommendForState_SyncConfiguredNoHub_SuggestsInitOrClone(t *testing.T) {
	r := Report{State: StateSyncConfiguredNoHub, HasDSN: true}
	recs := RecommendForState(r)
	require.NotEmpty(t, recs)
	combined := strings.Join(actionsOf(recs), " ")
	require.Contains(t, combined, "mtix sync init")
	require.Contains(t, combined, "mtix sync clone")
}

func TestRecommendForState_SyncActive_QuiescentNoExtraNoise(t *testing.T) {
	r := Report{
		State:             StateSyncActive,
		HasDSN:            true,
		LocalEventCount:   5,
		AppliedEventCount: 10,
	}
	recs := RecommendForState(r)
	require.NotEmpty(t, recs)
	// Active state should be quiet — at most an info-level "continue" suggestion.
	for _, rec := range recs {
		require.NotEqual(t, SeverityRefuse, rec.Severity,
			"sync-active should never produce a refuse-level recommendation")
	}
}

func TestRecommendForState_DivergentPending_SuggestsListThenResolve(t *testing.T) {
	r := Report{State: StateDivergentPending, HasDSN: true, HasUnresolvedConflicts: true}
	recs := RecommendForState(r)
	require.NotEmpty(t, recs)
	combined := strings.Join(actionsOf(recs), " ")
	require.Contains(t, combined, "mtix sync conflicts list")
	// The agent should be told to surface the divergence prominently.
	require.Equal(t, SeverityWarn, recs[0].Severity)
}

func TestRecommendForState_HubUnreachable_SuggestsDoctor(t *testing.T) {
	r := Report{
		State:             StateHubUnreachable,
		HasDSN:            true,
		ConsecutiveErrors: 5,
	}
	recs := RecommendForState(r)
	require.NotEmpty(t, recs)
	combined := strings.Join(actionsOf(recs), " ")
	require.Contains(t, combined, "mtix sync doctor")
}

// FR-18 / MTIX-15.8.2 critical safety rule: never recommend
// sslmode=disable for any state.
func TestRecommend_NeverSuggestsSslmodeDisable(t *testing.T) {
	for _, s := range []State{
		StateSolo,
		StateSyncConfiguredNoHub,
		StateSyncActive,
		StateDivergentPending,
		StateHubUnreachable,
	} {
		r := Report{State: s, StateName: s.String(), HasDSN: true, ConsecutiveErrors: 5}
		for _, rec := range RecommendForState(r) {
			combined := rec.Action + " " + rec.Rationale
			require.NotContainsf(t, strings.ToLower(combined), "sslmode=disable",
				"state %s recommendation %q references sslmode=disable", s, rec.Action)
		}
	}
}

// FR-18 / MTIX-15.8.2 critical safety rule: never recommend
// committing a DSN to a tracked config file. Heuristic: no
// recommendation may mention both "config.yaml" (or .yml/.json)
// AND a DSN-shaped phrase.
func TestRecommend_NeverSuggestsCommittingDSN(t *testing.T) {
	tracked := []string{"config.yaml", "config.yml", "config.json"}
	dsnPhrases := []string{"postgres://", "postgresql://", "MTIX_SYNC_DSN", "DSN"}
	for _, s := range []State{
		StateSolo,
		StateSyncConfiguredNoHub,
		StateSyncActive,
		StateDivergentPending,
		StateHubUnreachable,
	} {
		r := Report{State: s, StateName: s.String(), HasDSN: true}
		for _, rec := range RecommendForState(r) {
			combined := strings.ToLower(rec.Action + " " + rec.Rationale)
			for _, t1 := range tracked {
				if !strings.Contains(combined, strings.ToLower(t1)) {
					continue
				}
				for _, t2 := range dsnPhrases {
					require.NotContainsf(t, combined, strings.ToLower(t2),
						"recommendation refers to both %q and %q in state %s: %q",
						t1, t2, s, rec.Action)
				}
			}
		}
	}
}

// DocLinks must come from the closed allow-list — agent-injected
// URLs would be a vector for steering operators to malicious docs.
func TestRecommend_DocLinksAreFromAllowList(t *testing.T) {
	for _, s := range []State{
		StateSolo,
		StateSyncConfiguredNoHub,
		StateSyncActive,
		StateDivergentPending,
		StateHubUnreachable,
	} {
		r := Report{State: s, StateName: s.String(), HasDSN: true, ConsecutiveErrors: 5, HasUnresolvedConflicts: true}
		for _, rec := range RecommendForState(r) {
			if rec.DocLink == "" {
				continue
			}
			require.Containsf(t, allowedDocLinks, rec.DocLink,
				"DocLink %q for state %s not in allow-list", rec.DocLink, s)
		}
	}
}

func TestRender_BoundedTo4KB(t *testing.T) {
	// Build a synthetic max state with all flags set to maximize output.
	r := Report{
		State:                  StateDivergentPending,
		StateName:              StateDivergentPending.String(),
		HasDSN:                 true,
		HasUnresolvedConflicts: true,
		LocalEventCount:        99999,
		AppliedEventCount:      99999,
		ConsecutiveErrors:      99,
	}
	recs := RecommendForState(r)
	out := Render(r, recs)
	require.LessOrEqualf(t, len(out), MaxRenderBytes,
		"render output exceeded %d bytes: %d", MaxRenderBytes, len(out))
}

func TestRender_ContainsStateNameAndRecommendationsHeader(t *testing.T) {
	r := Report{State: StateSolo, StateName: StateSolo.String()}
	out := Render(r, RecommendForState(r))
	require.Contains(t, out, "State: solo")
	require.Contains(t, out, "Recommendations:")
}

func TestRender_NeverContainsRawDSN(t *testing.T) {
	// The render layer is the last line of defense; assert the report
	// data alone (which excludes DSN by construction) cannot leak even
	// if a recommendation Rationale tried to embed it.
	r := Report{State: StateSyncActive, StateName: StateSyncActive.String(), HasDSN: true}
	out := Render(r, RecommendForState(r))
	require.NotContains(t, out, "postgres://")
	require.NotContains(t, out, "postgresql://")
}

// TruncationMarker should appear if the output would otherwise exceed
// MaxRenderBytes; never silently drop content. Verified by stuffing
// a large recommendation slice past the limit.
func TestRender_TruncationMarkerExplicit(t *testing.T) {
	r := Report{State: StateSyncActive, StateName: StateSyncActive.String(), HasDSN: true}
	// Synthesize an oversized recommendation slice.
	large := make([]Recommendation, 0, 200)
	long := strings.Repeat("x", 200)
	for i := 0; i < 200; i++ {
		large = append(large, Recommendation{
			Action:    "synthetic " + long,
			Rationale: long,
			Severity:  SeverityInfo,
		})
	}
	out := Render(r, large)
	require.LessOrEqual(t, len(out), MaxRenderBytes)
	require.Contains(t, out, TruncationMarker)
}

func actionsOf(recs []Recommendation) []string {
	out := make([]string, len(recs))
	for i, r := range recs {
		out[i] = r.Action
	}
	return out
}
