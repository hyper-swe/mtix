// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/testutil"
)

func TestQuarantineHandle_HostileIDs_AreFixedLengthLowerHexOfTheExactBytes(t *testing.T) {
	for _, f := range testutil.HostileIDs() {
		t.Run(f.Name, func(t *testing.T) {
			h := service.QuarantineHandle(f.Value)
			sum := sha256.Sum256([]byte(f.Value))
			require.Len(t, h, service.QuarantineHandleLen)
			assert.Equal(t, hex.EncodeToString(sum[:])[:service.QuarantineHandleLen], h)
			assert.Equal(t, hex.EncodeToString(sum[:]), service.QuarantineHandleFull(f.Value))
		})
	}
}

func TestMatchQuarantineHandle_Cases_ResolveOrRefuseClearly(t *testing.T) {
	ids := []string{"evt-a", "evt\x00b", "evt-\xff"}
	for _, id := range ids {
		got, err := service.MatchQuarantineHandle(ids, service.QuarantineHandle(id))
		require.NoError(t, err)
		assert.Equal(t, id, got)
		got, err = service.MatchQuarantineHandle(ids, "  "+service.QuarantineHandleFull(id)[:service.QuarantineHandleMin]+"\n")
		require.NoError(t, err, "minimum prefix, case and surrounding space are accepted")
		assert.Equal(t, id, got)
	}
	tests := []struct {
		name, handle string
		wantNotFound bool
	}{
		{"unknown", "deadbeefdeadbeef", true},
		{"too short", service.QuarantineHandleFull("evt-a")[:service.QuarantineHandleMin-1], false},
		{"not hex", "zzzzzzzzzzzz", false},
		{"empty", "", false},
		{"longer than a digest", service.QuarantineHandleFull("evt-a") + "0", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := service.MatchQuarantineHandle(ids, tc.handle)
			require.Error(t, err)
			if tc.wantNotFound {
				assert.ErrorIs(t, err, model.ErrNotFound)
			} else {
				assert.ErrorIs(t, err, model.ErrInvalidInput)
			}
		})
	}
}

// collidingIDs finds two ids whose digests share the first n hex characters.
func collidingIDs(n int) (string, string) {
	seen := map[string]string{}
	for i := 0; ; i++ {
		id := fmt.Sprintf("collide-%d", i)
		key := service.QuarantineHandleFull(id)[:n]
		if other, ok := seen[key]; ok {
			return other, id
		}
		seen[key] = id
	}
}

func TestMatchQuarantineHandle_AmbiguousPrefix_NamesALongerPrefix(t *testing.T) {
	a, b := collidingIDs(service.QuarantineHandleMin)
	ids := []string{a, b}
	prefix := service.QuarantineHandleFull(a)[:service.QuarantineHandleMin]
	_, err := service.MatchQuarantineHandle(ids, prefix)
	require.ErrorIs(t, err, model.ErrInvalidInput)
	assert.Contains(t, err.Error(), "ambiguous")
	assert.Contains(t, err.Error(), "longer")
	// A prefix long enough to separate the two resolves each one.
	for _, id := range ids {
		got, matchErr := service.MatchQuarantineHandle(ids, service.QuarantineHandleFull(id)[:service.QuarantineHandleLen+8])
		require.NoError(t, matchErr)
		assert.Equal(t, id, got)
	}
}
