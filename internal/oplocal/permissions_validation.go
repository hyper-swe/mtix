// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"encoding/hex"
	"github.com/google/uuid"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

func permissionText(value string) bool {
	return value != "" && len(value) <= 256 && utf8.ValidString(value) && !strings.ContainsAny(value, "\x00\r\n")
}
func permissionID(id string) bool {
	data, err := hex.DecodeString(id)
	return err == nil && len(data) == 16 && strings.ToLower(id) == id
}
func validatePermissionBatch(b PermissionBatch, stored bool) error {
	if err := validatePermissionBatchMetadata(b, stored); err != nil {
		return err
	}
	if b.Mode != "interactive" && b.Mode != "yes" && b.Mode != "file" {
		return invalid("permission mode is invalid")
	}
	if err := validateHub(b.Hub); err != nil {
		return err
	}
	if b.NodeID != "" && !permissionText(b.NodeID) {
		return invalid("permission node identity is invalid")
	}
	if len(b.Changes) == 0 || len(b.Changes) > 1024 {
		return invalid("permission batch size is invalid")
	}
	seen := map[string]bool{}
	for _, change := range b.Changes {
		if err := validatePermissionChange(change, b.Mode, seen[change.AgentID]); err != nil {
			return err
		}
		seen[change.AgentID] = true
	}
	return validatePermissionMarkers(b.Markers)
}
func validatePermissionBatchMetadata(b PermissionBatch, stored bool) error {
	if err := validatePermissionPublicationID(b.PublicationID); err != nil {
		return err
	}
	if b.IntentHash != "" && (!validAlertHash(b.IntentHash) || b.IntentHash == "absent") {
		return invalid("permission intent hash is invalid")
	}
	if b.CallerID != "" && !permissionID(b.CallerID) {
		return invalid("permission caller identity is invalid")
	}
	if (b.ID != "" || stored) && !permissionID(b.ID) {
		return invalid("permission batch identity is invalid")
	}
	if !validBoardID(b.BoardID) || b.Time.IsZero() || !permissionText(b.Source) {
		return invalid("permission batch metadata is invalid")
	}
	return nil
}

func validatePermissionChange(change PermissionChange, mode string, duplicate bool) error {
	if !permissionText(change.AgentID) || len(change.AgentID) > 64 || duplicate {
		return invalid("permission agent identity is invalid")
	}
	if change.Action != "allow" && change.Action != "deny" {
		return invalid("permission action is invalid")
	}
	if change.Outcome != "" && ((change.Outcome != "denied_requires_operator" && change.Outcome != "quarantined_requires_operator") || change.Action != "allow" || mode != "file" || change.PendingRegistration) {
		return invalid("permission outcome is invalid")
	}
	if change.Action == "deny" && change.PendingRegistration {
		return invalid("deny cannot be pending registration")
	}
	return nil
}

func validatePermissionMarkers(markers []string) error {
	if len(markers) > 128 || !slices.IsSorted(markers) {
		return invalid("permission markers must be sorted names")
	}
	names := regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)
	for i, name := range markers {
		if !names.MatchString(name) || (i > 0 && name == markers[i-1]) {
			return invalid("permission marker name is invalid")
		}
	}
	return nil
}
func validateAgentGrant(g AgentGrant, host string) error {
	if g.Quarantined {
		if g.Denied || !permissionID(g.QuarantineBatchID) {
			return invalid("permission quarantine hold is invalid")
		}
	} else if g.QuarantineBatchID != "" {
		return invalid("permission quarantine identity has no hold")
	}
	if g.Denied && g.PendingRegistration {
		return invalid("denied grant cannot be pending registration")
	}
	if g.HostID != host || !validHostID(g.HostID) {
		return invalid("permission grant belongs to another host")
	}
	return validatePermissionBatch(PermissionBatch{BoardID: g.BoardID, Hub: g.Hub, Time: g.ConfirmedAt, Mode: g.Mode, Source: g.Source, Changes: []PermissionChange{{AgentID: g.AgentID, Action: "allow", PendingRegistration: g.PendingRegistration}}}, false)
}
func validateConfirmation(row Confirmation, host string) error {
	if !validAlertHash(row.BatchHash) || row.BatchHash == "absent" {
		return invalid("confirmation batch hash is invalid")
	}
	if row.HostID != host {
		return invalid("confirmation belongs to another host")
	}
	if row.BoardLine != "published" && row.BoardLine != "pending" && row.BoardLine != "skipped_no_node" && row.BoardLine != "undeliverable" && row.BoardLine != "cancelled" && row.BoardLine != "quarantined" {
		return invalid("confirmation publication state is invalid")
	}
	return validatePermissionBatch(PermissionBatch{ID: row.ID, CallerID: row.CallerID, BoardID: row.BoardID, Time: row.Time, Mode: row.Mode, Source: row.Source, Markers: row.Markers, Changes: []PermissionChange{{AgentID: row.AgentID, Action: row.Action, Outcome: row.Outcome, PendingRegistration: row.PendingRegistration}}}, true)
}

func validatePermissionPublicationID(publicationID string) error {
	if publicationID != "" {
		id, err := uuid.Parse(publicationID)
		if err != nil || id.Version() != 7 || id.String() != publicationID {
			return invalid("permission publication identity is invalid")
		}
	}

	return nil
}
