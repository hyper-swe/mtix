// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Additive create classification survives sync without changing protocol or old payloads.
package sqlite

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestSyncCreate_IssueType_AdditiveAndOptional(t *testing.T) {
	for _, kind := range []string{"bug", "feature", "task", "chore", "refactor", "test", "doc", ""} {
		t.Run(kind, func(t *testing.T) {
			s, _ := applyTestStore(t)
			payload := map[string]any{"title": "Classified work", "node_type": "story", "creator": "author"}
			if kind != "" {
				payload["issue_type"] = kind
			}
			event := makeApplyEvent(t, model.OpCreateNode, "TEST-1", "author", 1, payload)
			event.ProjectPrefix = "TEST"
			require.NoError(t, applyOnce(t, s, event))
			got, err := s.GetNode(context.Background(), "TEST-1")
			require.NoError(t, err)
			assert.Equal(t, model.IssueType(kind), got.IssueType)
			assert.Equal(t, model.NodeTypeEpic, got.NodeType)
			raw, err := buildCreateNodePayload(got)
			require.NoError(t, err)
			var fields map[string]any
			require.NoError(t, json.Unmarshal(raw, &fields))
			if kind == "" {
				assert.NotContains(t, fields, "issue_type")
			} else {
				assert.Equal(t, kind, fields["issue_type"])
			}
			// 0.5.4's applyCreateNode decodes via json.Unmarshal, which ignores new optional fields.
			var legacy legacyCreateNodePayload054
			require.NoError(t, json.Unmarshal(raw, &legacy))
			assert.Equal(t, "Classified work", legacy.Title)
			assert.Equal(t, "author", legacy.Creator)
		})
	}
}

// legacyCreateNodePayload054 freezes the complete payload from v0.5.4-beta
// abec943b16a5f81213c8f08971db56e51cb87451. Its apply path uses json.Unmarshal.
// Retaining this old shape proves unknown optional fields are tolerated.
type legacyCreateNodePayload054 struct {
	Title       string         `json:"title"`
	ParentID    string         `json:"parent_id,omitempty"`
	NodeType    model.NodeType `json:"node_type"`
	Description string         `json:"description,omitempty"`
	Prompt      string         `json:"prompt,omitempty"`
	Acceptance  string         `json:"acceptance,omitempty"`
	Priority    model.Priority `json:"priority,omitempty"`
	Labels      []string       `json:"labels,omitempty"`
	Assignee    string         `json:"assignee,omitempty"`
	Creator     string         `json:"creator,omitempty"`
}
