// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestReviewR1_WireEquivalencePreservesOffsetAndIgnoresTransientFields(t *testing.T) {
	utc := alertTime().UTC()
	offset := utc.In(time.FixedZone("synthetic-offset", 19800))
	sameWire := utc.In(time.FixedZone("synthetic-utc", 0))
	a := Confirmation{Time: utc}
	b := a
	b.Time = offset
	require.False(t, sameConfirmation(a, b), "distinct serialized offsets remain contradictory")
	b.Time = sameWire
	b.Pending = true
	require.True(t, sameConfirmation(a, b), "location names and transient flags are not JSON state")
	first := permissionFixtureBatch()
	first.Time = utc
	second := first
	second.Time = offset
	require.False(t, samePermissionBatch(first, second), "immutable canonical wire time includes its offset")
	second.Time = sameWire
	require.True(t, samePermissionBatch(first, second))
}
