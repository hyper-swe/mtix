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
