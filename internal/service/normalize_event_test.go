// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/hyper-swe/mtix/internal/hooks"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// TestNormalizeEvent_AllOpTypes: the journal→hook-event mapping for every
// trigger op_type, the addressed/unaddressed comment split, and non-trigger
// ops that must produce a zero Event (Name == "").
func TestNormalizeEvent_AllOpTypes(t *testing.T) {
	cases := []struct {
		name     string
		je       sqlite.JournalEvent
		wantName string
		check    func(*testing.T, hooks.Event)
	}{
		{
			name:     "addressed comment",
			je:       sqlite.JournalEvent{Seq: 1, NodeID: "P-1", Author: "planner", OpType: "comment", Payload: []byte(`{"to":"dev","body":"go"}`)},
			wantName: hooks.EventCommentAddressed,
			check:    func(t *testing.T, e hooks.Event) { assert.Equal(t, "dev", e.ToAgent) },
		},
		{
			name:     "unaddressed comment is not a hook event",
			je:       sqlite.JournalEvent{Seq: 2, OpType: "comment", Payload: []byte(`{"body":"note"}`)},
			wantName: "",
		},
		{
			name:     "status transition",
			je:       sqlite.JournalEvent{Seq: 3, NodeID: "P-1", OpType: "transition_status", Payload: []byte(`{"to":"done"}`)},
			wantName: hooks.EventStatusChanged,
			check:    func(t *testing.T, e hooks.Event) { assert.Equal(t, "done", e.StatusTo) },
		},
		{
			name:     "node created",
			je:       sqlite.JournalEvent{Seq: 4, NodeID: "P-2", OpType: "create_node", Payload: []byte(`{}`)},
			wantName: hooks.EventNodeCreated,
		},
		{
			name:     "non-trigger op is zero",
			je:       sqlite.JournalEvent{Seq: 5, OpType: "claim", Payload: []byte(`{}`)},
			wantName: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := service.NormalizeEvent(tc.je)
			assert.Equal(t, tc.wantName, got.Name)
			if tc.wantName != "" {
				assert.Equal(t, tc.je.Seq, got.Seq)
				assert.Equal(t, tc.je.NodeID, got.NodeID)
			}
			if tc.check != nil {
				tc.check(t, got)
			}
		})
	}
}
