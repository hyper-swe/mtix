// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/sync/validator"
	"github.com/stretchr/testify/require"
)

// PG-free unit tests for the push/pull surface: nil-pool guards,
// empty-input handling, validation reject-before-PG behavior. The
// happy-path round-trip lives in integration_test.go and skips when
// MTIX_PG_TEST_DSN is unset.

func TestPushEvents_NilPool(t *testing.T) {
	var p *transport.Pool
	valid := &model.SyncEvent{
		EventID:           "0193fa00-0000-7000-8000-000000000001",
		ProjectPrefix:     "MTIX",
		NodeID:            "MTIX-1",
		OpType:            model.OpCreateNode,
		Payload:           json.RawMessage(`{}`),
		WallClockTS:       time.Now().UnixMilli(),
		LamportClock:      1,
		VectorClock:       model.VectorClock{"alice": 1},
		AuthorID:          "alice",
		AuthorMachineHash: "0123456789abcdef",
	}
	_, _, err := p.PushEvents(context.Background(), []*model.SyncEvent{valid})
	require.Error(t, err)
	require.Contains(t, err.Error(), "pool not open")
}

func TestPushEvents_EmptyBatchIsNoop(t *testing.T) {
	var p *transport.Pool
	// Even nil pool must not error on empty batch — the contract is
	// that an empty batch is a degenerate happy path; nothing to do.
	ids, conf, err := p.PushEvents(context.Background(), nil)
	require.NoError(t, err)
	require.Nil(t, ids)
	require.Nil(t, conf)
}

func TestPushEvents_ValidationFailsBeforeAnyPG(t *testing.T) {
	// Construct a pool wrapper with no PG connection; validation runs
	// BEFORE the pool is touched, so this test passes without PG.
	p := &transport.Pool{}

	bad := &model.SyncEvent{
		// missing event_id, op_type, etc — fails Validate before any
		// PG call would be made.
	}
	_, _, err := p.PushEvents(context.Background(), []*model.SyncEvent{bad})
	require.Error(t, err)
	require.Contains(t, err.Error(), "validate")
}

func TestPushEvents_FutureTimestampRejectedBeforePG(t *testing.T) {
	p := &transport.Pool{}
	e := &model.SyncEvent{
		EventID:           "0193fa00-0000-7000-8000-000000000001",
		ProjectPrefix:     "MTIX",
		NodeID:            "MTIX-1",
		OpType:            model.OpCreateNode,
		Payload:           json.RawMessage(`{}`),
		WallClockTS:       time.Now().Add(48 * time.Hour).UnixMilli(),
		LamportClock:      1,
		VectorClock:       model.VectorClock{"alice": 1},
		AuthorID:          "alice",
		AuthorMachineHash: "0123456789abcdef",
	}
	_, _, err := p.PushEvents(context.Background(), []*model.SyncEvent{e})
	require.Error(t, err)
	require.ErrorIs(t, err, validator.ErrTimestampFuture,
		"future-timestamp rejection must wrap the validator sentinel")
}

func TestPullEvents_NilPool(t *testing.T) {
	var p *transport.Pool
	_, _, err := p.PullEvents(context.Background(), 0, 10)
	require.Error(t, err)
	require.Contains(t, err.Error(), "pool not open")
}

func TestPullEvents_LimitMustBePositive(t *testing.T) {
	p := &transport.Pool{}
	_, _, err := p.PullEvents(context.Background(), 0, 0)
	require.Error(t, err)
	require.Contains(t, err.Error(), "limit must be > 0")

	_, _, err = p.PullEvents(context.Background(), 0, -5)
	require.Error(t, err)
	require.Contains(t, err.Error(), "limit must be > 0")
}

func TestConflictDescriptor_JSONShape(t *testing.T) {
	d := transport.ConflictDescriptor{
		NewEventID:         "e1",
		ConflictingEventID: "e2",
		NodeID:             "MTIX-1",
		FieldName:          "title",
	}
	b, err := json.Marshal(d)
	require.NoError(t, err)
	require.JSONEq(t,
		`{"new_event_id":"e1","conflicting_event_id":"e2","node_id":"MTIX-1","field_name":"title"}`,
		string(b))
}

func TestConflictDescriptor_OmitsEmptyFieldName(t *testing.T) {
	d := transport.ConflictDescriptor{
		NewEventID:         "e1",
		ConflictingEventID: "e2",
		NodeID:             "MTIX-1",
	}
	b, err := json.Marshal(d)
	require.NoError(t, err)
	require.NotContains(t, string(b), "field_name",
		"empty field_name (set_acceptance / set_prompt) is omitted from output")
}
