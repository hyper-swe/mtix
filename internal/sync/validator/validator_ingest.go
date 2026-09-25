// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package validator

import (
	"errors"
	"fmt"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// Ingest-side checks (MTIX-95.11, review F-02, M21). The hub validates an
// event when it is pushed, but a replica cannot trust that every hub row
// passed that check: a row written by an older or modified client, or
// directly into the database, reaches `mtix sync pull` unchecked. Pull
// therefore runs the same FR-18.7 envelope validation on every event before
// it applies it (ValidateIngest), and bounds how far one event may move the
// local Lamport clock (CheckLamportJump). An event that fails either is
// quarantined by the caller and never applied.

// DefaultMaxLamportJump is the default of the sync.max_lamport_jump config
// key: how far above the local Lamport clock a pulled event may be stamped
// before pull quarantines it instead of applying it.
//
// A Lamport clock grows by one per event along a causal chain, so a gap
// between a replica's clock and an incoming event is bounded by the number
// of events the replica has not yet seen. 2^32 (about four billion unseen
// events) is far beyond any real team's history, so no legitimate event is
// refused. It is also 2^21 times smaller than MaxLamportClock: no single
// event can carry a replica's clock anywhere near the FR-18.7 overflow
// guard, after which every event the replica emits would be refused at
// push.
const DefaultMaxLamportJump = int64(1) << 32

// Ingest sentinels (MTIX-95.11). ErrLamportJump marks an event stamped
// more than sync.max_lamport_jump above the local Lamport clock.
// ErrMalformedEvent marks an event whose hub row did not fully decode
// (model.SyncEvent.Malformed set by the transport).
var (
	ErrLamportJump    = errors.New("lamport_clock jump beyond sync.max_lamport_jump")
	ErrMalformedEvent = errors.New("hub row does not decode")
)

// ValidateIngest runs the FR-18.7 envelope validation on an event a
// replica pulled from the hub (MTIX-95.11): every rule Validate enforces
// (grammar, payload size and depth, Lamport overflow, vector-clock caps),
// with the same sentinels. The clock-relative FR-18.8 rule differs: an
// event stamped more than FutureTimestampGrace ahead of now is accepted
// and its id is appended to res.FutureTimestamps, so the caller can warn
// (review F-25). The hub refuses such an event at push, but this
// machine's clock may be the one that is wrong, and refusing a hub event
// at ingest would diverge this replica from its peers. Stale timestamps
// are reported in res.StaleTimestamps as by Validate. A nil res opts out
// of both observations. An event whose hub row did not decode (Malformed
// set) is rejected first, with ErrMalformedEvent.
func ValidateIngest(e *model.SyncEvent, now time.Time, res *Result) error {
	if e != nil && e.Malformed != "" {
		return fmt.Errorf("event %s: %s: %w", e.EventID, e.Malformed, ErrMalformedEvent)
	}
	if err := validateEnvelope(e); err != nil {
		return err
	}
	if res == nil {
		return nil
	}
	wallTS := time.UnixMilli(e.WallClockTS).UTC()
	if wallTS.After(now.Add(FutureTimestampGrace)) {
		res.FutureTimestamps = append(res.FutureTimestamps, e.EventID)
	}
	if wallTS.Before(now.Add(-PastTimestampWarn)) {
		res.StaleTimestamps = append(res.StaleTimestamps, e.EventID)
	}
	return nil
}

// CheckLamportJump refuses an event whose Lamport clock is more than
// maxJump above the local clock (MTIX-95.11): applying it would advance
// the local clock by that much. It returns an error wrapping ErrLamportJump
// for such an event, and nil for one at most maxJump above, equal to or
// below the local clock. A negative local clock (corrupted state) counts
// as zero. maxJump must be positive; a bound that is not refuses every
// event above the local clock with model.ErrInvalidInput, so a bad bound
// can never let an event through. The subtraction cannot overflow: both
// operands are non-negative.
func CheckLamportJump(lamport, local, maxJump int64) error {
	if local < 0 {
		local = 0
	}
	if lamport <= local {
		return nil
	}
	if maxJump <= 0 {
		return fmt.Errorf("sync.max_lamport_jump %d is not a positive integer: %w",
			maxJump, model.ErrInvalidInput)
	}
	if lamport-local > maxJump {
		return fmt.Errorf("lamport_clock %d is %d above the local clock %d (sync.max_lamport_jump %d): %w",
			lamport, lamport-local, local, maxJump, ErrLamportJump)
	}
	return nil
}

// CheckIngestClock is the clock check pull runs first on every event
// (MTIX-95.11): a Lamport clock at or above MaxLamportClock is refused with
// ErrLamportOverflow, whatever the bound, and one more than maxJump above
// the local clock with ErrLamportJump (CheckLamportJump). It runs before
// the other envelope rules, so an event with an extreme clock is always
// recorded as refused for its clock, whatever else is wrong with it.
func CheckIngestClock(lamport, local, maxJump int64) error {
	if lamport >= MaxLamportClock {
		return fmt.Errorf("lamport_clock %d (max %d): %w", lamport, MaxLamportClock-1, ErrLamportOverflow)
	}
	return CheckLamportJump(lamport, local, maxJump)
}
