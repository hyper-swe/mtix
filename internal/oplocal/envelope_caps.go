// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package oplocal

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	inboxformat "github.com/hyper-swe/mtix/internal/format"
)

// EnvelopeCaps reads host-validated wake.json envelope bounds without writes
// (FR-20.3, D4). Missing fields default to unlimited; unrelated keys survive.
func (s *State) EnvelopeCaps() (inboxformat.Caps, []Report, error) {
	var values map[string]json.RawMessage
	reports, err := s.ReadJSON("wake", &values)
	if err != nil {
		return inboxformat.Caps{}, reports, fmt.Errorf("read inbox envelope caps: %w", err)
	}
	if len(reports) > 0 {
		return inboxformat.Caps{}, reports, fmt.Errorf("wake.json belongs to another host: %w", invalid("restore host-specific configuration"))
	}
	var caps inboxformat.Caps
	for key, target := range map[string]*int{"max_events": &caps.MaxEvents, "max_body_bytes": &caps.MaxBodyBytes, "max_total_bytes": &caps.MaxTotalBytes} {
		if raw, ok := values[key]; ok {
			if string(raw) == "null" {
				return inboxformat.Caps{}, reports, invalid(key + " must be a nonnegative integer")
			}
			if err := json.Unmarshal(raw, target); err != nil {
				return inboxformat.Caps{}, reports, fmt.Errorf("read %s: %w", key, err)
			}
		}
	}
	if err := caps.Validate(); err != nil {
		return inboxformat.Caps{}, reports, fmt.Errorf("validate inbox envelope caps: %w", err)
	}
	return caps, reports, nil
}

// EnvelopeCapsRemedy describes an exact operator-local edit, without changing
// authority (FR-20.3). A foreign-host report requires a fresh local record,
// while invalid numeric caps preserve the valid host's other settings.
func (s *State) EnvelopeCapsRemedy(reports ...Report) string {
	path, err := s.Path()
	if err != nil {
		return "Restore an operator-owned configuration directory; in wake.json data set max_events, max_body_bytes and max_total_bytes to integer 0 (unlimited), preserving the valid host_id and other settings."
	}
	file := filepath.Join(path, "wake.json")
	if len(reports) > 0 {
		return "Move " + file + " aside to a private backup outside the project; do not adopt foreign settings. Recreate " + file + " with host_id from " + filepath.Join(path, "host-id") + " and data containing only desired local caps (0 means unlimited). Use mode 0600 and operator directory 0700 on Unix, or a user-only ACL on Windows; then run mtix sync doctor --json."
	}
	return "Edit " + file + ": in data set max_events, max_body_bytes and max_total_bytes to integer 0 (unlimited), preserving host_id and other settings; then run mtix sync doctor --json."
}
