// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package oplocal

import (
	"crypto/sha256"
	"encoding/base32"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// BoardObservation is current routing evidence, not an enrollment grant.
type BoardObservation struct {
	Checkout  string      `json:"checkout"`
	BoardID   string      `json:"board_id,omitempty"`
	Hub       HubIdentity `json:"hub_identity"`
	RelayPath string      `json:"relay_path,omitempty"`
}

// ObserveBoard records a successful lookup, including an unknown identity.
func (s *State) ObserveBoard(checkout, boardID string, hub HubIdentity) error {
	name, err := boardRecordName(checkout)
	if err != nil {
		return err
	}
	if boardID != "" && !validBoardID(boardID) {
		return invalid("board identity is invalid")
	}
	if err := validateHub(hub); err != nil {
		return err
	}
	return s.WriteJSON(name, BoardObservation{Checkout: checkout, BoardID: boardID, Hub: hub})
}

// BoardObservation reads validated host-local evidence for this checkout.
func (s *State) BoardObservation(checkout string) (BoardObservation, []Report, error) {
	name, err := boardRecordName(checkout)
	if err != nil {
		return BoardObservation{}, nil, err
	}
	var observation BoardObservation
	reports, err := s.ReadJSON(name, &observation)
	if err != nil {
		return BoardObservation{}, reports, err
	}
	if observation == (BoardObservation{}) {
		return BoardObservation{}, reports, nil
	}
	if observation.Checkout != checkout || (observation.BoardID != "" && !validBoardID(observation.BoardID)) {
		return BoardObservation{}, reports, invalid("cached board metadata is invalid")
	}
	if err := validateHub(observation.Hub); err != nil {
		return BoardObservation{}, reports, err
	}
	return observation, reports, nil
}

// StrictBoardIdentity reads the host setting without creating or changing state.
func (s *State) StrictBoardIdentity() (bool, []Report, error) {
	return s.StrictBoardIdentityForAgent("")
}

// StrictBoardIdentityForAgent applies the selected agent override (FR-20.5).
func (s *State) StrictBoardIdentityForAgent(agent string) (bool, []Report, error) {
	values, reports, err := s.WakeSettings(agent)
	if err != nil {
		return false, reports, err
	}
	strict, err := strconv.ParseBool(values["inbox.strict_board_identity"].Value)
	return strict, reports, err
}

// BoardGrants reads operator confirmations without creating any.
func (s *State) BoardGrants() ([]Grant, []Report, error) {
	var record struct {
		Grants []Grant `json:"board_grants"`
	}
	reports, err := s.ReadJSON("grants", &record)
	if err != nil {
		return nil, reports, err
	}
	for _, grant := range record.Grants {
		if !validBoardID(grant.BoardID) {
			return nil, reports, invalid("recorded board identity is invalid")
		}
		if err := validateHub(grant.Hub); err != nil {
			return nil, reports, err
		}
	}
	return record.Grants, reports, nil
}

func boardRecordName(checkout string) (string, error) {
	if !filepath.IsAbs(checkout) || filepath.Clean(checkout) != checkout {
		return "", invalid("checkout path must be absolute and clean")
	}
	sum := sha256.Sum256([]byte(checkout))
	return "identity-" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:])), nil
}

func validateHub(hub HubIdentity) error {
	if hub == (HubIdentity{}) || hub.Known() {
		return nil
	}
	return fmt.Errorf("validate cached identity: %w", invalid("transport identity is invalid"))
}

// ObserveRelay records the configuration identity after an attach has verified it.
func (s *State) ObserveRelay(checkout, boardID, relayPath, relayID string) error {
	name, err := boardRecordName(checkout)
	if err != nil {
		return err
	}
	if _, err = boardRecordName(relayPath); err != nil {
		return err
	}
	hub := HubIdentity{Kind: "relay", RelayID: relayID}
	if err := validateHub(hub); err != nil {
		return err
	}
	if boardID != "" && !validBoardID(boardID) {
		return invalid("board identity is invalid")
	}
	return s.WriteJSON(name, BoardObservation{Checkout: checkout, BoardID: boardID, Hub: hub, RelayPath: relayPath})
}
