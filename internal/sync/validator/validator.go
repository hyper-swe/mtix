// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Package validator enforces the FR-18.7 / SYNC-DESIGN section 5.1
// hub-side validation rules against incoming sync events.
//
// Layered on top of model.SyncEvent.Validate (which handles non-empty
// fields, regex grammar, sign checks): this package adds the
// time-dependent and size-dependent rules the model package cannot
// evaluate in isolation:
//
//   - Payload <= 64KB serialized (MaxPayloadBytes).
//   - Payload JSON nesting depth <= 10 (MaxPayloadNestingDepth).
//   - Wall-clock timestamp not more than 24h in the future
//     (clock-skew abuse mitigation per FR-18.8).
//   - Wall-clock timestamp warning (not error) when more than 30d
//     in the past — legitimate replay from offline laptops is allowed.
//   - Lamport clock < 2^53 (overflow protection per FR-18.7).
//
// MTIX-15.3.3's PushEvents calls ValidateBatch before any database
// touch; the entire batch fails atomically on the first invalid event.
package validator

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// MaxPayloadBytes is the FR-18.7 hard cap on serialized payload size.
const MaxPayloadBytes = 64 * 1024

// MaxPayloadNestingDepth is the FR-18.7 cap on JSON nesting depth.
// Mitigates a stack-blowing payload from a hostile or buggy client.
const MaxPayloadNestingDepth = 10

// MaxLamportClock is the FR-18.7 overflow guard. Same value as the
// per-vector-clock-entry cap; keeps headroom below int64 max.
const MaxLamportClock = int64(1) << 53

// FutureTimestampGrace is the FR-18.8 forward window. Events with
// wall_clock_ts beyond now+grace are rejected.
const FutureTimestampGrace = 24 * time.Hour

// PastTimestampWarn is the FR-18.8 backward warning threshold. Events
// older than this are accepted (legitimate offline replay) but the
// caller is invited to log a WARN.
const PastTimestampWarn = 30 * 24 * time.Hour

// Sentinel errors. Callers MUST use errors.Is to dispatch — the wrapped
// strings are informational and may include the failing event_id.
var (
	ErrPayloadTooLarge   = errors.New("payload too large")
	ErrPayloadTooNested  = errors.New("payload nesting too deep")
	ErrTimestampFuture   = errors.New("wall_clock_ts too far in future")
	ErrLamportOverflow   = errors.New("lamport_clock at or above 2^53")
	ErrInvalidBatch      = errors.New("invalid event in batch")
)

// Result lets callers know about non-fatal observations such as a stale
// timestamp that warranted a warning but did not cause rejection.
type Result struct {
	StaleTimestamps []string // event_ids with wall_clock_ts < now - PastTimestampWarn
}

// Validate runs every FR-18.7 rule against e. Returns nil iff all rules
// pass. Wraps the appropriate sentinel error on failure so callers can
// errors.Is for structured handling.
//
// now is the reference for time-bound checks. Callers SHOULD pass
// time.Now().UTC() in production; tests inject a fixed instant.
//
// Stale-but-acceptable timestamps surface in res.StaleTimestamps when
// res != nil (callers passing nil opt out of warnings).
func Validate(e *model.SyncEvent, now time.Time, res *Result) error {
	if e == nil {
		return fmt.Errorf("event nil: %w", model.ErrInvalidInput)
	}
	if err := e.Validate(); err != nil {
		return err
	}

	if len(e.Payload) > MaxPayloadBytes {
		return fmt.Errorf("event %s payload %d bytes (max %d): %w",
			e.EventID, len(e.Payload), MaxPayloadBytes, ErrPayloadTooLarge)
	}
	if depth := jsonDepth(e.Payload); depth > MaxPayloadNestingDepth {
		return fmt.Errorf("event %s payload depth %d (max %d): %w",
			e.EventID, depth, MaxPayloadNestingDepth, ErrPayloadTooNested)
	}

	if e.LamportClock >= MaxLamportClock {
		return fmt.Errorf("event %s lamport %d (max %d): %w",
			e.EventID, e.LamportClock, MaxLamportClock-1, ErrLamportOverflow)
	}
	if err := e.VectorClock.Validate(); err != nil {
		return fmt.Errorf("event %s vector_clock: %w", e.EventID, err)
	}

	tsMS := e.WallClockTS
	wallTS := time.UnixMilli(tsMS).UTC()
	if wallTS.After(now.Add(FutureTimestampGrace)) {
		return fmt.Errorf("event %s wall_clock_ts %s > now+%s: %w",
			e.EventID, wallTS.Format(time.RFC3339), FutureTimestampGrace, ErrTimestampFuture)
	}
	if wallTS.Before(now.Add(-PastTimestampWarn)) && res != nil {
		res.StaleTimestamps = append(res.StaleTimestamps, e.EventID)
	}

	return nil
}

// ValidateBatch runs Validate against every event in the slice. Returns
// the first failing error wrapped with ErrInvalidBatch so callers can
// errors.Is to detect the batch-level abort. Does NOT continue past the
// first failure — partial-batch acceptance is forbidden by FR-18.7.
//
// res aggregates non-fatal observations across the batch.
func ValidateBatch(events []*model.SyncEvent, now time.Time, res *Result) error {
	for i, e := range events {
		if err := Validate(e, now, res); err != nil {
			return fmt.Errorf("batch index %d: %w: %w", i, ErrInvalidBatch, err)
		}
	}
	return nil
}

// jsonDepth returns the maximum nesting depth of a JSON byte slice.
// 0 = invalid JSON or scalar. 1 = top-level object/array. Depth grows
// by 1 for each nested object or array.
//
// Implemented with json.Decoder so we never materialize the full tree
// — the FR-18.7 cap is reached well before allocator pressure becomes
// a concern, but the streaming approach is also robust against
// adversarial input.
func jsonDepth(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	maxDepth := 0
	cur := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		if d, ok := tok.(json.Delim); ok {
			if d == '{' || d == '[' {
				cur++
				if cur > maxDepth {
					maxDepth = cur
				}
			} else {
				cur--
			}
		}
	}
	return maxDepth
}
