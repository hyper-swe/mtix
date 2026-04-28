// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package validator_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/sync/validator"
)

// FuzzEventDecode feeds random bytes into json.Unmarshal targeting
// model.SyncEvent. The contract per FR-18.27: never panic. Errors are
// expected and fine — we are looking for memory-safety violations or
// JSON-decoder corner cases that crash the process.
func FuzzEventDecode(f *testing.F) {
	seedCorpus := [][]byte{
		[]byte(`{"event_id":"x","op_type":"create_node"}`),
		[]byte(`{"vector_clock":{}}`),
		[]byte(`{"payload":null}`),
		[]byte(`{"wall_clock_ts":-1}`),
		[]byte(`{}`),
		[]byte(``),
		[]byte(`null`),
		[]byte(`[1,2,3]`),
	}
	for _, s := range seedCorpus {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		var e model.SyncEvent
		_ = json.Unmarshal(data, &e)
	})
}

// FuzzVectorClockMerge generates random pairs of vector clocks and
// asserts merge commutativity per FR-18.27.
//
// Inputs are JSON strings; we unmarshal them into VectorClocks and
// then test merge(a,b) == merge(b,a).
func FuzzVectorClockMerge(f *testing.F) {
	seeds := []struct{ a, b string }{
		{`{"alice":1}`, `{"bob":2}`},
		{`{}`, `{}`},
		{`{"a":1}`, `{"a":1}`},
		{`{"a":1,"b":2}`, `{"b":3,"c":4}`},
	}
	for _, s := range seeds {
		f.Add(s.a, s.b)
	}
	f.Fuzz(func(t *testing.T, aRaw, bRaw string) {
		var a, b model.VectorClock
		if err := json.Unmarshal([]byte(aRaw), &a); err != nil {
			t.Skip()
		}
		if err := json.Unmarshal([]byte(bRaw), &b); err != nil {
			t.Skip()
		}
		ab := a.Merge(b)
		ba := b.Merge(a)
		// Use VectorClock.Equal to avoid map-iteration-order issues.
		if !ab.Equal(ba) {
			t.Fatalf("merge not commutative: a=%v b=%v\n  a.Merge(b)=%v\n  b.Merge(a)=%v",
				a, b, ab, ba)
		}
	})
}

// FuzzPushEventsValidation feeds random partially-formed events
// through the validator. The contract per FR-18.27: never panic, never
// partially apply. The validator returns either nil (accept) or a
// structured error (reject). Anything else is a bug.
func FuzzPushEventsValidation(f *testing.F) {
	seeds := []struct {
		eventID, opType, payload, authorID, vcJSON string
		wallTS, lamport                             int64
	}{
		{"0193fa00-0000-7000-8000-000000000001", "create_node", `{"title":"x"}`, "alice", `{"alice":1}`, time.Now().UnixMilli(), 1},
		{"", "garbage", `garbage`, "Capital!", `not-json`, -1, -1},
		{"x", "comment", `null`, "", `{}`, 0, 0},
	}
	for _, s := range seeds {
		f.Add(s.eventID, s.opType, s.payload, s.authorID, s.vcJSON, s.wallTS, s.lamport)
	}
	now := time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)
	f.Fuzz(func(t *testing.T, eventID, opType, payload, authorID, vcJSON string, wallTS, lamport int64) {
		var vc model.VectorClock
		if err := json.Unmarshal([]byte(vcJSON), &vc); err != nil {
			vc = model.VectorClock{}
		}
		e := &model.SyncEvent{
			EventID:           eventID,
			ProjectPrefix:     "MTIX",
			NodeID:            "MTIX-1",
			OpType:            model.OpType(opType),
			Payload:           json.RawMessage(payload),
			WallClockTS:       wallTS,
			LamportClock:      lamport,
			VectorClock:       vc,
			AuthorID:          authorID,
			AuthorMachineHash: "0123456789abcdef",
			SyncStatus:        model.SyncStatusPending,
			CreatedAt:         now,
		}
		// Must not panic regardless of input.
		_ = validator.Validate(e, now, nil)
	})
}
