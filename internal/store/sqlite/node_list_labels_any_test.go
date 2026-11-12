// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// seedLabelNodes creates five roots with distinct label sets for LabelsAny tests.
func seedLabelNodes(t *testing.T, s *sqlite.Store) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	for _, c := range []struct {
		id     string
		labels []string
	}{
		{"PROJ-1", []string{"desk:now", "bug"}},
		{"PROJ-2", []string{"desk:backlog"}},
		{"PROJ-3", []string{"desk:nowhere", "x%y"}},
		{"PROJ-4", nil},
		{"PROJ-5", []string{"Desk:Now"}},
	} {
		n := makeRootNode(c.id, "PROJ", "Node "+c.id, now)
		n.Labels = c.labels
		require.NoError(t, s.CreateNode(ctx, n))
	}
}

// MTIX-255.4: LabelsAny matches nodes carrying ANY listed label, exactly.
func TestListNodes_LabelsAny_MatchesAnyListedLabel(t *testing.T) {
	tests := []struct {
		name   string
		labels []string
		want   []string
	}{
		{"single colon label", []string{"desk:now"}, []string{"PROJ-1"}},
		{"two labels any-of", []string{"desk:now", "desk:backlog"}, []string{"PROJ-1", "PROJ-2"}},
		{"one present one absent", []string{"nope", "bug"}, []string{"PROJ-1"}},
		{"no match", []string{"missing"}, nil},
		{"prefix is not a match", []string{"desk"}, nil},
		{"case sensitive", []string{"Desk:Now"}, []string{"PROJ-5"}},
		{"percent is literal", []string{"x%y"}, []string{"PROJ-3"}},
		{"wildcard percent matches nothing", []string{"%"}, nil},
		{"empty means no filter", nil, []string{"PROJ-1", "PROJ-2", "PROJ-3", "PROJ-4", "PROJ-5"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestStore(t)
			seedLabelNodes(t, s)
			nodes, total, err := s.ListNodes(context.Background(),
				store.NodeFilter{LabelsAny: tt.labels}, store.ListOptions{Limit: 50})
			require.NoError(t, err)
			var got []string
			for _, n := range nodes {
				got = append(got, n.ID)
			}
			assert.ElementsMatch(t, tt.want, got)
			assert.Equal(t, len(tt.want), total)
		})
	}
}

// MTIX-255.4: LabelsAny combines by AND with status and under; all-of Labels is unchanged.
func TestListNodes_LabelsAny_CombinesWithOtherFilters(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedLabelNodes(t, s)
	now := time.Now().UTC().Truncate(time.Second)
	child := makeChildNode("PROJ-1.1", "PROJ-1", "PROJ", "Child", 1, 1, now)
	child.Labels = []string{"desk:now"}
	require.NoError(t, s.CreateNode(ctx, child))
	require.NoError(t, s.TransitionStatus(ctx, "PROJ-2", model.StatusInProgress, "wip", "test"))

	ids := func(f store.NodeFilter) []string {
		nodes, _, err := s.ListNodes(ctx, f, store.ListOptions{Limit: 50})
		require.NoError(t, err)
		var out []string
		for _, n := range nodes {
			out = append(out, n.ID)
		}
		return out
	}
	both := []string{"desk:now", "desk:backlog"}
	assert.ElementsMatch(t, []string{"PROJ-2"},
		ids(store.NodeFilter{LabelsAny: both, Status: []model.Status{model.StatusInProgress}}))
	// Under includes the subtree root itself, so PROJ-1 and its child both match.
	assert.ElementsMatch(t, []string{"PROJ-1", "PROJ-1.1"},
		ids(store.NodeFilter{LabelsAny: both, Under: []string{"PROJ-1"}}))
	assert.ElementsMatch(t, []string{"PROJ-1.1"},
		ids(store.NodeFilter{LabelsAny: []string{"desk:now"}, Under: []string{"PROJ-1.1"}}))
	// The existing all-of filter still requires every label.
	assert.ElementsMatch(t, []string{"PROJ-1"},
		ids(store.NodeFilter{Labels: []string{"desk:now", "bug"}}))
	assert.Empty(t, ids(store.NodeFilter{Labels: []string{"desk:now", "desk:backlog"}}))
}

// MTIX-255.4: a hostile label value is bound, never interpreted as SQL.
func TestListNodes_LabelsAny_HostileValueIsBound(t *testing.T) {
	s := newTestStore(t)
	seedLabelNodes(t, s)
	nodes, total, err := s.ListNodes(context.Background(),
		store.NodeFilter{LabelsAny: []string{"x') OR 1=1 --"}}, store.ListOptions{Limit: 50})
	require.NoError(t, err)
	assert.Empty(t, nodes)
	assert.Zero(t, total)
}

// MTIX-255.4: nodes whose labels column is NULL, an empty array or empty text
// are excluded by LabelsAny without error, and are still listed unfiltered.
func TestListNodes_LabelsAny_NullAndEmptyLabelsExcluded(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedLabelNodes(t, s)
	now := time.Now().UTC().Truncate(time.Second)
	empty := makeRootNode("PROJ-6", "PROJ", "Empty array", now)
	empty.Labels = []string{}
	require.NoError(t, s.CreateNode(ctx, empty))
	// PROJ-4 has no labels; force its column to SQL NULL, and PROJ-6 to '[]'.
	_, err := s.WriteDB().ExecContext(ctx, `UPDATE nodes SET labels = NULL WHERE id = ?`, "PROJ-4")
	require.NoError(t, err)
	_, err = s.WriteDB().ExecContext(ctx, `UPDATE nodes SET labels = '[]' WHERE id = ?`, "PROJ-6")
	require.NoError(t, err)

	nodes, total, err := s.ListNodes(ctx,
		store.NodeFilter{LabelsAny: []string{"desk:now", "desk:backlog", ""}}, store.ListOptions{Limit: 50})
	require.NoError(t, err)
	var got []string
	for _, n := range nodes {
		got = append(got, n.ID)
	}
	assert.ElementsMatch(t, []string{"PROJ-1", "PROJ-2"}, got)
	assert.Equal(t, 2, total)

	_, all, err := s.ListNodes(ctx, store.NodeFilter{}, store.ListOptions{Limit: 50})
	require.NoError(t, err)
	assert.Equal(t, 6, all, "NULL and empty-label nodes still list without the filter")
}
