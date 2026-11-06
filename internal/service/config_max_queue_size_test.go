// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

// Tests of the sync.max_queue_size config key (MTIX-112.17.16.16): the cap
// on pending sync events, settable so the cap refusal's fix exists.

// TestConfig_MaxQueueSize_DefaultIsUnlimited: the key is valid and 0
// (unlimited) by default.
func TestConfig_MaxQueueSize_DefaultIsUnlimited(t *testing.T) {
	cs, err := service.NewConfigService("")
	require.NoError(t, err)

	got, err := cs.Get("sync.max_queue_size")

	require.NoError(t, err)
	require.Equal(t, "0", got)
	require.Zero(t, cs.MaxQueueSize())
	require.Contains(t, service.ValidConfigKeys(), "sync.max_queue_size")
}

// TestConfig_SetMaxQueueSize_AcceptsOnlyNonNegativeIntegers: a non-negative
// integer is stored and survives a reload; anything else is refused with
// ErrInvalidInput and leaves the stored value unchanged.
func TestConfig_SetMaxQueueSize_AcceptsOnlyNonNegativeIntegers(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    int64
		wantErr bool
	}{
		{"zero is unlimited", "0", 0, false},
		{"one", "1", 1, false},
		{"ten thousand", "10000", 10000, false},
		{"negative", "-1", 0, true},
		{"fraction", "1.5", 0, true},
		{"text", "many", 0, true},
		{"empty", "", 0, true},
		{"beyond 64 bits", "99999999999999999999", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			cs, err := service.NewConfigService(path)
			require.NoError(t, err)

			_, err = cs.Set("sync.max_queue_size", tt.value)

			if tt.wantErr {
				require.ErrorIs(t, err, model.ErrInvalidInput)
				require.Zero(t, cs.MaxQueueSize(), "a refused value leaves the default")
				return
			}
			require.NoError(t, err)
			reloaded, err := service.NewConfigService(path)
			require.NoError(t, err)
			require.Equal(t, tt.want, reloaded.MaxQueueSize())
		})
	}
}
