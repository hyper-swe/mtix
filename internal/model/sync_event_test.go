// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/require"
)

func TestOpType_AllTwelveCanonical(t *testing.T) {
	require.Len(t, model.AllOpTypes, 12, "FR-18.6 mandates exactly 12 op_types")
	seen := make(map[model.OpType]struct{}, 12)
	for _, op := range model.AllOpTypes {
		_, dup := seen[op]
		require.False(t, dup, "duplicate op_type %s", op)
		seen[op] = struct{}{}
	}
}

func TestOpType_IsValid(t *testing.T) {
	for _, op := range model.AllOpTypes {
		require.True(t, op.IsValid(), "%s should be valid", op)
	}
	require.False(t, model.OpType("not_an_op").IsValid())
	require.False(t, model.OpType("").IsValid())
	require.False(t, model.OpType("CREATE_NODE").IsValid(), "case-sensitive per SYNC-DESIGN §3.3")
}

func TestSyncStatus_AllFourCanonical(t *testing.T) {
	require.Len(t, model.AllSyncStatuses, 4)
	for _, s := range model.AllSyncStatuses {
		require.True(t, s.IsValid())
	}
}

func TestSyncStatus_IsValid(t *testing.T) {
	require.False(t, model.SyncStatus("done").IsValid(),
		"sync_status is its own enum, NOT the node status")
}

func validEvent() *model.SyncEvent {
	return &model.SyncEvent{
		EventID:           "0193fa00-0000-7000-8000-000000000001",
		ProjectPrefix:     "MTIX",
		NodeID:            "MTIX-1",
		OpType:            model.OpCreateNode,
		Payload:           json.RawMessage(`{"title":"x"}`),
		WallClockTS:       1700000000000,
		LamportClock:      1,
		VectorClock:       model.VectorClock{"alice": 1},
		AuthorID:          "alice",
		AuthorMachineHash: "0123456789abcdef",
		SyncStatus:        model.SyncStatusPending,
		CreatedAt:         time.Unix(0, 1700000000000000000),
	}
}

func TestSyncEvent_Validate_HappyPath(t *testing.T) {
	require.NoError(t, validEvent().Validate())
}

func TestSyncEvent_Validate_RejectsEachField(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*model.SyncEvent)
		wantSub string
	}{
		{"empty event_id", func(e *model.SyncEvent) { e.EventID = "" }, "event_id required"},
		{"unknown op_type", func(e *model.SyncEvent) { e.OpType = "garbage" }, "op_type"},
		{"empty op_type", func(e *model.SyncEvent) { e.OpType = "" }, "op_type"},
		{"unknown sync_status", func(e *model.SyncEvent) { e.SyncStatus = "stuck" }, "sync_status"},
		{"empty node_id", func(e *model.SyncEvent) { e.NodeID = "" }, "node_id required"},
		{"lowercase project_prefix", func(e *model.SyncEvent) { e.ProjectPrefix = "mtix" }, "project_prefix"},
		{"too long project_prefix", func(e *model.SyncEvent) { e.ProjectPrefix = "ABCDEFGHIJKLMNOPQ" }, "project_prefix"},
		{"empty project_prefix", func(e *model.SyncEvent) { e.ProjectPrefix = "" }, "project_prefix"},
		{"uppercase author_id", func(e *model.SyncEvent) { e.AuthorID = "Alice" }, "author_id"},
		{"too long author_id", func(e *model.SyncEvent) {
			e.AuthorID = "a"
			for i := 0; i < 65; i++ {
				e.AuthorID += "a"
			}
		}, "author_id"},
		{"empty author_id", func(e *model.SyncEvent) { e.AuthorID = "" }, "author_id"},
		{"short machine_hash", func(e *model.SyncEvent) { e.AuthorMachineHash = "abc" }, "machine_hash"},
		{"non-hex machine_hash", func(e *model.SyncEvent) { e.AuthorMachineHash = "0123456789abcdeg" }, "machine_hash"},
		{"negative wall_clock_ts", func(e *model.SyncEvent) { e.WallClockTS = -1 }, "wall_clock_ts"},
		{"negative lamport_clock", func(e *model.SyncEvent) { e.LamportClock = -1 }, "lamport_clock"},
		{"empty payload", func(e *model.SyncEvent) { e.Payload = nil }, "payload"},
		{"nil vector_clock", func(e *model.SyncEvent) { e.VectorClock = nil }, "vector_clock"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := validEvent()
			tc.mutate(e)
			err := e.Validate()
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wantSub)
			require.True(t, errors.Is(err, model.ErrInvalidInput),
				"validate errors must wrap ErrInvalidInput so callers can errors.Is")
		})
	}
}

func TestSyncEvent_JSONRoundTripsPerOpType(t *testing.T) {
	for _, op := range model.AllOpTypes {
		t.Run(string(op), func(t *testing.T) {
			e := validEvent()
			e.OpType = op
			e.Payload = json.RawMessage(`{}`)

			b, err := json.Marshal(e)
			require.NoError(t, err)

			var got model.SyncEvent
			require.NoError(t, json.Unmarshal(b, &got))

			require.Equal(t, e.EventID, got.EventID)
			require.Equal(t, e.OpType, got.OpType)
			require.Equal(t, e.LamportClock, got.LamportClock)
			require.Equal(t, e.VectorClock, got.VectorClock)
			require.Equal(t, e.AuthorID, got.AuthorID)
			require.Equal(t, e.AuthorMachineHash, got.AuthorMachineHash)
			require.JSONEq(t, string(e.Payload), string(got.Payload))
		})
	}
}

func TestSyncEvent_JSONOmitsEmptyOptionals(t *testing.T) {
	e := validEvent()
	e.SyncStatus = ""
	e.RetainedUntil = nil

	b, err := json.Marshal(e)
	require.NoError(t, err)

	require.NotContains(t, string(b), "sync_status",
		"empty sync_status should be omitted via omitempty")
	require.NotContains(t, string(b), "retained_until",
		"nil retained_until should be omitted via omitempty")
}
