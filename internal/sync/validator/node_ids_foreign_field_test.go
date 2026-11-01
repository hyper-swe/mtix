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

// TestValidateIngest_NodeIDCheck_MistypedForeignFieldCannotHideBadID: the
// node id check decodes the payload the way the apply does, into the op's
// own struct. A field that belongs to another op, with the wrong type, must
// not make the check skip the id the op does name (MTIX-187).
func TestValidateIngest_NodeIDCheck_MistypedForeignFieldCannotHideBadID(t *testing.T) {
	tests := []struct {
		name    string
		op      model.OpType
		payload string
		want    string
	}{
		{"link_dep with a numeric parent_id", model.OpLinkDep,
			`{"depends_on_node_id":"MTIX-9é","dep_type":"blocks","parent_id":5}`, `depends_on_node_id "MTIX-9\u00e9"`},
		{"unlink_dep with a numeric parent_id", model.OpUnlinkDep,
			`{"depends_on_node_id":"MTIX-9é","parent_id":5}`, `depends_on_node_id "MTIX-9\u00e9"`},
		{"create_node with a numeric depends_on_node_id", model.OpCreateNode,
			`{"title":"x","parent_id":"MTIX-1é","depends_on_node_id":5}`, `parent_id "MTIX-1\u00e9"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := validEvent()
			e.OpType, e.NodeID, e.Payload = tt.op, "MTIX-1", json.RawMessage(tt.payload)

			err := validator.ValidateIngest(e, refNow, nil)

			require.ErrorIs(t, err, model.ErrInvalidInput)
			require.ErrorContains(t, err, "invalid node id")
			require.ErrorContains(t, err, tt.want)
		})
	}
}

// TestValidateIngest_NodeIDCheck_UndecodablePayload_FailsClosed: a
// create_node, link_dep or unlink_dep payload the apply cannot decode is
// refused by the check, never passed on (MTIX-187).
func TestValidateIngest_NodeIDCheck_UndecodablePayload_FailsClosed(t *testing.T) {
	tests := []struct {
		name    string
		op      model.OpType
		payload string
	}{
		{"create_node with a payload that is not an object", model.OpCreateNode, `"x"`},
		{"create_node with a numeric parent_id", model.OpCreateNode, `{"title":"x","parent_id":5}`},
		{"link_dep with a numeric endpoint", model.OpLinkDep, `{"depends_on_node_id":5,"dep_type":"blocks"}`},
		{"unlink_dep with a numeric endpoint", model.OpUnlinkDep, `{"depends_on_node_id":5}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := validEvent()
			e.OpType, e.NodeID, e.Payload = tt.op, "MTIX-1", json.RawMessage(tt.payload)

			err := validator.ValidateEventNodeIDs(e)

			require.ErrorIs(t, err, model.ErrInvalidInput)
			require.ErrorContains(t, err, "does not decode")
		})
	}
}
