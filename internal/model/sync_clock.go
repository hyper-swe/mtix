// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"encoding/json"
	"fmt"
	"sort"
)

// VectorClock is the per-author causality counter per SYNC-DESIGN §8.1.
//
// Keys are author_id strings (validated by authorIDPattern). Values are
// monotonically increasing int64 counters bumped on emit. The MarshalJSON
// implementation sorts keys to guarantee determinism (FR-15.3a precedent).
//
// Concurrency: VectorClock is not thread-safe. Callers operate on it
// inside a SQLite transaction (the global lock provides serialization).
type VectorClock map[string]int64

// MaxVectorClockEntries is the FR-18.7 hard cap. The hub validator
// rejects events with more entries than this.
const MaxVectorClockEntries = 100

// MaxVectorClockValue is the per-entry overflow guard per FR-18.7.
// 2^53 keeps headroom below int64 max so future arithmetic does not wrap.
const MaxVectorClockValue = int64(1) << 53

// Bump increments the entry for authorID by 1, creating it at 1 if absent.
// Returns the new value.
func (vc VectorClock) Bump(authorID string) int64 {
	vc[authorID]++
	return vc[authorID]
}

// Merge takes the per-author maximum of vc and other. Returns a new map;
// neither input is mutated. Commutativity (a.Merge(b) == b.Merge(a)) is
// guaranteed by the per-key max operation.
func (vc VectorClock) Merge(other VectorClock) VectorClock {
	out := make(VectorClock, len(vc)+len(other))
	for k, v := range vc {
		out[k] = v
	}
	for k, v := range other {
		if cur, ok := out[k]; !ok || v > cur {
			out[k] = v
		}
	}
	return out
}

// Prune returns vc bounded to MaxVectorClockEntries entries, and the number
// of entries dropped. It is the one place the size cap is enforced for
// local state (MTIX-95.16).
//
// Invariant: a local write never fails because of the vector clock's size.
// The cap is a wire limit (FR-18.7, the hub rejects larger clocks), so the
// local clock is bounded here, before it is persisted or emitted, instead of
// being validated and refused after a bump.
//
// Design choice: drop the authors with the SMALLEST counters first (the
// least active, so least likely to matter for causality), ties broken by
// author id in descending lexical order, so the kept set is a pure function
// of the input and never of map iteration order. The author ids in keep
// (the local author) are never dropped, so the local counter never resets
// and stays monotonic. keep is ordered by priority: if keep alone exceeds the
// cap, the entries listed last are dropped first, so the first keep entry
// (the author being bumped) survives; the result is always valid.
//
// Causality: a dropped author reads as 0 in Dominates, Concurrent and
// Equal (missing keys are 0). That can only make two clocks look
// concurrent or equal where an exact comparison would order them, so
// conflict DETECTION degrades for pruned authors (a conflict can be logged
// that an exact clock would have ordered, or one missed when the dropped
// entries were the only difference). It never changes which write wins:
// convergence is decided by LWW (lamport, wall clock, machine hash), not by the
// vector clock (SYNC-DESIGN §8.1-8.2). Merge stays an exact per-key max;
// only the stored and emitted result is pruned. When len(vc) is within the
// cap vc is returned unchanged (as a copy).
func (vc VectorClock) Prune(keep ...string) (VectorClock, int) {
	out := make(VectorClock, len(vc))
	for k, v := range vc {
		out[k] = v
	}
	excess := len(out) - MaxVectorClockEntries
	if excess <= 0 {
		return out, 0
	}
	rank := make(map[string]int, len(keep))
	for i, k := range keep {
		if _, dup := rank[k]; !dup {
			rank[k] = i
		}
	}
	keys := make([]string, 0, len(out))
	for k := range out {
		keys = append(keys, k)
	}
	// Drop order: unprotected before protected; unprotected by smallest
	// counter, then larger author id first; protected (only when keep alone
	// exceeds the cap) by later position in keep first.
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		ra, pa := rank[a]
		rb, pb := rank[b]
		if pa != pb {
			return !pa
		}
		if pa {
			return ra > rb
		}
		if out[a] != out[b] {
			return out[a] < out[b]
		}
		return a > b
	})
	for _, k := range keys[:excess] {
		delete(out, k)
	}
	return out, excess
}

// Dominates reports whether vc strictly dominates other in the partial
// order: every entry in vc is >= the corresponding entry in other (treating
// missing keys as 0), and at least one entry is strictly greater.
//
// dominates ⇒ causally precedes (in the reverse direction): if A.Dominates(B)
// then B happened-before A.
func (vc VectorClock) Dominates(other VectorClock) bool {
	strictlyGreater := false
	for k, v := range vc {
		ov := other[k]
		if v < ov {
			return false
		}
		if v > ov {
			strictlyGreater = true
		}
	}
	for k, ov := range other {
		if _, ok := vc[k]; ok {
			continue
		}
		if ov > 0 {
			return false
		}
	}
	return strictlyGreater
}

// Concurrent reports whether vc and other are causally concurrent —
// neither dominates the other. Two equal vector clocks are NOT concurrent
// (Concurrent returns false for equal inputs); they are causally identical
// and resolve via the LWW tie-break (SYNC-DESIGN §8.2).
func (vc VectorClock) Concurrent(other VectorClock) bool {
	return !vc.Dominates(other) && !other.Dominates(vc) && !vc.Equal(other)
}

// Equal reports whether vc and other have identical entries (treating
// missing keys as 0).
func (vc VectorClock) Equal(other VectorClock) bool {
	for k, v := range vc {
		if other[k] != v {
			return false
		}
	}
	for k, v := range other {
		if _, ok := vc[k]; !ok && v != 0 {
			return false
		}
	}
	return true
}

// Validate enforces the FR-18.7 caps: <=100 entries, each value < 2^53.
func (vc VectorClock) Validate() error {
	if len(vc) > MaxVectorClockEntries {
		return fmt.Errorf("vector_clock has %d entries (max %d): %w",
			len(vc), MaxVectorClockEntries, ErrInvalidInput)
	}
	for k, v := range vc {
		if v < 0 {
			return fmt.Errorf("vector_clock[%q] negative (%d): %w", k, v, ErrInvalidInput)
		}
		if v >= MaxVectorClockValue {
			return fmt.Errorf("vector_clock[%q] = %d >= 2^53: %w", k, v, ErrInvalidInput)
		}
	}
	return nil
}

// MarshalJSON serializes the map with keys in lexical order so that
// equal vector clocks produce byte-identical JSON. This is critical for
// content_hash determinism on the hub side and for property-test
// stability in MTIX-15.4.
func (vc VectorClock) MarshalJSON() ([]byte, error) {
	if vc == nil {
		return []byte("{}"), nil
	}
	keys := make([]string, 0, len(vc))
	for k := range vc {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	buf := make([]byte, 0, 2+len(vc)*32)
	buf = append(buf, '{')
	for i, k := range keys {
		if i > 0 {
			buf = append(buf, ',')
		}
		kb, err := json.Marshal(k)
		if err != nil {
			return nil, fmt.Errorf("vector_clock key %q: %w", k, err)
		}
		buf = append(buf, kb...)
		buf = append(buf, ':')
		vb, err := json.Marshal(vc[k])
		if err != nil {
			return nil, fmt.Errorf("vector_clock value for %q: %w", k, err)
		}
		buf = append(buf, vb...)
	}
	buf = append(buf, '}')
	return buf, nil
}

// UnmarshalJSON inflates the canonical map form. Accepts both "{}" and
// null as the empty case to interop with PG JSONB null storage.
func (vc *VectorClock) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*vc = VectorClock{}
		return nil
	}
	m := make(map[string]int64)
	if err := json.Unmarshal(b, &m); err != nil {
		return fmt.Errorf("vector_clock unmarshal: %w", err)
	}
	*vc = m
	return nil
}
