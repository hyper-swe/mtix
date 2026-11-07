// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func annotationOf(id string) model.Annotation {
	return model.Annotation{ID: id, Author: "bob", Text: "x"}
}

// MTIX-112.17.16.16 round 3: the fields each event settles, and the fields a
// command here can send at all (survivors of the round 2 mutation run).

func TestFieldsOfEvent_EachOpNamesTheFieldsItSettles(t *testing.T) {
	tests := []struct {
		name, op, payload string
		want              []string
	}{
		{"update_field", "update_field", `{"field_name":"title","new_value":"x"}`, []string{"title"}},
		{"update_field without a name", "update_field", `{}`, nil},
		{"set_prompt", "set_prompt", `{}`, []string{"prompt"}},
		{"set_acceptance", "set_acceptance", `{}`, []string{"acceptance"}},
		{"transition carries the status only", "transition_status", `{}`, []string{"status"}},
		{"defer carries the wake time", "defer", `{}`, []string{"status", "defer_until"}},
		{"claim", "claim", `{}`, []string{"assignee", "status"}},
		{"unclaim", "unclaim", `{}`, []string{"assignee", "status"}},
		{"delete", "delete", `{}`, []string{"deleted_at"}},
		{"comment", "comment", `{}`, []string{"annotations"}},
		{"link_dep", "link_dep", `{}`, []string{"dependencies"}},
		{"unlink_dep", "unlink_dep", `{}`, []string{"dependencies"}},
		{"create_node settles nothing", "create_node", `{}`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, fieldsOfEvent(tt.op, tt.payload))
		})
	}
}

func TestSettleableFields_KeepsOnlyFieldsACommandHereSends(t *testing.T) {
	got := SettleableFields([]string{"title", "weight", "defer_until", "agent_state", "deleted_at", "status", "dependencies", "annotations"})
	require.Equal(t, []string{"title", "status", "dependencies", "annotations"}, got,
		"a wake time, an agent state and a deletion have no command that journals them")
}

func TestNodeFieldDiffs_NamesAnAnnotationAddedOrRemoved(t *testing.T) {
	b := &exportNode{}
	a := &exportNode{}
	require.NotContains(t, nodeFieldDiffs(b, a), "annotations")
	a.Annotations = append(a.Annotations, annotationOf("01ARZ3NDEKTSV4RRFFQ69G5FAV"))
	require.Contains(t, nodeFieldDiffs(b, a), "annotations")
	require.Contains(t, nodeFieldDiffs(a, b), "annotations")
}
