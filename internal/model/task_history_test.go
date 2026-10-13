// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestHistoryFieldClassOf_StructuredAndFreeTextFields_AreClassified(t *testing.T) {
	tests := []struct {
		field string
		want  model.HistoryFieldClass
	}{
		{"status", model.HistoryFieldStructured}, {"assignee", model.HistoryFieldStructured},
		{"priority", model.HistoryFieldStructured}, {"labels", model.HistoryFieldStructured},
		{"node_type", model.HistoryFieldStructured}, {"issue_type", model.HistoryFieldStructured},
		{"parent", model.HistoryFieldStructured}, {"defer_until", model.HistoryFieldStructured},
		{"deleted_at", model.HistoryFieldStructured}, {"agent_state", model.HistoryFieldStructured},
		{"title", model.HistoryFieldValueFree}, {"description", model.HistoryFieldValueFree},
		{"prompt", model.HistoryFieldValueFree}, {"acceptance", model.HistoryFieldValueFree},
		{"annotations", model.HistoryFieldValueFree}, {"notes", model.HistoryFieldValueFree},
		{"", model.HistoryFieldValueFree}, {"future_field", model.HistoryFieldValueFree},
	}
	for _, tt := range tests {
		t.Run(tt.field, func(t *testing.T) {
			assert.Equal(t, tt.want, model.HistoryFieldClassOf(tt.field))
		})
	}
}
