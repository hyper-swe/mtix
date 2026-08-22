// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package keyring

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRing_EmptyRingIsAbsentNotAPanic covers the zero-value Ring.
// Load never produces one — it refuses an empty directory — but a Ring
// is an exported type, so the guard has to hold for a caller that
// builds one, and "absent" is the honest answer rather than a nil map
// panic at the far end of an ingest loop.
func TestRing_EmptyRingIsAbsentNotAPanic(t *testing.T) {
	var r Ring

	_, err := r.For(1)
	require.ErrorIs(t, err, ErrKeyAbsent)
	require.Contains(t, err.Error(), "none", "the verdict should say no epochs are installed")

	_, _, err = r.Current()
	require.ErrorIs(t, err, ErrKeyAbsent)

	require.Empty(t, r.Epochs())
	require.Empty(t, r.Foreign())
}

// TestFormatEpochs renders the installed set for an operator-facing
// verdict.
func TestFormatEpochs(t *testing.T) {
	tests := []struct {
		name   string
		epochs []uint16
		want   string
	}{
		{"none installed", nil, "none"},
		{"one", []uint16{1}, "1"},
		{"several", []uint16{1, 2, 7}, "1,2,7"},
		{"max", []uint16{65535}, "65535"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, formatEpochs(tt.epochs))
		})
	}
}

// TestCodeOf covers the verdict-to-code mapping, including the
// unclassified case that must stay empty rather than guess.
func TestCodeOf(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"absent", ErrKeyAbsent, CodeKeyAbsent},
		{"perms", ErrKeyPerms, CodeKeyPerms},
		{"invalid", ErrKeyInvalid, CodeKeyInvalid},
		{"symlink", ErrKeySymlink, CodeKeySymlink},
		{"wrapped", errors.New("outer: " + CodeKeyAbsent), ""},
		{"unclassified", errors.New("something else"), ""},
		{"exists is not a relay verdict", ErrKeyExists, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, CodeOf(tt.err))
		})
	}
}

// TestParseEpochName pins the name rule at the unit level.
func TestParseEpochName(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		want  uint16
		valid bool
	}{
		{"zero", "0", 0, true},
		{"one", "1", 1, true},
		{"max u16", "65535", 65535, true},
		{"past u16", "65536", 0, false},
		{"way past u16", "99999999999999999999", 0, false},
		{"leading zero", "01", 0, false},
		{"empty", "", 0, false},
		{"sign", "+1", 0, false},
		{"space", " 1", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseEpochName(tt.in)
			require.Equal(t, tt.valid, ok)
			require.Equal(t, tt.want, got)
		})
	}
}
