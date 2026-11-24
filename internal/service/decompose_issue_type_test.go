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

// TestParseJSONL_IssueTypeKeys_PersistedOnChildren covers MTIX-279: both the
// issue_type key and the type alias reach the child; no key leaves it unset.
func TestParseJSONL_IssueTypeKeys_PersistedOnChildren(t *testing.T) {
	svc, s, _ := newTestNodeService(t)
	ctx := context.Background()
	parent, err := svc.CreateNode(ctx, &service.CreateNodeRequest{Project: "PROJ", Title: "Parent", Creator: "admin"})
	require.NoError(t, err)

	entries, err := service.ParseJSONL(strings.NewReader(
		`{"title":"a","issue_type":"bug"}` + "\n" +
			`{"title":"b","type":"feature"}` + "\n" +
			`{"title":"c","type":"doc","issue_type":"doc"}` + "\n" +
			`{"title":"d"}` + "\n"))
	require.NoError(t, err)
	ids, err := svc.Decompose(ctx, parent.ID, service.ToDecomposeInputs(entries), "admin")
	require.NoError(t, err)

	want := []model.IssueType{model.IssueTypeBug, model.IssueTypeFeature, model.IssueTypeDoc, ""}
	for i, id := range ids {
		got, err := s.GetNode(ctx, id)
		require.NoError(t, err)
		assert.Equal(t, want[i], got.IssueType, id)
	}
}

// TestDecomposeDryRun_IssueType_ShownInProposal covers the dry-run surface.
func TestDecomposeDryRun_IssueType_ShownInProposal(t *testing.T) {
	svc, _, _ := newTestNodeService(t)
	ctx := context.Background()
	parent, err := svc.CreateNode(ctx, &service.CreateNodeRequest{Project: "PROJ", Title: "Parent", Creator: "admin"})
	require.NoError(t, err)

	proposed, err := svc.DecomposeDryRun(ctx, parent.ID, []service.DecomposeInput{{Title: "a", IssueType: model.IssueTypeBug}})
	require.NoError(t, err)
	require.Len(t, proposed, 1)
	assert.Equal(t, model.IssueTypeBug, proposed[0].IssueType)

	_, err = svc.DecomposeDryRun(ctx, parent.ID, []service.DecomposeInput{{Title: "a", IssueType: "epic"}})
	assert.ErrorIs(t, err, model.ErrInvalidInput)
}

// TestParseJSONL_BadIssueTypeOrUnknownKey_NamesLineAndKey covers refusal.
func TestParseJSONL_BadIssueTypeOrUnknownKey_NamesLineAndKey(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{"invalid issue type", `{"title":"ok"}` + "\n" + `{"title":"x","issue_type":"epic"}`, []string{"line 2", "epic"}},
		{"invalid type alias", `{"title":"x","type":"nope"}`, []string{"line 1", "nope"}},
		{"conflicting keys", `{"title":"x","type":"bug","issue_type":"feature"}`, []string{"line 1", "type"}},
		{"unknown key", `{"title":"ok"}` + "\n" + `{"title":"x","severity":"high"}`, []string{"line 2", "severity"}},
		{"misspelled key", `{"title":"x","acceptence":"y"}`, []string{"line 1", "acceptence"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := service.ParseJSONL(strings.NewReader(tt.input))
			require.ErrorIs(t, err, model.ErrInvalidInput)
			for _, w := range tt.want {
				assert.Contains(t, err.Error(), w)
			}
		})
	}
}

// TestDecompose_InvalidIssueTypeInBatch_CreatesNothing covers atomicity.
func TestDecompose_InvalidIssueTypeInBatch_CreatesNothing(t *testing.T) {
	svc, s, _ := newTestNodeService(t)
	ctx := context.Background()
	parent, err := svc.CreateNode(ctx, &service.CreateNodeRequest{Project: "PROJ", Title: "Parent", Creator: "admin"})
	require.NoError(t, err)

	_, err = svc.Decompose(ctx, parent.ID, []service.DecomposeInput{
		{Title: "good", IssueType: model.IssueTypeBug}, {Title: "bad", IssueType: "epic"},
	}, "admin")
	require.ErrorIs(t, err, model.ErrInvalidInput)
	kids, err := s.GetDirectChildren(ctx, parent.ID)
	require.NoError(t, err)
	assert.Empty(t, kids)
}

// TestDecomposeInput_UnmarshalJSON_AliasAndStrictKeys covers the shared decoder
// used by the MCP tool.
func TestDecomposeInput_UnmarshalJSON_AliasAndStrictKeys(t *testing.T) {
	var ok []service.DecomposeInput
	require.NoError(t, json.Unmarshal([]byte(`[{"title":"a","type":"bug"},{"title":"b","issue_type":"test"}]`), &ok))
	assert.Equal(t, model.IssueTypeBug, ok[0].IssueType)
	assert.Equal(t, model.IssueTypeTest, ok[1].IssueType)

	var bad []service.DecomposeInput
	err := json.Unmarshal([]byte(`[{"title":"a","bogus":1}]`), &bad)
	require.ErrorIs(t, err, model.ErrInvalidInput)
	assert.Contains(t, err.Error(), "bogus")
}
