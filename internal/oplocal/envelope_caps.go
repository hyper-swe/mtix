// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package oplocal

import (
	"fmt"
	"github.com/hyper-swe/mtix/internal/model"
	"path/filepath"
	"strconv"

	inboxformat "github.com/hyper-swe/mtix/internal/format"
)

// EnvelopeCaps reads host-validated wake.json envelope bounds without writes
// (FR-20.3, D4). Missing fields default to unlimited; unrelated keys survive.
func (s *State) EnvelopeCaps() (inboxformat.Caps, []Report, error) {
	return s.EnvelopeCapsForAgent("")
}

// EnvelopeCapsForAgent reads effective host and agent envelope limits (FR-20.3).
func (s *State) EnvelopeCapsForAgent(agent string) (inboxformat.Caps, []Report, error) {
	values, reports, err := s.WakeSettings(agent)
	if err != nil {
		return inboxformat.Caps{}, reports, err
	}
	caps, err := wakeEnvelopeCaps(values)
	return caps, reports, err
}

// EnvelopeCapsRemedy describes an exact operator-local edit, without changing
// authority (FR-20.3). A foreign-host report requires a fresh local record,
// while invalid numeric caps preserve the valid host's other settings.
func (s *State) EnvelopeCapsRemedy(reports ...Report) string {
	repair := "in data.host and each data.agents.<agent> object set wake.envelope_max_events, wake.envelope_max_body_bytes and wake.envelope_max_total_bytes to integer 0 (unlimited); also set legacy data fields max_events, max_body_bytes and max_total_bytes to integer 0, preserving host_id and other settings"
	path, err := s.Path()
	if err != nil {
		return "Restore an operator-owned configuration directory; in wake.json " + repair + "."
	}
	file := filepath.Join(path, "wake.json")
	if len(reports) > 0 {
		return "Move " + file + " aside to a private backup outside the project; do not adopt foreign settings. Recreate " + file + " with host_id from " + filepath.Join(path, "host-id") + " and data containing only desired local caps (0 means unlimited). Use mode 0600 and operator directory 0700 on Unix, or a user-only ACL on Windows; then run mtix sync doctor --json."
	}
	return "Edit " + file + ": " + repair + "; then run mtix sync doctor --json."
}

func wakeEnvelopeCaps(values map[string]model.WakeSetting) (inboxformat.Caps, error) {
	var err error
	var caps inboxformat.Caps
	for key, target := range map[string]*int{"wake.envelope_max_events": &caps.MaxEvents, "wake.envelope_max_body_bytes": &caps.MaxBodyBytes, "wake.envelope_max_total_bytes": &caps.MaxTotalBytes} {
		if value := values[key].Value; value != "" {
			*target, err = strconv.Atoi(value)
			if err != nil {
				return inboxformat.Caps{}, fmt.Errorf("read %s: %w", key, err)
			}
		}
	}
	if capsErr := caps.Validate(); capsErr != nil {
		return inboxformat.Caps{}, fmt.Errorf("validate inbox envelope caps: %w", capsErr)
	}
	return caps, nil
}
