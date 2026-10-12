// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

// MTIX-146: ListNodes validates the text query at the service boundary.
func TestListNodes_TextFilter_Validation(t *testing.T) {
	tests := []struct {
		name    string
		text    []string
		wantErr bool
	}{
		{"none", nil, false},
		{"one word", []string{"template"}, false},
		{"empty word", []string{""}, true},
		{"blank word", []string{"   "}, true},
		{"NUL byte", []string{"a\x00b"}, true},
		{"word at limit", []string{strings.Repeat("a", service.MaxSearchWordLen)}, false},
		{"word over limit", []string{strings.Repeat("a", service.MaxSearchWordLen+1)}, true},
		{"too many words", make([]string, service.MaxSearchWords+1), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for i, w := range tt.text {
				if w == "" && tt.name == "too many words" {
					tt.text[i] = "w"
				}
			}
			svc := service.NewNodeService(&mockStore{}, nil, nil, nil, time.Now)
			_, _, err := svc.ListNodes(context.Background(), service.NodeFilter{Text: tt.text}, service.ListOptions{Limit: 5})
			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, model.ErrInvalidInput)
				return
			}
			assert.NoError(t, err)
		})
	}
}
