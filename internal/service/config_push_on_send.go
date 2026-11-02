// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import "fmt"

// PushOnAddressedSendKey is the config key that switches the automatic push
// of an addressed send (ADR-007 D21, MTIX-112.20.2).
const PushOnAddressedSendKey = "sync.push_on_addressed_send"

// PushOnAddressedSend reports whether an addressed send pushes the board's
// pending events to the hub (ADR-007 D21, MTIX-112.20.2). The shipped
// default is true; the key accepts true or false in any form
// strconv.ParseBool accepts. An unparsable stored value is an error, so a
// typo never reads as "on".
func (cs *ConfigService) PushOnAddressedSend() (bool, error) {
	raw, err := cs.Get(PushOnAddressedSendKey)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", PushOnAddressedSendKey, err)
	}
	on, err := parseAutoSync(raw)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", PushOnAddressedSendKey, err)
	}
	return on, nil
}
