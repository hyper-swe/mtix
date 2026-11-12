// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// CloneEvidence is what a comparison may know about how the hub clone got its
// values, beyond the exported documents (MTIX-112.17.16.19.1). The zero value
// and nil mean none: every field then compares exactly.
//
// LegacyClosedAt maps a node id to the closed_at the clone stamped from a plain
// terminal event written before closed_at was carried in the payload (a plain
// 0.5.4 done). The author's store stamped the same node from a separate clock
// read, so the two stamps can differ by the second boundary alone.
type CloneEvidence struct {
	LegacyClosedAt map[string]string
}

// skewedLegacyClosedAt reports whether a closed_at difference on a node is the
// one-second clock skew of a plain old terminal event and nothing else. It
// holds only when all of these do: the clone's stamp is the one a legacy event
// produced (evidence), both stamps are present and parse, they are no more than
// one second apart, and both sides hold the same terminal status. An event that
// carries closed_at, a longer gap, a missing value or another status is a real
// difference (FR-18.9; MTIX-112.17.16.19).
func (c *boardCmp) skewedLegacyClosedAt(id string, a, b map[string]any) bool {
	stamp, ok := c.evidence.legacyStamp(id)
	if !ok {
		return false
	}
	clone, okClone := b["closed_at"].(string)
	store, okStore := a["closed_at"].(string)
	if !okClone || !okStore || clone != stamp || !sameJSON(a["status"], b["status"]) {
		return false
	}
	if status, _ := a["status"].(string); !model.Status(status).IsTerminal() {
		return false
	}
	return withinOneSecond(store, clone)
}

func (e *CloneEvidence) legacyStamp(id string) (string, bool) {
	if e == nil {
		return "", false
	}
	stamp, ok := e.LegacyClosedAt[id]
	return stamp, ok
}

func withinOneSecond(x, y string) bool {
	tx, errX := time.Parse(time.RFC3339, x)
	ty, errY := time.Parse(time.RFC3339, y)
	if errX != nil || errY != nil {
		return false
	}
	gap := tx.Sub(ty)
	return gap >= -time.Second && gap <= time.Second
}
