// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model_test

import (
	"encoding/json"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/require"
)

// TestSyncEvent_UID_OmitemptyEnvelope asserts the ADR-003 §3/§7 Phase 3
// DUAL-CARRY contract on the wire shape: the new uid field rides the
// SyncEvent envelope with `omitempty`. An event with no uid MUST NOT
// emit a "uid" key (so a pre-30.6 hub/CLI sees byte-for-byte the old
// shape); an event WITH a uid MUST emit it.
func TestSyncEvent_UID_OmitemptyEnvelope(t *testing.T) {
	withoutUID := &model.SyncEvent{
		EventID:           "evt-1",
		ProjectPrefix:     "MTIX",
		NodeID:            "MTIX-1",
		OpType:            model.OpCreateNode,
		Payload:           json.RawMessage(`null`),
		VectorClock:       model.VectorClock{"alice": 1},
		AuthorID:          "alice",
		AuthorMachineHash: "0123456789abcdef",
	}
	b, err := json.Marshal(withoutUID)
	require.NoError(t, err)
	require.NotContains(t, string(b), `"uid"`,
		"omitempty is load-bearing: an event with no uid must serialize the legacy shape")

	withUID := *withoutUID
	withUID.UID = "evt-1"
	b2, err := json.Marshal(&withUID)
	require.NoError(t, err)
	require.Contains(t, string(b2), `"uid":"evt-1"`,
		"a uid-bearing event must carry uid on the wire")
}

// TestSyncEvent_OldCLI_IgnoresUnknownUID is the REQUIRED
// "old-CLI-ignores-unknown" corner case (ADR-003 §7 Phase 3): a
// uid-bearing event JSON, when unmarshaled by code that does NOT know
// the uid field, parses with NO error and node_id still drives apply.
//
// We simulate the old CLI with a struct that has every legacy field but
// NOT uid — exactly what Go's json does when an old binary unmarshals a
// new envelope: the unknown key is silently dropped, never an error.
func TestSyncEvent_OldCLI_IgnoresUnknownUID(t *testing.T) {
	// A wire event carrying both node_id and the new uid.
	wire := `{
		"event_id":"evt-1","project_prefix":"MTIX","node_id":"MTIX-1",
		"op_type":"create_node","payload":null,"wall_clock_ts":1,
		"lamport_clock":1,"vector_clock":{"alice":1},"author_id":"alice",
		"author_machine_hash":"0123456789abcdef","uid":"evt-1"
	}`

	// The legacy decoder: a SyncEvent WITHOUT a uid field.
	type legacySyncEvent struct {
		EventID string       `json:"event_id"`
		NodeID  string       `json:"node_id"`
		OpType  model.OpType `json:"op_type"`
	}
	var legacy legacySyncEvent
	require.NoError(t, json.Unmarshal([]byte(wire), &legacy),
		"old CLI must unmarshal a uid-bearing event with no error (unknown field dropped)")
	require.Equal(t, "MTIX-1", legacy.NodeID,
		"node_id still drives the old CLI's apply path")

	// The new decoder sees both.
	var current model.SyncEvent
	require.NoError(t, json.Unmarshal([]byte(wire), &current))
	require.Equal(t, "MTIX-1", current.NodeID)
	require.Equal(t, "evt-1", current.UID)
}
