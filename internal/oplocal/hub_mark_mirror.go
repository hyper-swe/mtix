// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package oplocal

import "strings"

// HubMarkMirror is the operator-local copy of a board's sticky hub mark.
// The board store holds the authority; this copy lets host-wide tools read
// the mark without opening the store (ADR-007 §3.5).
type HubMarkMirror struct {
	Checkout string `json:"checkout"`
	Source   string `json:"source"`
}

func hubMarkRecordName(checkout string) (string, error) {
	name, err := boardRecordName(checkout)
	if err != nil {
		return "", err
	}
	return "hubmark-" + strings.TrimPrefix(name, "identity-"), nil
}

// MirrorHubMark records that this checkout's board is hub-connected.
func (s *State) MirrorHubMark(checkout, source string) error {
	name, err := hubMarkRecordName(checkout)
	if err != nil {
		return err
	}
	if source == "" {
		return invalid("hub mark source is required")
	}
	return s.WriteJSON(name, HubMarkMirror{Checkout: checkout, Source: source})
}

// ClearHubMarkMirror records an empty mirror after a TTY reset.
func (s *State) ClearHubMarkMirror(checkout string) error {
	name, err := hubMarkRecordName(checkout)
	if err != nil {
		return err
	}
	return s.WriteJSON(name, HubMarkMirror{})
}

// HubMarkMirror reads the mirror; an absent record is empty.
func (s *State) HubMarkMirror(checkout string) (HubMarkMirror, []Report, error) {
	name, err := hubMarkRecordName(checkout)
	if err != nil {
		return HubMarkMirror{}, nil, err
	}
	var mirror HubMarkMirror
	reports, err := s.ReadJSON(name, &mirror)
	if err != nil {
		return HubMarkMirror{}, reports, err
	}
	if mirror.Checkout != checkout {
		return HubMarkMirror{}, reports, nil
	}
	return mirror, reports, nil
}
