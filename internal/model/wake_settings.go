// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"
	"strconv"
	"strings"
)

// WakeSetting is an effective host policy value and its provenance (FR-20.3).
// An empty budget value means unlimited.
type WakeSetting struct {
	Value  string `json:"value"`
	Source string `json:"source"`
}

// WakeSettingDefaults returns independent liberal defaults per FR-20.3/20.5.
func WakeSettingDefaults() map[string]string {
	return map[string]string{
		"wake.live": "true", "wake.wakes_per_hour": "", "wake.per_sender_per_hour": "",
		"wake.remote_awake_daily_cap": "", "wake.reply_extensions_per_hour": "",
		"wake.envelope_max_events": "", "wake.envelope_max_body_bytes": "", "wake.envelope_max_total_bytes": "",
		"wake.max_hop_depth": "32", "wake.backoff_after_unacked": "5", WakeConfirmWindowKey: "30", "inbox.strict_board_identity": "false",
	}
}

// IsWakeSetting reports keys stored outside the workspace (FR-20.3/20.5).
func IsWakeSetting(key string) bool { _, ok := WakeSettingDefaults()[key]; return ok }

// NormalizeWakeSetting validates one operator-supplied value (FR-20.3).
func NormalizeWakeSetting(key, value string) (string, error) {
	if !IsWakeSetting(key) {
		return "", fmt.Errorf("unknown wake setting %s: %w", key, ErrInvalidConfigKey)
	}
	value = strings.ToLower(strings.TrimSpace(value))
	if key == "wake.live" || key == "inbox.strict_board_identity" {
		switch value {
		case "on", "true", "1":
			return "true", nil
		case "off", "false", "0":
			return "false", nil
		}
		return "", fmt.Errorf("%s requires on/off or true/false: %w", key, ErrInvalidInput)
	}
	if key == WakeConfirmWindowKey {
		return normalizeConfirmWindow(value)
	}
	guard := isWakeGuardSetting(key)
	if !guard && (value == "" || value == "off" || value == "unset" || value == "0") {
		return "", nil
	}
	number, err := strconv.ParseInt(value, 10, 64)
	if err != nil || number <= 0 {
		if guard {
			return "", fmt.Errorf("%s is always on and requires a positive integer: %w", key, ErrInvalidInput)
		}
		return "", fmt.Errorf("%s requires a nonnegative integer or unset: %w", key, ErrInvalidInput)
	}
	return strconv.FormatInt(number, 10), nil
}

// WakeSettingLoosens classifies already normalized values for TTY policy.
func WakeSettingLoosens(key, before, after string) bool {
	if before == after || key == WakeConfirmWindowKey {
		return false
	}
	if key == "wake.live" {
		return after == "true"
	}
	if key == "inbox.strict_board_identity" {
		return after == "false"
	}
	if before == "" {
		return false
	}
	if after == "" {
		return true
	}
	a, err := strconv.ParseInt(before, 10, 64)
	if err != nil {
		return true
	}
	b, err := strconv.ParseInt(after, 10, 64)
	if err != nil {
		return true
	}
	return b > a
}

// isWakeGuardSetting reports the always-on settings, which refuse zero, off and empty values.
func isWakeGuardSetting(key string) bool {
	return key == "wake.max_hop_depth" || key == "wake.backoff_after_unacked"
}

// WakeConfirmWindowKey is the setting that decides when a sent nudge reads as unconfirmed.
// It is an ordinary setting, not a guard: it changes only how an unanswered nudge is
// reported (MTIX-112.40.6), has no safety effect, and never needs an operator terminal.
const WakeConfirmWindowKey = "wake.confirm_window"

// WakeConfirmWindowMaxSeconds caps the window at one day, so the seconds value cannot
// overflow a time.Duration and flip every nudge to unconfirmed.
const WakeConfirmWindowMaxSeconds = 86400

func normalizeConfirmWindow(value string) (string, error) {
	number, err := strconv.ParseInt(value, 10, 64)
	if err != nil || number <= 0 || number > WakeConfirmWindowMaxSeconds {
		return "", fmt.Errorf("%s requires a whole number of seconds from 1 to %d: %w", WakeConfirmWindowKey, WakeConfirmWindowMaxSeconds, ErrInvalidInput)
	}
	return strconv.FormatInt(number, 10), nil
}
