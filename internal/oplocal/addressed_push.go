// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package oplocal

import "time"

// AddressedPush is the outcome of the latest push that an addressed send
// requested (ADR-007 D21, MTIX-112.20.2). It is operator-local, never synced,
// and carries a short error class only: never the error text, so no
// connection detail can reach it.
type AddressedPush struct {
	At         time.Time `json:"at"`
	OK         bool      `json:"ok"`
	ErrorClass string    `json:"error_class,omitempty"`
	// Shown is true once a command has printed this failure.
	Shown bool `json:"shown,omitempty"`
}

// RecordAddressedPush stores the outcome of the latest requested push.
func (s *State) RecordAddressedPush(key string, p AddressedPush) error {
	name, err := hubRecordName("addrpush", key)
	if err != nil {
		return err
	}
	if p.At.IsZero() {
		return invalid("addressed push needs a time")
	}
	return s.WriteJSON(name, p)
}

// ReadAddressedPush returns the latest requested push outcome; absent means none.
func (s *State) ReadAddressedPush(key string) (AddressedPush, bool, error) {
	var p AddressedPush
	name, err := hubRecordName("addrpush", key)
	if err != nil {
		return p, false, err
	}
	reports, err := s.ReadJSON(name, &p)
	if err != nil || len(reports) != 0 || p.At.IsZero() {
		return AddressedPush{}, false, err
	}
	return p, true, nil
}
