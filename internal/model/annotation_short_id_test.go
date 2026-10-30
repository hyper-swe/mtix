// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestShortAnnotationID_Lengths_TruncatesToPrefixLength(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want string
	}{
		{"full ulid", "01ARZ3NDEKTSV4RRFFQ69G5FAV", "01ARZ3NDEKTS"},
		{"exactly the short length", "01ARZ3NDEKTS", "01ARZ3NDEKTS"},
		{"shorter than the short length", "abc", "abc"},
		{"empty", "", ""},
		{"multi-byte runes are not split", "界界界界界界界界界界界界界界", "界界界界界界界界界界界界"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, model.ShortAnnotationID(tt.id))
		})
	}
	assert.Equal(t, 12, model.AnnotationShortIDLen)
}
