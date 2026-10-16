// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestActivityPage_BoundsAndOverflow(t *testing.T) {
	entries := []model.ActivityEntry{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	for _, tt := range []struct {
		name          string
		limit, offset int
		want          []model.ActivityEntry
	}{
		{"unlimited", 0, 0, entries}, {"negative limit", -1, 0, entries},
		{"negative offset", 0, -1, entries}, {"bounded", 1, 1, entries[1:2]},
		{"overflow", math.MaxInt, 1, entries[1:]}, {"at end", 10, 3, []model.ActivityEntry{}},
		{"past end", 10, math.MaxInt, []model.ActivityEntry{}},
	} {
		t.Run(tt.name, func(t *testing.T) { require.Equal(t, tt.want, activityPage(entries, tt.limit, tt.offset)) })
	}
	require.Equal(t, []model.ActivityEntry{}, activityPage(nil, 0, 0))
}

func TestMergeNodeActivity_EmptyIDsAndInputImmutability(t *testing.T) {
	stamp := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	activity := []model.ActivityEntry{
		{ID: "same", Text: "old", CreatedAt: stamp}, {ID: "same", Text: "new", CreatedAt: stamp},
		{Text: "unidentified one", CreatedAt: stamp}, {Text: "unidentified two", CreatedAt: stamp},
	}
	before := append([]model.ActivityEntry(nil), activity...)
	rows := mergeNodeActivity(activity, nil)
	require.Len(t, rows, 3)
	require.Equal(t, "new", rows[0].Text)
	require.Equal(t, "unidentified one", rows[1].Text)
	require.Equal(t, "unidentified two", rows[2].Text)
	require.Equal(t, before, activity)
	require.Equal(t, []model.ActivityEntry{}, mergeNodeActivity(nil, nil))
}
