// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/hyper-swe/mtix/internal/model"
)

// WakeHoldStore tracks each registration's own high-water mark and hold flag
// (FR-20.3, ADR-007 §5.4). A store without it dispatches exactly as before.
type WakeHoldStore interface {
	WakeRegistrationMark(context.Context, model.WakeKey) (model.WakeRegistrationMark, error)
	SetWakeHold(ctx context.Context, key model.WakeKey, advanceTo int64, hold bool) error
	// HeldWakeEvent returns the lowest unacknowledged addressed event above the given mark:
	// the event a pending hold protects. found is false when there is none.
	HeldWakeEvent(ctx context.Context, key model.WakeKey, above int64) (seq int64, found bool, err error)
}

// passState carries which registrations were held earlier in one pass. A held event
// blocks later events of the same registration, so the high-water mark only ever
// advances over a contiguous prefix of nudged events and a held event is never skipped.
type passState struct{ blocked map[string]bool }

func holdRegistrationID(key model.WakeKey) string {
	return key.AgentID + "\x00" + key.HostID + "\x00" + key.UID
}

// orderedCandidates returns the candidates in event order without changing the caller's slice.
func orderedCandidates(keys []model.WakeKey) []model.WakeKey {
	ordered := append([]model.WakeKey(nil), keys...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].EventSeq < ordered[j].EventSeq })
	return ordered
}

// eligible applies I10: only events above the registration's own mark and of its own
// epoch may wake; a registration blocked earlier in this pass waits. Untracked keys pass.
func (d *WakeDispatcher) eligible(ctx context.Context, key model.WakeKey, st *passState) (bool, error) {
	if d.holds == nil {
		return true, nil
	}
	if st.blocked[holdRegistrationID(key)] {
		return false, nil
	}
	mark, err := d.holds.WakeRegistrationMark(ctx, key)
	if err != nil {
		return false, fmt.Errorf("read registration high-water: %w", err)
	}
	if !mark.Found {
		return true, nil
	}
	if mark.Epoch != key.Epoch || key.EventSeq <= mark.HighWater {
		return false, nil
	}
	if mark.Held {
		if err := d.checkHeldEventPresent(ctx, key, mark.HighWater); err != nil {
			return false, err
		}
	}
	return true, nil
}

// checkHeldEventPresent refuses an event above a pending hold when a lower event of the
// registration was not handled earlier in the pass (a handled held event lifts the hold, and
// a still-held one blocks the registration before this point): the mark must never move past a held event, even across
// passes (ADR-007 section 2.7). The held event is derived from the store, and a failed
// lookup refuses too, so nothing is sent and the mark and hold stay as they are.
func (d *WakeDispatcher) checkHeldEventPresent(ctx context.Context, key model.WakeKey, mark int64) error {
	held, found, err := d.holds.HeldWakeEvent(ctx, key, mark)
	if err != nil {
		return fmt.Errorf("read held wake event: %w", err)
	}
	if found && key.EventSeq > held {
		return fmt.Errorf("event %d above held event %d: %w", key.EventSeq, held, model.ErrWakeHeldEventMissing)
	}
	return nil
}

// markHeld records the hold on the registration and blocks later events of it this pass.
func (d *WakeDispatcher) markHeld(ctx context.Context, key model.WakeKey, st *passState) error {
	st.blocked[holdRegistrationID(key)] = true
	if d.holds == nil {
		return nil
	}
	if err := d.holds.SetWakeHold(ctx, key, 0, true); err != nil {
		return fmt.Errorf("record wake hold: %w", err)
	}
	return nil
}

// advanced moves the registration's own mark over a nudged event and lifts its hold.
func (d *WakeDispatcher) advanced(ctx context.Context, key model.WakeKey) error {
	if d.holds == nil {
		return nil
	}
	if err := d.holds.SetWakeHold(ctx, key, key.EventSeq, false); err != nil {
		return fmt.Errorf("advance registration high-water: %w", err)
	}
	return nil
}

// passOne evaluates one candidate: guard (including the own-mark re-arm rule), budget,
// send, then mark. The mark moves only over an event that was sent or lost to another
// claimant; a hold or any error blocks the registration's later events and is retried.
func (d *WakeDispatcher) passOne(ctx context.Context, key model.WakeKey, st *passState) error {
	hop, verdict, err := d.guardWakeHop(ctx, key, st)
	if err != nil && verdict != verdictHeld {
		st.blocked[holdRegistrationID(key)] = true
		return err
	}
	switch verdict {
	case verdictSkip:
		return nil
	case verdictHeld:
		return errors.Join(err, d.markHeld(ctx, key, st))
	}
	outcome, sendErr := d.sendWake(ctx, key, hop)
	switch outcome {
	case wakeBudgetHeld:
		return errors.Join(sendErr, d.markHeld(ctx, key, st))
	case wakeNotAttempted, wakeInFlight:
		// Nothing was sent by us: the mark stays so the event is retried or reclaimed later.
		st.blocked[holdRegistrationID(key)] = true
		return sendErr
	}
	// A completed send, even a failed one, is terminal in the ledger, so the mark moves.
	return errors.Join(sendErr, d.advanced(ctx, key))
}
