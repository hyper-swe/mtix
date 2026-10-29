// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package validator_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/sync/validator"
)

// TestValidateIngest_InvalidNodeID_Rejected: a pulled event is refused when
// its node id, a create_node parent id or a link or unlink endpoint is
// outside the node id grammar (MTIX-187). The error names the field and
// the id, and wraps model.ErrInvalidInput.
func TestValidateIngest_InvalidNodeID_Rejected(t *testing.T) {
	tests := []struct {
		name    string
		op      model.OpType
		nodeID  string
		payload string
		want    string
	}{
		{"node id with a non-ASCII segment", model.OpCreateNode, "MTIX-1é.4", `{"title":"x"}`, `node_id "MTIX-1é.4"`},
		{"node id without a dash", model.OpComment, "MTIX1", `{"body":"x"}`, `node_id "MTIX1"`},
		{"node id with a trailing dot", model.OpDelete, "MTIX-1.", `{}`, `node_id "MTIX-1."`},
		{"node id with a space", model.OpClaim, "MTIX-1 .2", `{"agent_id":"a"}`, `node_id "MTIX-1 .2"`},
		{"node id with a lowercase prefix", model.OpSetPrompt, "mtix-1", `{"prompt_text":"x"}`, `node_id "mtix-1"`},
		{"parent id with a non-ASCII segment", model.OpCreateNode, "MTIX-1.4",
			`{"title":"x","parent_id":"MTIX-1é"}`, `parent_id "MTIX-1é"`},
		{"link_dep endpoint", model.OpLinkDep, "MTIX-1",
			`{"depends_on_node_id":"MTIX-2.x","dep_type":"blocks"}`, `depends_on_node_id "MTIX-2.x"`},
		{"link_dep without an endpoint", model.OpLinkDep, "MTIX-1",
			`{"dep_type":"blocks"}`, `depends_on_node_id ""`},
		{"unlink_dep endpoint", model.OpUnlinkDep, "MTIX-1",
			`{"depends_on_node_id":"MTIX-2é"}`, `depends_on_node_id "MTIX-2é"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := validEvent()
			e.OpType, e.NodeID, e.Payload = tt.op, tt.nodeID, json.RawMessage(tt.payload)

			err := validator.ValidateIngest(e, refNow, nil)

			require.ErrorIs(t, err, model.ErrInvalidInput)
			require.ErrorContains(t, err, "invalid node id")
			require.ErrorContains(t, err, tt.want)
		})
	}
}

// TestValidateIngest_ValidNodeIDs_Accepted: ids inside the grammar pass,
// including a prefix with an underscore (the sync prefix grammar admits it,
// MTIX-95.17), a provisional uid segment, a root create without a parent and
// an op whose payload names no node.
func TestValidateIngest_ValidNodeIDs_Accepted(t *testing.T) {
	tests := []struct {
		name    string
		op      model.OpType
		nodeID  string
		payload string
	}{
		{"root create", model.OpCreateNode, "MTIX-1", `{"title":"x"}`},
		{"child create", model.OpCreateNode, "MTIX-1.4.2", `{"title":"x","parent_id":"MTIX-1.4"}`},
		{"provisional uid segment", model.OpCreateNode, "MTIX-1.u0193fa00", `{"title":"x","parent_id":"MTIX-1"}`},
		{"multi-dash prefix", model.OpUpdateField, "MY-PROJ-3.1", `{"field_name":"title","new_value":"x"}`},
		{"prefix with an underscore", model.OpComment, "DEP_ADD-1", `{"body":"x"}`},
		{"link_dep across prefixes", model.OpLinkDep, "MTIX-1", `{"depends_on_node_id":"OTHER-2.1","dep_type":"blocks"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := validEvent()
			e.OpType, e.NodeID, e.Payload = tt.op, tt.nodeID, json.RawMessage(tt.payload)

			require.NoError(t, validator.ValidateIngest(e, refNow, nil))
		})
	}
}

// TestValidate_InvalidNodeID_PushUnchanged: the hub's push validation does
// not check node ids, so a replica that already holds such a task can still
// push its own changes (MTIX-187); only ingest refuses them.
func TestValidate_InvalidNodeID_PushUnchanged(t *testing.T) {
	e := validEvent()
	e.NodeID = "MTIX-1é.4"

	require.NoError(t, validator.Validate(e, refNow, nil))
}
