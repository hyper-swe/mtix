// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/hyper-swe/mtix/internal/model"
)

// TestCarriesNodeColumns_SchemaVersions_ReadsOnlyFromMajor2 verifies which
// schema versions a merge import trusts to carry the node columns schema
// 2.0.0 added (MTIX-95.31.1): major 2 and later do; every 1.x version, an
// empty version and anything unparsable read as 1.0.0, so the local values
// are kept.
func TestCarriesNodeColumns_SchemaVersions_ReadsOnlyFromMajor2(t *testing.T) {
	tests := []struct {
		version string
		want    bool
	}{
		{"", false},
		{"1", false},
		{"1.0.0", false},
		{"1.1.0", false},
		{"1.99.0", false},
		{"0.9.0", false},
		{"x.y.z", false},
		{"2", true},
		{"2.0.0", true},
		{"2.3.1", true},
		{"3.0.0", true},
	}
	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			assert.Equal(t, tt.want, carriesNodeColumns(tt.version))
		})
	}
}

// streamTestTime is the fixed instant the merge unit tests stamp.
func streamTestTime() time.Time {
	return time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
}

// annotationIDs lists the ids of annotations in order.
func annotationIDs(anns []model.Annotation) []string {
	ids := make([]string, 0, len(anns))
	for _, a := range anns {
		ids = append(ids, a.ID)
	}
	return ids
}

// activityTexts lists the id and text of activity entries in order.
func activityTexts(entries []model.ActivityEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.ID+":"+e.Text)
	}
	return out
}

// TestMergeActivity_SameIDDifferentEntries_KeepsBoth verifies that two
// different activity entries sharing an id are both kept: activity ids
// derive from a timestamp and can collide across machines, so an entry is
// identified by its id, type, author, text and time (MTIX-95.31.1).
func TestMergeActivity_SameIDDifferentEntries_KeepsBoth(t *testing.T) {
	ts := streamTestTime()
	local := []model.ActivityEntry{{ID: "act-1", Type: model.ActivityTypeComment, Author: "a", Text: "local", CreatedAt: ts}}
	incoming := []model.ActivityEntry{
		{ID: "act-1", Type: model.ActivityTypeComment, Author: "a", Text: "local", CreatedAt: ts},
		{ID: "act-1", Type: model.ActivityTypeComment, Author: "b", Text: "other machine", CreatedAt: ts},
	}

	merged, changed := mergeActivity(local, incoming)
	assert.True(t, changed)
	assert.Equal(t, []string{"act-1:local", "act-1:other machine"}, activityTexts(merged))

	again, changedAgain := mergeActivity(merged, incoming)
	assert.False(t, changedAgain, "merging the same entries again adds nothing")
	assert.Len(t, again, 2)
}

// TestMergeAnnotations_WithoutID_DuplicateKeptOnce verifies that an
// annotation without an id is matched by its author, text and time: the
// same one arriving again is kept once, and a different one is added
// (MTIX-95.31.1).
func TestMergeAnnotations_WithoutID_DuplicateKeptOnce(t *testing.T) {
	ts := streamTestTime()
	same := model.Annotation{Author: "x", Text: "no id", CreatedAt: ts}
	other := model.Annotation{Author: "x", Text: "another without id", CreatedAt: ts.Add(time.Minute)}

	merged, changed := mergeAnnotations([]model.Annotation{same}, []model.Annotation{same})
	assert.False(t, changed, "an identical id-less annotation is already held")
	assert.Len(t, merged, 1)

	merged, changed = mergeAnnotations([]model.Annotation{same}, []model.Annotation{same, other, other})
	assert.True(t, changed)
	if assert.Len(t, merged, 2) {
		assert.Equal(t, "no id", merged[0].Text)
		assert.Equal(t, "another without id", merged[1].Text)
	}
}

// TestMergeAnnotations_EqualTimes_ConvergeInBothOrders verifies that two
// stores merging each other's annotations end with the same list: entries
// with the same time are ordered by id, whichever side held which
// (MTIX-95.31.1).
func TestMergeAnnotations_EqualTimes_ConvergeInBothOrders(t *testing.T) {
	ts := streamTestTime()
	a := model.Annotation{ID: "01J9A", Author: "a", Text: "a", CreatedAt: ts}
	b := model.Annotation{ID: "01J9B", Author: "b", Text: "b", CreatedAt: ts}

	ab, changed := mergeAnnotations([]model.Annotation{a}, []model.Annotation{b})
	assert.True(t, changed)
	ba, changed := mergeAnnotations([]model.Annotation{b}, []model.Annotation{a})
	assert.True(t, changed)

	assert.Equal(t, []string{"01J9A", "01J9B"}, annotationIDs(ab))
	assert.Equal(t, annotationIDs(ab), annotationIDs(ba), "both merge orders must converge")
}

// TestMergeActivity_EqualTimes_ConvergeInBothOrders is the activity
// counterpart: equal times are ordered by id in both merge orders
// (MTIX-95.31.1).
func TestMergeActivity_EqualTimes_ConvergeInBothOrders(t *testing.T) {
	ts := streamTestTime()
	a := model.ActivityEntry{ID: "act-a", Type: model.ActivityTypeComment, Author: "a", Text: "a", CreatedAt: ts}
	b := model.ActivityEntry{ID: "act-b", Type: model.ActivityTypeComment, Author: "b", Text: "b", CreatedAt: ts}

	ab, changed := mergeActivity([]model.ActivityEntry{a}, []model.ActivityEntry{b})
	assert.True(t, changed)
	ba, changed := mergeActivity([]model.ActivityEntry{b}, []model.ActivityEntry{a})
	assert.True(t, changed)

	assert.Equal(t, []string{"act-a:a", "act-b:b"}, activityTexts(ab))
	assert.Equal(t, activityTexts(ab), activityTexts(ba), "both merge orders must converge")
}

// TestMergeActivity_EntriesDifferingInOneField_BothKept verifies that an
// activity entry is identified by its id, type, author, text and time
// together: two entries with the same id that differ in any one of the
// other fields are both kept (MTIX-95.31.1).
func TestMergeActivity_EntriesDifferingInOneField_BothKept(t *testing.T) {
	ts := streamTestTime()
	base := model.ActivityEntry{ID: "act-1", Type: model.ActivityTypeComment, Author: "a", Text: "text", CreatedAt: ts}
	tests := []struct {
		name  string
		other func(e model.ActivityEntry) model.ActivityEntry
	}{
		{"text only", func(e model.ActivityEntry) model.ActivityEntry { e.Text = "other text"; return e }},
		{"author only", func(e model.ActivityEntry) model.ActivityEntry { e.Author = "b"; return e }},
		{"type only", func(e model.ActivityEntry) model.ActivityEntry { e.Type = model.ActivityTypeNote; return e }},
		{"created_at only", func(e model.ActivityEntry) model.ActivityEntry { e.CreatedAt = ts.Add(time.Second); return e }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			other := tt.other(base)
			merged, changed := mergeActivity([]model.ActivityEntry{base}, []model.ActivityEntry{other})
			assert.True(t, changed, "an entry that differs is new")
			assert.Len(t, merged, 2)

			same, changedAgain := mergeActivity(merged, []model.ActivityEntry{base, other})
			assert.False(t, changedAgain, "both are held now")
			assert.Len(t, same, 2)
		})
	}
}
