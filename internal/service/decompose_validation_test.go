// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

// TestValidateDecomposeChildren_Table covers the one rule shared by the CLI,
// MCP, REST and gRPC decompose paths (MTIX-279).
func TestValidateDecomposeChildren_Table(t *testing.T) {
	tests := []struct {
		name     string
		children []service.DecomposeInput
		wantErr  string
	}{
		{"valid", []service.DecomposeInput{{Title: "a", Priority: 3, IssueType: model.IssueTypeBug}}, ""},
		{"empty batch", nil, "at least one child"},
		{"empty title", []service.DecomposeInput{{Title: "a"}, {Title: ""}}, "child 1: title is required"},
		{"title too long", []service.DecomposeInput{{Title: strings.Repeat("a", model.MaxTitleLength+1)}}, "child 0: title too long"},
		{"bad issue type", []service.DecomposeInput{{Title: "a", IssueType: "epic"}}, "child 0: issue_type"},
		{"priority high", []service.DecomposeInput{{Title: "a"}, {Title: "b", Priority: 9}}, "child 1: priority"},
		{"priority low", []service.DecomposeInput{{Title: "a", Priority: -1}}, "child 0: priority"},
		{"description size", []service.DecomposeInput{{Title: "a", Description: strings.Repeat("d", model.MaxDescriptionSize+1)}}, "child 0: description"},
		{"prompt size", []service.DecomposeInput{{Title: "a", Prompt: strings.Repeat("p", model.MaxPromptSize+1)}}, "child 0: prompt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := service.ValidateDecomposeChildren(tt.children)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, model.ErrInvalidInput)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// TestDecompose_BadPriorityInBatch_RefusedAndDryRunAgrees proves the service
// and the dry run share the validation.
func TestDecompose_BadPriorityInBatch_RefusedAndDryRunAgrees(t *testing.T) {
	svc, s, _ := newTestNodeService(t)
	ctx := context.Background()
	parent, err := svc.CreateNode(ctx, &service.CreateNodeRequest{Project: "PROJ", Title: "Parent", Creator: "admin"})
	require.NoError(t, err)
	children := []service.DecomposeInput{{Title: "good"}, {Title: "bad", Priority: 9}}

	_, err = svc.Decompose(ctx, parent.ID, children, "admin")
	require.ErrorIs(t, err, model.ErrInvalidInput)
	_, err = svc.DecomposeDryRun(ctx, parent.ID, children)
	require.ErrorIs(t, err, model.ErrInvalidInput)
	kids, err := s.GetDirectChildren(ctx, parent.ID)
	require.NoError(t, err)
	assert.Empty(t, kids)
}

// TestDecodeDecomposeChildren_NamesChildIndex proves a refused child is named
// by its index and a type mismatch carries ErrInvalidInput without Go type names.
func TestDecodeDecomposeChildren_NamesChildIndex(t *testing.T) {
	raws := func(s string) []json.RawMessage {
		var r []json.RawMessage
		require.NoError(t, json.Unmarshal([]byte(s), &r))
		return r
	}
	got, err := service.DecodeDecomposeChildren(raws(`[{"title":"a","type":"bug"},{"title":"b"}]`))
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, model.IssueTypeBug, got[0].IssueType)

	tests := []struct {
		name, in string
		want     []string
	}{
		{"unknown key", `[{"title":"a"},{"title":"b","bogus":1}]`, []string{"child 1", "bogus"}},
		{"wrong type", `[{"title":"a"},{"title":"b","priority":"high"}]`, []string{"child 1", "priority"}},
		{"conflict", `[{"title":"a","type":"bug","issue_type":"test"}]`, []string{"child 0", "conflicts"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := service.DecodeDecomposeChildren(raws(tt.in))
			require.ErrorIs(t, err, model.ErrInvalidInput)
			for _, w := range tt.want {
				assert.Contains(t, err.Error(), w)
			}
			assert.NotContains(t, err.Error(), "Go struct field")
			assert.NotContains(t, err.Error(), "decomposeInputWire")
		})
	}
}

// TestDecomposeInput_UnmarshalJSON_TypeMismatch_WrapsInvalidInput covers the
// direct decode path used by ParseJSONL.
func TestDecomposeInput_UnmarshalJSON_TypeMismatch_WrapsInvalidInput(t *testing.T) {
	var in service.DecomposeInput
	err := json.Unmarshal([]byte(`{"title":"a","priority":"high"}`), &in)
	require.ErrorIs(t, err, model.ErrInvalidInput)
	assert.Contains(t, err.Error(), "priority")
	assert.NotContains(t, err.Error(), "Go struct field")
}
