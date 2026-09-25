// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/sync/validator"
)

// Tests of the sync.max_lamport_jump config key (MTIX-95.11): how far above
// the local Lamport clock a pulled event may be stamped before pull
// quarantines it.

// TestConfig_MaxLamportJump_DefaultIs2To32: the key is a valid config key
// whose default is 2^32.
func TestConfig_MaxLamportJump_DefaultIs2To32(t *testing.T) {
	cs, err := service.NewConfigService("")
	require.NoError(t, err)

	got, err := cs.Get("sync.max_lamport_jump")

	require.NoError(t, err)
	require.Equal(t, "4294967296", got)
	require.Equal(t, validator.DefaultMaxLamportJump, cs.MaxLamportJump())
	require.Contains(t, service.ValidConfigKeys(), "sync.max_lamport_jump")
}

// TestConfig_SetMaxLamportJump_AcceptsOnlyPositiveIntegers: `mtix config
// set` stores a positive integer, which survives a reload and is what
// MaxLamportJump returns; anything else is refused with ErrInvalidInput and
// leaves the stored value unchanged.
func TestConfig_SetMaxLamportJump_AcceptsOnlyPositiveIntegers(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    int64
		wantErr bool
	}{
		{"one", "1", 1, false},
		{"a million", "1000000", 1000000, false},
		{"2^53", "9007199254740992", 9007199254740992, false},
		{"zero", "0", 0, true},
		{"negative", "-5", 0, true},
		{"not a number", "lots", 0, true},
		{"empty", "", 0, true},
		{"fraction", "1.5", 0, true},
		{"beyond int64", "9223372036854775808", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			cs, err := service.NewConfigService(path)
			require.NoError(t, err)

			_, err = cs.Set("sync.max_lamport_jump", tt.value)

			if tt.wantErr {
				require.ErrorIs(t, err, model.ErrInvalidInput)
				require.ErrorContains(t, err, "sync.max_lamport_jump")
				require.Equal(t, validator.DefaultMaxLamportJump, cs.MaxLamportJump(),
					"a refused value is not stored")
				return
			}
			require.NoError(t, err)
			reloaded, err := service.NewConfigService(path)
			require.NoError(t, err)
			require.Equal(t, tt.want, reloaded.MaxLamportJump())
		})
	}
}

// TestConfig_MaxLamportJump_HandEditedInvalid_FallsBackToDefault: a value
// that is not a positive integer, written into config.yaml by hand, is
// never used as the bound; the default applies.
func TestConfig_MaxLamportJump_HandEditedInvalid_FallsBackToDefault(t *testing.T) {
	for _, raw := range []string{"0", "-1", "big", ""} {
		t.Run(raw, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			require.NoError(t, os.WriteFile(path,
				[]byte("sync:\n  max_lamport_jump: \""+raw+"\"\n"), 0o600))
			cs, err := service.NewConfigService(path)
			require.NoError(t, err)

			require.Equal(t, validator.DefaultMaxLamportJump, cs.MaxLamportJump())
		})
	}
}
