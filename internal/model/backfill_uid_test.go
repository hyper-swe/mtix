// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewBackfillUID_MintsMarkedUIDsThatNeverCollideWithV7 verifies a
// backfill uid (MTIX-95.31.9) is a UUIDv8 carrying the backfill marker,
// distinct on every mint, never a UUIDv7 (the version a create mints), and
// recognized again after a round trip through its text, as an export and
// an import carry it.
func TestNewBackfillUID_MintsMarkedUIDsThatNeverCollideWithV7(t *testing.T) {
	seen := make(map[string]bool)
	for range 200 {
		uid, err := NewBackfillUID()
		require.NoError(t, err)
		parsed, err := uuid.Parse(uid)
		require.NoError(t, err)
		assert.Equal(t, uuid.Version(8), parsed.Version())
		assert.Equal(t, uuid.RFC4122, parsed.Variant())
		assert.Equal(t, strings.ToLower(uid), uid, "stored and exported as lowercase text")
		assert.True(t, IsBackfillUID(uid))
		assert.True(t, IsBackfillUID(parsed.String()), "survives a round trip through its text")
		assert.False(t, seen[uid], "every mint is new")
		seen[uid] = true
	}
}

// TestIsBackfillUID_TellsMarkedFromOtherUIDs verifies only a UUIDv8 with
// the backfill marker is a backfill uid: a create's UUIDv7, a random
// UUIDv4, a UUIDv8 with another marker, and text that is not a uid are not.
func TestIsBackfillUID_TellsMarkedFromOtherUIDs(t *testing.T) {
	marked, err := NewBackfillUID()
	require.NoError(t, err)
	v7, err := uuid.NewV7()
	require.NoError(t, err)
	otherV8 := uuid.MustParse(marked)
	otherV8[7] ^= 0xFF // same version, another marker
	wrongVariant := uuid.MustParse(marked)
	wrongVariant[8] &= 0x3F // not the RFC 4122 variant
	tests := []struct {
		name string
		uid  string
		want bool
	}{
		{"a backfill uid", marked, true},
		{"a backfill uid in upper case", strings.ToUpper(marked), true},
		{"a create's UUIDv7", v7.String(), false},
		{"a random UUIDv4", uuid.NewString(), false},
		{"a UUIDv8 with another marker", otherV8.String(), false},
		{"another variant", wrongVariant.String(), false},
		{"empty", "", false},
		{"not a uid", "01J9NOTAUUIDV70000000000001", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsBackfillUID(tt.uid))
		})
	}
}
