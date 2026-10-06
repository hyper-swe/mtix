// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Sync update_field classification validates and clears without guessing existing nodes.
package sqlite

import (
	"context"
	"encoding/json"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestSyncUpdate_IssueType_Validated(t *testing.T) {
	for _, kind := range []string{"bug", "feature", "task", "chore", "refactor", "test", "doc", "", "invalid", "BUG", " bug"} {
		t.Run(kind, func(t *testing.T) {
			s, _ := applyTestStore(t)
			create := makeApplyEvent(t, model.OpCreateNode, "TEST-1", "author", 1, map[string]any{"title": "Classified work", "issue_type": "feature"})
			create.ProjectPrefix = "TEST"
			require.NoError(t, applyOnce(t, s, create))
			raw, err := json.Marshal(kind)
			require.NoError(t, err)
			event := makeApplyEvent(t, model.OpUpdateField, "TEST-1", "author", 2, &model.UpdateFieldPayload{FieldName: "issue_type", NewValue: raw})
			event.ProjectPrefix = "TEST"
			err = applyOnce(t, s, event)
			want := kind
			if kind == "invalid" || kind == "BUG" || kind == " bug" {
				require.ErrorIs(t, err, model.ErrInvalidInput)
				want = "feature"
			} else {
				require.NoError(t, err)
				require.NoError(t, applyOnce(t, s, event))
			}
			got, err := s.GetNode(context.Background(), "TEST-1")
			require.NoError(t, err)
			assert.Equal(t, model.IssueType(want), got.IssueType)
		})
	}
}

func TestSyncUpdate_IssueType_MalformedValueRejected(t *testing.T) {
	for _, raw := range []string{`null`, `42`, `true`, `[]`, `{}`, `"story"`} {
		t.Run(raw, func(t *testing.T) {
			s, _ := applyTestStore(t)
			create := makeApplyEvent(t, model.OpCreateNode, "TEST-1", "author", 1, map[string]any{"title": "Classified work", "issue_type": "feature"})
			create.ProjectPrefix = "TEST"
			require.NoError(t, applyOnce(t, s, create))
			event := makeApplyEvent(t, model.OpUpdateField, "TEST-1", "author", 2, &model.UpdateFieldPayload{FieldName: "issue_type", NewValue: json.RawMessage(raw)})
			event.ProjectPrefix = "TEST"
			require.ErrorIs(t, applyOnce(t, s, event), model.ErrInvalidInput)
			got, err := s.GetNode(context.Background(), "TEST-1")
			require.NoError(t, err)
			assert.Equal(t, model.IssueTypeFeature, got.IssueType)
		})
	}
}

func TestSyncUpdate_IssueType_DecodeErrorWrapped(t *testing.T) {
	for _, raw := range []string{"", `"unterminated`} {
		t.Run(raw, func(t *testing.T) {
			_, err := decodeIssueTypeUpdate(json.RawMessage(raw))
			require.ErrorIs(t, err, model.ErrInvalidInput)
			assert.Contains(t, err.Error(), "decode issue type")
		})
	}
}

func TestSyncUpdate_IssueType_SQLFailureRollsBack(t *testing.T) {
	s, _ := applyTestStore(t)
	ctx := context.Background()
	create := makeApplyEvent(t, model.OpCreateNode, "TEST-1", "author", 1, map[string]any{"title": "Classified work", "issue_type": "feature"})
	create.ProjectPrefix = "TEST"
	require.NoError(t, applyOnce(t, s, create))
	_, err := s.WriteDB().ExecContext(ctx, `CREATE TRIGGER reject_type_update BEFORE UPDATE OF issue_type ON nodes BEGIN SELECT RAISE(ABORT,'update refused'); END`)
	require.NoError(t, err)
	event := makeApplyEvent(t, model.OpUpdateField, "TEST-1", "author", 2, &model.UpdateFieldPayload{FieldName: "issue_type", NewValue: json.RawMessage(`"bug"`)})
	event.ProjectPrefix = "TEST"
	err = applyOnce(t, s, event)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "apply issue type to TEST-1")
	got, err := s.GetNode(ctx, "TEST-1")
	require.NoError(t, err)
	assert.Equal(t, model.IssueTypeFeature, got.IssueType)
}
