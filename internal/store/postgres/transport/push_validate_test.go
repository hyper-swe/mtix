// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/sync/validator"
)

// pushValidateNow is the reference time of the ValidatePushEvent tests.
var pushValidateNow = time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)

// pushValidateEvent is an event PushEvents accepts at pushValidateNow.
func pushValidateEvent() *model.SyncEvent {
	return &model.SyncEvent{
		EventID: "0193fa00-0000-7000-8000-000000000001", ProjectPrefix: "MTIX", NodeID: "MTIX-1",
		OpType: model.OpCreateNode, Payload: json.RawMessage(`{"title":"x"}`),
		WallClockTS: pushValidateNow.UnixMilli(), LamportClock: 1,
		VectorClock: model.VectorClock{"alice": 1}, AuthorID: "alice",
		AuthorMachineHash: "0123456789abcdef", SyncStatus: model.SyncStatusPending,
	}
}

// TestValidatePushEvent_Events_SameVerdictAsBatch: ValidatePushEvent refuses
// exactly the events PushEvents' batch validation refuses, with the same
// sentinel, so push can hold them one by one (MTIX-95.12).
func TestValidatePushEvent_Events_SameVerdictAsBatch(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(e *model.SyncEvent)
		want   error // nil when the event is valid
	}{
		{"valid event", func(*model.SyncEvent) {}, nil},
		{"payload over the wire cap", func(e *model.SyncEvent) {
			e.Payload = json.RawMessage(`{"prompt":"` + strings.Repeat("p", validator.MaxPayloadBytes) + `"}`)
		}, validator.ErrPayloadTooLarge},
		{"payload nested too deep", func(e *model.SyncEvent) {
			e.Payload = json.RawMessage(strings.Repeat("[", 11) + strings.Repeat("]", 11))
		}, validator.ErrPayloadTooNested},
		{"stamped more than 24h ahead", func(e *model.SyncEvent) {
			e.WallClockTS = pushValidateNow.Add(25 * time.Hour).UnixMilli()
		}, validator.ErrTimestampFuture},
		{"Lamport clock at 2^53", func(e *model.SyncEvent) { e.LamportClock = validator.MaxLamportClock },
			validator.ErrLamportOverflow},
		{"malformed author", func(e *model.SyncEvent) { e.AuthorID = "Not Valid" }, model.ErrInvalidInput},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := pushValidateEvent()
			tt.mutate(e)
			got := transport.ValidatePushEvent(e, pushValidateNow)
			batch := validator.ValidateBatch([]*model.SyncEvent{e}, pushValidateNow, nil)
			if tt.want == nil {
				require.NoError(t, got)
				require.NoError(t, batch)
				return
			}
			require.ErrorIs(t, got, tt.want)
			require.ErrorIs(t, batch, tt.want, "the batch validation refuses it for the same reason")
		})
	}
}
