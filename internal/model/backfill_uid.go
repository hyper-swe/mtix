// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"

	"github.com/google/uuid"
)

// A backfill uid is the uid mtix gives a node that reached the store without
// one (MTIX-95.31.9, FR-7.8): a node an import inserts from a board written
// before uids were shared, or one a store still holds without a uid when it
// opens. It is a UUIDv8 (RFC 9562) whose 12 bits after the version hold the
// fixed marker 0xBF1 ("backfill"); the other 120 bits, variant aside, are
// random. A create mints a UUIDv7, so the two can never be equal, and the
// marker lets the identity rule recognize a backfill uid without reading
// any time from it. The uid is stored and exported as its lowercase text,
// which an import keeps unchanged.
const (
	backfillMarkerHigh = 0x0B // low nibble of byte 6, after the version
	backfillMarkerLow  = 0xF1 // byte 7
)

// NewBackfillUID mints a backfill uid (MTIX-95.31.9): random, version 8,
// the RFC 4122 variant and the backfill marker.
func NewBackfillUID() (string, error) {
	u, err := uuid.NewRandom()
	if err != nil {
		return "", fmt.Errorf("mint a backfill uid: %w", err)
	}
	u[6] = 0x80 | backfillMarkerHigh // version 8, then the marker's high nibble
	u[7] = backfillMarkerLow
	u[8] = u[8]&0x3F | 0x80 // the RFC 4122 variant
	return u.String(), nil
}

// IsBackfillUID reports whether uid is a backfill uid (MTIX-95.31.9): a
// UUIDv8 of the RFC 4122 variant that carries the backfill marker.
func IsBackfillUID(uid string) bool {
	u, err := uuid.Parse(uid)
	if err != nil {
		return false
	}
	return u.Version() == 8 && u.Variant() == uuid.RFC4122 &&
		u[6]&0x0F == backfillMarkerHigh && u[7] == backfillMarkerLow
}
