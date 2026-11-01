// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Protected sender attribution validates before persistence and survives reloads.
package oplocal_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/oplocal"
)

func TestWakeBudgetCredit_SenderValidationAndRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name, sender string
		valid        bool
	}{
		{"legacy empty", "", true}, {"plain", "sender-a", true},
		{"256 bytes", strings.Repeat("a", 256), true},
		{"unicode 256 bytes", strings.Repeat("é", 128), true},
		{"257 bytes", strings.Repeat("a", 257), false},
		{"unicode 258 bytes", strings.Repeat("é", 129), false},
		{"invalid UTF8", string([]byte{0xff}), false},
		{"NUL", "sender\x00a", false}, {"CR", "sender\ra", false},
		{"LF", "sender\na", false}, {"tab", "sender\ta", false},
		{"DEL", "sender\x7fa", false}, {"C1 control", "sender\u0085a", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, key, _, file := seededWakeBudget(t)
			before, err := os.ReadFile(file)
			require.NoError(t, err)
			err = state.UpdateWakeBudget(t.Context(), key, func(m *oplocal.WakeBudgetMirror, save func() error) error {
				m.Credits[0].Sender = tc.sender
				return save()
			})
			if tc.valid {
				require.NoError(t, err)
				mirror, present, err := state.ReadWakeBudget(key)
				require.NoError(t, err)
				require.True(t, present)
				require.Equal(t, tc.sender, mirror.Credits[0].Sender)
				return
			}
			require.Error(t, err, "invalid sender must be refused before JSON encoding")
			after, err := os.ReadFile(file)
			require.NoError(t, err)
			require.Equal(t, before, after, "invalid checkpoint leaves protected record intact")
		})
	}
}

func TestWakeBudgetCredit_InvalidStoredSenderRefused(t *testing.T) {
	for _, sender := range []string{strings.Repeat("a", 257), "sender\tbad", "sender\ninvalid"} {
		t.Run(sender, func(t *testing.T) {
			state, key, _, file := seededWakeBudget(t)
			mirror, _, err := state.ReadWakeBudget(key)
			require.NoError(t, err)
			mirror.Credits[0].Sender = sender
			require.NoError(t, state.WriteJSON(strings.TrimSuffix(filepath.Base(file), ".json"), mirror))
			before, err := os.ReadFile(file)
			require.NoError(t, err)
			_, _, err = state.ReadWakeBudget(key)
			require.Error(t, err)
			_, err = state.WakeBudgetMirrors(t.Context())
			require.Error(t, err)
			require.Error(t, state.UpdateWakeBudget(t.Context(), key, func(*oplocal.WakeBudgetMirror, func() error) error {
				t.Error("invalid sender must not reach callback")
				return nil
			}))
			after, err := os.ReadFile(file)
			require.NoError(t, err)
			require.Equal(t, before, after)
		})
	}
}
