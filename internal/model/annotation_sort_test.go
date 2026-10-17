// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestSortAnnotations_EqualTimestamps_TieBreakByID(t *testing.T) {
	at := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	got := model.SortAnnotations([]model.Annotation{
		{ID: "C", CreatedAt: at},
		{ID: "A", CreatedAt: at},
		{ID: "B", CreatedAt: at},
	})
	ids := []string{got[0].ID, got[1].ID, got[2].ID}
	assert.Equal(t, []string{"A", "B", "C"}, ids)
}

func TestSortAnnotations_MixedTimestamps_OldestFirstWithoutMutatingInput(t *testing.T) {
	base := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	in := []model.Annotation{
		{ID: "B", CreatedAt: base.Add(time.Hour)},
		{ID: "A", CreatedAt: base},
	}
	got := model.SortAnnotations(in)
	assert.Equal(t, "A", got[0].ID)
	assert.Equal(t, "B", in[0].ID, "input order is unchanged")
}

func TestSortAnnotations_Empty_ReturnsEmpty(t *testing.T) {
	assert.Empty(t, model.SortAnnotations(nil))
}
