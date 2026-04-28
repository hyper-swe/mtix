// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package validator_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/sync/validator"
	"github.com/stretchr/testify/require"
)

var refNow = time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)

func validEvent() *model.SyncEvent {
	return &model.SyncEvent{
		EventID:           "0193fa00-0000-7000-8000-000000000001",
		ProjectPrefix:     "MTIX",
		NodeID:            "MTIX-1",
		OpType:            model.OpCreateNode,
		Payload:           json.RawMessage(`{"title":"x"}`),
		WallClockTS:       refNow.UnixMilli(),
		LamportClock:      1,
		VectorClock:       model.VectorClock{"alice": 1},
		AuthorID:          "alice",
		AuthorMachineHash: "0123456789abcdef",
		SyncStatus:        model.SyncStatusPending,
		CreatedAt:         refNow,
	}
}

func TestValidate_HappyPath(t *testing.T) {
	require.NoError(t, validator.Validate(validEvent(), refNow, nil))
}

func TestValidate_NilEvent(t *testing.T) {
	err := validator.Validate(nil, refNow, nil)
	require.Error(t, err)
	require.True(t, errors.Is(err, model.ErrInvalidInput))
}

func TestValidate_DelegatesToModelForGrammarRules(t *testing.T) {
	e := validEvent()
	e.AuthorID = "Capital"
	err := validator.Validate(e, refNow, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "author_id")
}

func TestValidate_RejectsOversizedPayload(t *testing.T) {
	e := validEvent()
	// 65KB payload — over the 64KB cap.
	body := strings.Repeat("a", 65*1024)
	e.Payload = json.RawMessage(`"` + body + `"`)
	err := validator.Validate(e, refNow, nil)
	require.Error(t, err)
	require.True(t, errors.Is(err, validator.ErrPayloadTooLarge))
}

func TestValidate_AcceptsExactlyMaxPayload(t *testing.T) {
	e := validEvent()
	// Exactly at the cap. The +2 for surrounding quotes pushes us over,
	// so build a 65534-byte body to land at 65536 = MaxPayloadBytes.
	body := strings.Repeat("a", validator.MaxPayloadBytes-2)
	e.Payload = json.RawMessage(`"` + body + `"`)
	require.NoError(t, validator.Validate(e, refNow, nil))
}

func TestValidate_RejectsTooDeepPayload(t *testing.T) {
	e := validEvent()
	// Build a JSON object nested 11 levels deep.
	deep := "1"
	for i := 0; i < 11; i++ {
		deep = `{"a":` + deep + `}`
	}
	e.Payload = json.RawMessage(deep)
	err := validator.Validate(e, refNow, nil)
	require.Error(t, err)
	require.True(t, errors.Is(err, validator.ErrPayloadTooNested))
}

func TestValidate_AcceptsExactlyMaxDepth(t *testing.T) {
	e := validEvent()
	deep := "1"
	for i := 0; i < 10; i++ {
		deep = `{"a":` + deep + `}`
	}
	e.Payload = json.RawMessage(deep)
	require.NoError(t, validator.Validate(e, refNow, nil))
}

func TestValidate_RejectsFutureTimestamp(t *testing.T) {
	cases := []struct {
		name  string
		delta time.Duration
	}{
		{"+25h", 25 * time.Hour},
		{"+30d", 30 * 24 * time.Hour},
		{"+1y", 365 * 24 * time.Hour},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := validEvent()
			e.WallClockTS = refNow.Add(tc.delta).UnixMilli()
			err := validator.Validate(e, refNow, nil)
			require.Error(t, err)
			require.True(t, errors.Is(err, validator.ErrTimestampFuture))
		})
	}
}

func TestValidate_AcceptsTimestampWithinFutureGrace(t *testing.T) {
	e := validEvent()
	e.WallClockTS = refNow.Add(23 * time.Hour).UnixMilli()
	require.NoError(t, validator.Validate(e, refNow, nil),
		"23h in future is within the 24h grace and accepted")
}

func TestValidate_AcceptsBackwardsTimestampWithWarning(t *testing.T) {
	e := validEvent()
	e.WallClockTS = refNow.Add(-31 * 24 * time.Hour).UnixMilli()

	res := &validator.Result{}
	require.NoError(t, validator.Validate(e, refNow, res),
		"31d in past is acceptable per FR-18.8 (warn, not error)")
	require.Contains(t, res.StaleTimestamps, e.EventID,
		"stale event_id surfaced in res for caller to log")
}

func TestValidate_BackwardsTimestampRespectsNilResult(t *testing.T) {
	e := validEvent()
	e.WallClockTS = refNow.Add(-31 * 24 * time.Hour).UnixMilli()
	require.NoError(t, validator.Validate(e, refNow, nil),
		"nil res still accepts; caller opted out of warnings")
}

func TestValidate_RejectsLamportOverflow(t *testing.T) {
	e := validEvent()
	e.LamportClock = validator.MaxLamportClock // exactly 2^53 — rejected
	err := validator.Validate(e, refNow, nil)
	require.Error(t, err)
	require.True(t, errors.Is(err, validator.ErrLamportOverflow))
}

func TestValidate_AcceptsLamportJustBelowOverflow(t *testing.T) {
	e := validEvent()
	e.LamportClock = validator.MaxLamportClock - 1
	require.NoError(t, validator.Validate(e, refNow, nil))
}

func TestValidate_RejectsVectorClockOverflow(t *testing.T) {
	e := validEvent()
	e.VectorClock = model.VectorClock{"alice": int64(1) << 53}
	err := validator.Validate(e, refNow, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "vector_clock")
}

func TestValidateBatch_HappyPath(t *testing.T) {
	events := []*model.SyncEvent{validEvent(), validEvent(), validEvent()}
	for i, e := range events {
		e.EventID = "0193fa00-0000-7000-8000-00000000000" + string(rune('1'+i))
	}
	require.NoError(t, validator.ValidateBatch(events, refNow, nil))
}

func TestValidateBatch_AbortsOnFirstFailure(t *testing.T) {
	events := []*model.SyncEvent{validEvent(), validEvent(), validEvent()}
	events[0].EventID = "0193fa00-0000-7000-8000-000000000001"
	events[1].EventID = "0193fa00-0000-7000-8000-000000000002"
	events[2].EventID = "0193fa00-0000-7000-8000-000000000003"
	events[1].WallClockTS = refNow.Add(48 * time.Hour).UnixMilli() // future
	err := validator.ValidateBatch(events, refNow, nil)
	require.Error(t, err)
	require.True(t, errors.Is(err, validator.ErrInvalidBatch))
	require.True(t, errors.Is(err, validator.ErrTimestampFuture),
		"underlying cause MUST be wrapped so callers can errors.Is for both")
	require.Contains(t, err.Error(), "batch index 1",
		"error MUST identify which batch index failed")
}

func TestValidateBatch_StaleObservationsAggregated(t *testing.T) {
	events := []*model.SyncEvent{validEvent(), validEvent()}
	events[0].EventID = "0193fa00-0000-7000-8000-000000000001"
	events[1].EventID = "0193fa00-0000-7000-8000-000000000002"
	events[0].WallClockTS = refNow.Add(-31 * 24 * time.Hour).UnixMilli()
	events[1].WallClockTS = refNow.Add(-32 * 24 * time.Hour).UnixMilli()

	res := &validator.Result{}
	require.NoError(t, validator.ValidateBatch(events, refNow, res))
	require.Len(t, res.StaleTimestamps, 2,
		"both stale events surface in the aggregate result")
}
