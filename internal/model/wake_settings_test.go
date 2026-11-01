// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model_test

import (
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestWakeSettingPolicy_DefaultsAndValueValidation(t *testing.T) {
	defaults := model.WakeSettingDefaults()
	require.Len(t, defaults, 12)
	require.Equal(t, "32", defaults["wake.max_hop_depth"])
	require.Equal(t, "5", defaults["wake.backoff_after_unacked"])
	defaults["wake.live"] = "false"
	require.Equal(t, "true", model.WakeSettingDefaults()["wake.live"])
	for _, tt := range []struct {
		key, input, want string
		bad              bool
	}{
		{"wake.live", "ON", "true", false}, {"wake.live", "0", "false", false}, {"wake.live", "maybe", "", true},
		{"inbox.strict_board_identity", "true", "true", false}, {"inbox.strict_board_identity", "off", "false", false},
		{"wake.wakes_per_hour", "12", "12", false}, {"wake.wakes_per_hour", "0", "", false}, {"wake.wakes_per_hour", "off", "", false},
		{"wake.wakes_per_hour", "unset", "", false}, {"wake.wakes_per_hour", "-1", "", true}, {"wake.wakes_per_hour", "1.2", "", true},
		{"wake.max_hop_depth", "64", "64", false}, {"wake.max_hop_depth", "0", "", true}, {"wake.max_hop_depth", "off", "", true},
		{"wake.backoff_after_unacked", "-1", "", true}, {"wake.unknown", "2", "", true},
	} {
		t.Run(tt.key+"/"+tt.input, func(t *testing.T) {
			got, err := model.NormalizeWakeSetting(tt.key, tt.input)
			if tt.bad {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.want, got)
			}
		})
	}
	require.False(t, model.IsWakeSetting("wake.unknown"))
	require.True(t, model.IsWakeSetting("wake.live"))
}

func TestWakeSettingPolicy_TTYRequiredOnlyForLoosening(t *testing.T) {
	for _, tt := range []struct {
		key, before, after string
		loosen             bool
	}{
		{"wake.live", "true", "true", false}, {"wake.live", "true", "false", false}, {"wake.live", "false", "true", true},
		{"inbox.strict_board_identity", "true", "false", true}, {"inbox.strict_board_identity", "false", "true", false},
		{"wake.wakes_per_hour", "", "12", false}, {"wake.wakes_per_hour", "12", "", true},
		{"wake.wakes_per_hour", "12", "11", false}, {"wake.wakes_per_hour", "12", "13", true},
		{"wake.max_hop_depth", "32", "33", true}, {"wake.max_hop_depth", "32", "31", false},
		{"wake.wakes_per_hour", "invalid", "3", true}, {"wake.wakes_per_hour", "3", "invalid", true},
	} {
		t.Run(tt.key+"/"+tt.before+"/"+tt.after, func(t *testing.T) { require.Equal(t, tt.loosen, model.WakeSettingLoosens(tt.key, tt.before, tt.after)) })
	}
}
