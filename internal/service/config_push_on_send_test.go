// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
)

func TestPushOnAddressedSend_DefaultsAndOverrides(t *testing.T) {
	tests := []struct {
		name  string
		value string // "" leaves the key unset
		want  bool
	}{
		{"unset defaults on", "", true},
		{"explicit true", "true", true},
		{"explicit false", "false", false},
		{"any ParseBool form", "0", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cs, _ := newHubContactConfig(t)
			if tt.value != "" {
				_, err := cs.Set(service.PushOnAddressedSendKey, tt.value)
				require.NoError(t, err)
			}
			got, err := cs.PushOnAddressedSend()
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

// A hand-edited unparsable value is an error, never read as on.
func TestPushOnAddressedSend_UnparsableStoredValue_Errors(t *testing.T) {
	_, path := newHubContactConfig(t)
	require.NoError(t, os.WriteFile(path, []byte("prefix: TEST\nsync:\n  push_on_addressed_send: maybe\n"), 0o600))
	cs, err := service.NewConfigService(path)
	require.NoError(t, err)
	_, err = cs.PushOnAddressedSend()
	require.Error(t, err)
}
