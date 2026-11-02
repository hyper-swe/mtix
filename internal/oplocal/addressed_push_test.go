// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
//go:build !windows

package oplocal

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAddressedPush_RecordThenRead_RoundTrips(t *testing.T) {
	s := fixture(t)
	key := BoardKey("/work/a/.mtix")
	_, present, err := s.ReadAddressedPush(key)
	require.NoError(t, err)
	require.False(t, present)

	at := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	require.NoError(t, s.RecordAddressedPush(key, AddressedPush{At: at, OK: false, ErrorClass: "timeout"}))
	got, present, err := s.ReadAddressedPush(key)
	require.NoError(t, err)
	require.True(t, present)
	require.True(t, at.Equal(got.At))
	require.False(t, got.OK)
	require.Equal(t, "timeout", got.ErrorClass)

	got.Shown = true
	require.NoError(t, s.RecordAddressedPush(key, got))
	again, _, err := s.ReadAddressedPush(key)
	require.NoError(t, err)
	require.True(t, again.Shown)
}

func TestAddressedPush_InvalidInputs_Refused(t *testing.T) {
	s := fixture(t)
	tests := []struct {
		name string
		key  string
		rec  AddressedPush
	}{
		{"bad key", "not-a-key", AddressedPush{At: time.Now(), OK: true}},
		{"zero time", BoardKey("/work/a/.mtix"), AddressedPush{OK: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Error(t, s.RecordAddressedPush(tt.key, tt.rec))
		})
	}
	_, _, err := s.ReadAddressedPush("not-a-key")
	require.Error(t, err)
}

func TestAddressedPush_OtherBoard_NotShared(t *testing.T) {
	s := fixture(t)
	require.NoError(t, s.RecordAddressedPush(BoardKey("/work/a/.mtix"), AddressedPush{At: time.Now(), OK: true}))
	_, present, err := s.ReadAddressedPush(BoardKey("/work/b/.mtix"))
	require.NoError(t, err)
	require.False(t, present)
}
