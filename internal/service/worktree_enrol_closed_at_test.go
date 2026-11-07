// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
)

// Completion stamps are replicated content even when both statuses match.
func TestCompareBoardDocs_ClosedAt_SameStatus_StaysADifference(t *testing.T) {
	node := func(status, closed string) string {
		c := ""
		if closed != "" {
			c = `,"closed_at":"` + closed + `"`
		}
		return `{"nodes":[{"id":"TEST-1","status":"` + status + `"` + c + `}]}`
	}
	for name, tc := range map[string]struct{ a, b string }{
		"open with a stale stamp": {node("open", "2026-01-01T00:00:01Z"), node("open", "")},
		"done at two readings":    {node("done", "2026-01-01T00:00:01Z"), node("done", "2026-01-01T00:00:09Z")},
		"stamp on the hub side":   {node("open", ""), node("open", "2026-01-01T00:00:01Z")},
	} {
		t.Run(name, func(t *testing.T) {
			d, err := service.CompareBoardDocs([]byte(tc.a), []byte(tc.b))
			require.NoError(t, err)
			assert.Equal(t, service.BoardDiff{"TEST-1": {"closed_at"}}, d)
			f, err := service.BoardHubFinding([]byte(tc.a), []byte(tc.b))
			require.NoError(t, err)
			require.NotNil(t, f)
			assert.Equal(t, d, f.Diff)
		})
	}
}

func TestCompareBoardDocs_ClosedAt_DifferentStatus_StaysADifference(t *testing.T) {
	a := `{"nodes":[{"id":"TEST-1","status":"done","closed_at":"2026-01-01T00:00:01Z"}]}`
	b := `{"nodes":[{"id":"TEST-1","status":"open"}]}`
	d, err := service.CompareBoardDocs([]byte(a), []byte(b))
	require.NoError(t, err)
	assert.Equal(t, service.BoardDiff{"TEST-1": {"closed_at", "status"}}, d)
}

// A node whose status is absent on both sides has no status to match: the
// stamp is not normalized.
func TestCompareBoardDocs_ClosedAt_NoStatusEitherSide_StaysADifference(t *testing.T) {
	a := `{"nodes":[{"id":"TEST-1","closed_at":"2026-01-01T00:00:01Z"}]}`
	b := `{"nodes":[{"id":"TEST-1"}]}`
	d, err := service.CompareBoardDocs([]byte(a), []byte(b))
	require.NoError(t, err)
	assert.Equal(t, service.BoardDiff{"TEST-1": {"closed_at"}}, d)
}

// Survivors of the r2 review: a shorter list is not the same list, and an
// absent list is contained in any list.
func TestCompareBoardDocs_Labels_ShorterSideIsADifference(t *testing.T) {
	lab := func(v string) string { return `{"nodes":[{"id":"TEST-1","labels":` + v + `}]}` }
	d, err := service.CompareBoardDocs([]byte(lab(`"x"`)), []byte(lab(`"x,y"`)))
	require.NoError(t, err)
	assert.Equal(t, service.BoardDiff{"TEST-1": {"labels"}}, d)
}

func TestFindLostLocal_AnnotationsAbsentOnTheOldStore_IsNotLoss(t *testing.T) {
	aside := `{"nodes":[{"id":"TEST-1"}]}`
	clone := `{"nodes":[{"id":"TEST-1","annotations":[{"author":"a","text":"theirs"}]}]}`
	got, err := service.FindLostLocal([]byte(aside), []byte(clone), []string{"TEST-1"})
	require.NoError(t, err)
	assert.Nil(t, got, "a real export omits the field when a node has none")
}
