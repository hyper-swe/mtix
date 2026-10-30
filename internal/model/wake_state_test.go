// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests the sender-visible delivery state derived from a wake ledger outcome (MTIX-112.40.6).
package model_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestWakeOutcomeState_EveryOutcome_MapsToOneDocumentedState(t *testing.T) {
	window := 30 * time.Second
	tests := []struct {
		name    string
		outcome model.WakeOutcome
		reason  string
		age     time.Duration
		want    model.WakeState
	}{
		{"sent within window is nudged", model.WakeSent, "", 5 * time.Second, model.WakeStateNudged},
		{"sent at window edge is nudged", model.WakeSent, "", window, model.WakeStateNudged},
		{"sent past window is unconfirmed", model.WakeSent, "", window + time.Second, model.WakeStateUnconfirmed},
		{"legacy delivered reads as sent", model.WakeDelivered, "", 5 * time.Second, model.WakeStateNudged},
		{"legacy delivered past window is unconfirmed", model.WakeDelivered, "", time.Minute, model.WakeStateUnconfirmed},
		{"confirmed stays confirmed however old", model.WakeConfirmed, "", 24 * time.Hour, model.WakeStateConfirmed},
		{"held", model.WakeHeld, "", time.Hour, model.WakeStateHeld},
		{"off outcome", model.WakeOff, "", time.Hour, model.WakeStateOff},
		{"undeliverable keeps its reason state", model.WakeUndeliverable, "no-live-route", 0, model.WakeStateUndeliverable},
		{"legacy error reads as undeliverable", model.WakeError, "", 0, model.WakeStateUndeliverable},
		{"claimed row is in flight, shown as queued", model.WakeClaimed, "", 0, model.WakeStateQueued},
		{"unknown outcome is never silent", model.WakeOutcome("future"), "", 0, model.WakeStateUnknown},
		{"empty outcome is never silent", model.WakeOutcome(""), "", 0, model.WakeStateUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, tt.outcome.State(tt.age, window))
		})
	}
}

func TestWakeStates_Documented_ListIsClosedAndSorted(t *testing.T) {
	require.Equal(t, []model.WakeState{
		"confirmed", "held", "no-dispatcher", "no-live-session", "nudged", "off", "queued", "undeliverable", "unconfirmed", "unknown",
	}, model.WakeStates())
}

func TestWakeSettings_ConfirmWindow_IsNotAGuardAndNeverLoosens(t *testing.T) {
	// The window only decides when an unanswered nudge reads as unconfirmed: no safety effect,
	// so raising, lowering or deleting it never needs an operator terminal.
	require.False(t, model.WakeSettingLoosens("wake.confirm_window", "30", "60"))
	require.False(t, model.WakeSettingLoosens("wake.confirm_window", "60", "30"))
	require.False(t, model.WakeSettingLoosens("wake.confirm_window", "30", ""))
	require.True(t, model.WakeSettingLoosens("wake.max_hop_depth", "32", "64"), "real guards still loosen")
}

func TestWakeSettings_ConfirmWindow_CapsAtOneDay(t *testing.T) {
	got, err := model.NormalizeWakeSetting("wake.confirm_window", "86400")
	require.NoError(t, err)
	require.Equal(t, "86400", got)
	for _, bad := range []string{"86401", "9223372036854775807", "99999999999999999999"} {
		_, err := model.NormalizeWakeSetting("wake.confirm_window", bad)
		require.ErrorIs(t, err, model.ErrInvalidInput, bad)
	}
}

func TestWakeSettings_ConfirmWindow_DefaultsTo30AndRejectsNonPositive(t *testing.T) {
	require.Equal(t, "30", model.WakeSettingDefaults()["wake.confirm_window"])
	for _, tt := range []struct {
		input, want string
		bad         bool
	}{{"45", "45", false}, {"0", "", true}, {"off", "", true}, {"-3", "", true}, {"1.5", "", true}, {"", "", true}} {
		got, err := model.NormalizeWakeSetting("wake.confirm_window", tt.input)
		if tt.bad {
			require.ErrorIs(t, err, model.ErrInvalidInput, tt.input)
			continue
		}
		require.NoError(t, err)
		require.Equal(t, tt.want, got)
	}
}
