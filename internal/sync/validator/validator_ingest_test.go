// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package validator_test

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/sync/validator"
)

// Tests of the ingest-side checks a replica runs on every pulled event
// (MTIX-95.11): the FR-18.7 envelope validation with its caps, where the
// clock-relative FR-18.8 future-timestamp rule only warns (review F-25),
// and the Lamport jump bound (sync.max_lamport_jump).

// TestValidateIngest_EnvelopeCapsAndGrammars_Rejected: every rule Validate
// enforces that does not depend on this machine's clock rejects the event
// at ingest too, with the same sentinel.
func TestValidateIngest_EnvelopeCapsAndGrammars_Rejected(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(e *model.SyncEvent)
		want   error
	}{
		{"payload over 64 KB", func(e *model.SyncEvent) {
			e.Payload = json.RawMessage(`"` + strings.Repeat("a", validator.MaxPayloadBytes) + `"`)
		}, validator.ErrPayloadTooLarge},
		{"payload nested deeper than 10", func(e *model.SyncEvent) {
			e.Payload = json.RawMessage(strings.Repeat("[", 11) + strings.Repeat("]", 11))
		}, validator.ErrPayloadTooNested},
		{"lamport at 2^53", func(e *model.SyncEvent) {
			e.LamportClock = validator.MaxLamportClock
		}, validator.ErrLamportOverflow},
		{"vector clock with 101 entries", func(e *model.SyncEvent) {
			vc := model.VectorClock{}
			for i := 0; i <= model.MaxVectorClockEntries; i++ {
				vc[fmt.Sprintf("author%d", i)] = 1
			}
			e.VectorClock = vc
		}, model.ErrInvalidInput},
		{"vector clock entry at 2^53", func(e *model.SyncEvent) {
			e.VectorClock = model.VectorClock{"alice": model.MaxVectorClockValue}
		}, model.ErrInvalidInput},
		{"author_id grammar", func(e *model.SyncEvent) { e.AuthorID = "Alice!" }, model.ErrInvalidInput},
		{"author_machine_hash grammar", func(e *model.SyncEvent) { e.AuthorMachineHash = "XYZ" }, model.ErrInvalidInput},
		{"project_prefix grammar", func(e *model.SyncEvent) { e.ProjectPrefix = "lower" }, model.ErrInvalidInput},
		{"op_type not canonical", func(e *model.SyncEvent) { e.OpType = "not_an_op" }, model.ErrInvalidInput},
		{"nil event", nil, model.ErrInvalidInput},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var e *model.SyncEvent
			if tt.mutate != nil {
				e = validEvent()
				tt.mutate(e)
			}
			var res validator.Result

			err := validator.ValidateIngest(e, refNow, &res)

			require.ErrorIs(t, err, tt.want)
			require.Empty(t, res.FutureTimestamps)
		})
	}
}

// TestValidateIngest_FutureTimestamp_WarnsNotRejected: an event stamped more
// than 24h ahead of this machine's clock is accepted at ingest and its id is
// reported in FutureTimestamps; exactly at the grace it is not reported. A
// nil Result opts out of the warning without changing the verdict. Push-side
// Validate still rejects the same event.
func TestValidateIngest_FutureTimestamp_WarnsNotRejected(t *testing.T) {
	tests := []struct {
		name     string
		ahead    time.Duration
		wantWarn bool
	}{
		{"48h ahead", 48 * time.Hour, true},
		{"24h and 1ms ahead", validator.FutureTimestampGrace + time.Millisecond, true},
		{"exactly 24h ahead", validator.FutureTimestampGrace, false},
		{"now", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := validEvent()
			e.WallClockTS = refNow.Add(tt.ahead).UnixMilli()
			var res validator.Result

			require.NoError(t, validator.ValidateIngest(e, refNow, &res))
			require.NoError(t, validator.ValidateIngest(e, refNow, nil), "a nil Result changes nothing")

			if tt.wantWarn {
				require.Equal(t, []string{e.EventID}, res.FutureTimestamps)
				require.ErrorIs(t, validator.Validate(e, refNow, nil), validator.ErrTimestampFuture,
					"the hub still refuses it at push")
				return
			}
			require.Empty(t, res.FutureTimestamps)
		})
	}
}

// TestValidateIngest_StaleTimestamp_RecordedAsAtPush: an event older than
// 30 days is accepted and reported in StaleTimestamps, as at push.
func TestValidateIngest_StaleTimestamp_RecordedAsAtPush(t *testing.T) {
	e := validEvent()
	e.WallClockTS = refNow.Add(-validator.PastTimestampWarn - time.Hour).UnixMilli()
	var res validator.Result

	require.NoError(t, validator.ValidateIngest(e, refNow, &res))
	require.Equal(t, []string{e.EventID}, res.StaleTimestamps)
	require.Empty(t, res.FutureTimestamps)
}

// TestCheckLamportJump_BeyondBound_ReturnsErrLamportJump: an event stamped
// more than maxJump above the local clock is refused; one exactly maxJump
// above it, below it, or equal to it passes. A corrupted negative local
// clock counts as zero, and a bound that is not positive refuses every
// event above the local clock rather than letting it through.
func TestCheckLamportJump_BeyondBound_ReturnsErrLamportJump(t *testing.T) {
	tests := []struct {
		name                  string
		lamport, local, bound int64
		want                  error
	}{
		{"exactly the bound above", 110, 100, 10, nil},
		{"one past the bound", 111, 100, 10, validator.ErrLamportJump},
		{"below the local clock", 5, 100, 10, nil},
		{"equal to the local clock", 100, 100, 10, nil},
		{"default bound, fresh replica, extreme clock", validator.MaxLamportClock - 1, 0,
			validator.DefaultMaxLamportJump, validator.ErrLamportJump},
		{"default bound, exactly 2^32 above", validator.DefaultMaxLamportJump + 7, 7,
			validator.DefaultMaxLamportJump, nil},
		{"negative local clock counts as zero", 11, -5, 10, validator.ErrLamportJump},
		{"negative local clock, within bound of zero", 10, math.MinInt64, 10, nil},
		{"zero bound", 101, 100, 0, model.ErrInvalidInput},
		{"negative bound", 101, 100, -1, model.ErrInvalidInput},
		{"non-positive bound, event not above local", 100, 100, 0, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validator.CheckLamportJump(tt.lamport, tt.local, tt.bound)
			if tt.want == nil {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, tt.want)
		})
	}
}

// TestDefaultMaxLamportJump_Is2To32: the gate-1 default of
// sync.max_lamport_jump.
func TestDefaultMaxLamportJump_Is2To32(t *testing.T) {
	require.Equal(t, int64(4294967296), validator.DefaultMaxLamportJump)
}

// TestCheckIngestClock_OverflowOrJump_Refused: the clock check pull runs
// first on every event (MTIX-95.11 round 2): a clock at or above 2^53 is
// refused with ErrLamportOverflow whatever the bound, one more than maxJump
// above the local clock with ErrLamportJump, and anything else passes.
func TestCheckIngestClock_OverflowOrJump_Refused(t *testing.T) {
	tests := []struct {
		name                  string
		lamport, local, bound int64
		want                  error
	}{
		{"at 2^53, huge bound", validator.MaxLamportClock, validator.MaxLamportClock - 1, math.MaxInt64,
			validator.ErrLamportOverflow},
		{"above 2^53", validator.MaxLamportClock + 5, 0, validator.DefaultMaxLamportJump,
			validator.ErrLamportOverflow},
		{"just below 2^53, local close", validator.MaxLamportClock - 1, validator.MaxLamportClock - 2,
			validator.DefaultMaxLamportJump, nil},
		{"jump beyond the bound", 1 << 40, 0, validator.DefaultMaxLamportJump, validator.ErrLamportJump},
		{"within the bound", 42, 0, validator.DefaultMaxLamportJump, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validator.CheckIngestClock(tt.lamport, tt.local, tt.bound)
			if tt.want == nil {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, tt.want)
		})
	}
}

// TestValidateIngest_MalformedHubRow_Rejected: an event the transport could
// not fully decode from its hub row (Malformed set) is rejected at ingest
// with ErrMalformedEvent and the decode note, so pull quarantines it.
func TestValidateIngest_MalformedHubRow_Rejected(t *testing.T) {
	e := validEvent()
	e.Malformed = "vector_clock does not decode: json: cannot unmarshal string"

	err := validator.ValidateIngest(e, refNow, nil)

	require.ErrorIs(t, err, validator.ErrMalformedEvent)
	require.ErrorContains(t, err, "vector_clock does not decode")
}
