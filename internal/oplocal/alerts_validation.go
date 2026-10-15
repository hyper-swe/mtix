// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"encoding/hex"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/hyper-swe/mtix/internal/model"
)

// AlertClassWakeHopDepth names a configured hop-depth guard diagnostic.
const AlertClassWakeHopDepth = "wake.hop-depth"

// AlertClassWakeBackoff names a configured backoff guard diagnostic.
const AlertClassWakeBackoff = "wake.backoff"

func knownAlertClass(class string) bool {
	switch class {
	case AlertClassWakeHopDepth, AlertClassWakeBackoff, "wake.binary", "wake.contract-probe", "wake.probe-no-cold", "wake.cold-exit", "wake.nothing-acked", "wake.budget", "hooks.exec-untrusted", "hooks.webhook-untrusted", "wake.takeover", "wake.counter-rewind", "hooks.pin-placement", "wake.unconfirmed", "wake.version-skew", "attach.protocol-skew", "attach.verb-failure":
		return true
	default:
		return false
	}
}

func alertTextValid(text string) bool {
	if !utf8.ValidString(text) {
		return false
	}
	for _, r := range text {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return false
		}
	}
	return true
}

// ValidateAlertInput checks bounded plain-text diagnostics before persistence.
func ValidateAlertInput(a Alert) error {
	if a.Agent == "" || strings.TrimSpace(a.Agent) == "" || len(a.Agent) > 64 || !alertTextValid(a.Agent) || !knownAlertClass(a.Class) {
		return fmt.Errorf("alert identity or class is invalid: %w", model.ErrInvalidInput)
	}
	if len(a.Detail)+len(a.Fix) > 4096 || !alertTextValid(a.Detail) || !alertTextValid(a.Fix) {
		return fmt.Errorf("alert diagnostic is invalid: %w", model.ErrInvalidInput)
	}
	return nil
}

func validateStoredAlert(a Alert, host string) error {
	id, err := hex.DecodeString(a.ID)
	if err != nil || len(id) != 16 || strings.ToLower(a.ID) != a.ID || a.HostID != host || !validHostID(a.HostID) {
		return invalid("alert metadata is invalid")
	}
	if err = ValidateAlertInput(a); err != nil {
		return invalid("alert data is invalid")
	}
	if a.Count == 0 || a.FirstAt.IsZero() || a.LastAt.IsZero() || a.LastAt.Before(a.FirstAt) || !a.LastAt.Before(a.FirstAt.Add(time.Hour)) {
		return invalid("alert window is invalid")
	}
	return nil
}

func reconcileAlert(a, b Alert) (Alert, error) {
	if a.ID != b.ID || a.HostID != b.HostID || a.Agent != b.Agent || a.Class != b.Class || !a.FirstAt.Equal(b.FirstAt) {
		return Alert{}, invalid("alert copies conflict")
	}
	if a.Count == b.Count {
		if !a.LastAt.Equal(b.LastAt) || a.Detail != b.Detail || a.Fix != b.Fix {
			return Alert{}, invalid("alert copies conflict")
		}
		return a, nil
	}
	if a.Count > b.Count {
		a, b = b, a
	}
	if b.LastAt.Before(a.LastAt) {
		return Alert{}, invalid("alert count chronology conflicts")
	}
	return b, nil
}
