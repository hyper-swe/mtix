// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package oplocal

import "github.com/google/uuid"

// HubIdentity identifies the transport history observed with local credentials.
type HubIdentity struct {
	Kind           string `json:"kind,omitempty"`
	ProjectPrefix  string `json:"project_prefix,omitempty"`
	FirstEventHash string `json:"first_event_hash,omitempty"`
	RelayID        string `json:"relay_id,omitempty"`
}

// Grant records an operator confirmation, separately from current observations.
type Grant struct {
	BoardID string      `json:"board_id"`
	Hub     HubIdentity `json:"hub_identity"`
}

// Matches reports whether the supplied observation satisfies this confirmation.
func (g Grant) Matches(boardID string, hub HubIdentity, strict bool) bool {
	return validBoardID(boardID) && boardID == g.BoardID && (!strict || (g.Hub.Known() && hub.Known() && g.Hub == hub))
}

// Known reports whether this value identifies an observed transport history.
func (h HubIdentity) Known() bool {
	switch h.Kind {
	case "postgres":
		return h.ProjectPrefix != "" && h.FirstEventHash != "" && h.RelayID == ""
	case "relay":
		id, err := uuid.Parse(h.RelayID)
		return err == nil && id.Version() == 7 && id.String() == h.RelayID && h.ProjectPrefix == "" && h.FirstEventHash == ""
	default:
		return false
	}
}

func validBoardID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil && id.String() == value
}
