// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"time"
)

// PermissionChange changes one agent's host-local eligibility.
type PermissionChange struct {
	AgentID             string `json:"agent_id"`
	Action              string `json:"action"`
	PendingRegistration bool   `json:"pending_registration,omitempty"`
	Outcome             string `json:"outcome,omitempty"`
}

// PermissionBatch is one immutable operator confirmation and board publication.
type PermissionBatch struct {
	ID            string             `json:"id"`
	PublicationID string             `json:"publication_id,omitempty"`
	IntentHash    string             `json:"intent_hash,omitempty"`
	CallerID      string             `json:"caller_id,omitempty"`
	BoardID       string             `json:"board_id"`
	Hub           HubIdentity        `json:"hub_identity"`
	Mode          string             `json:"mode"`
	Source        string             `json:"source"`
	Markers       []string           `json:"markers"`
	Changes       []PermissionChange `json:"changes"`
	NodeID        string             `json:"node_id"`
	Time          time.Time          `json:"time"`
}

// AgentGrant records an explicit allow or deny on this host.
type AgentGrant struct {
	HostID              string      `json:"host_id"`
	BoardID             string      `json:"board_id"`
	Hub                 HubIdentity `json:"hub_identity"`
	AgentID             string      `json:"agent_id"`
	ConfirmedAt         time.Time   `json:"confirmed_at"`
	Mode                string      `json:"mode"`
	Source              string      `json:"source"`
	Denied              bool        `json:"denied"`
	Quarantined         bool        `json:"quarantined,omitempty"`
	QuarantineBatchID   string      `json:"quarantine_batch_id,omitempty"`
	PendingRegistration bool        `json:"pending_registration,omitempty"`
}

// Matches checks all identities; Denied remains available to distinguish an explicit refusal.
func (g AgentGrant) Matches(host, board string, hub HubIdentity, agent string, strict bool) bool {
	return !g.Quarantined && g.HostID == host && g.AgentID == agent && (Grant{BoardID: g.BoardID, Hub: g.Hub}).Matches(board, hub, strict)
}

// Confirmation is an immutable per-agent record in a bounded retained log.
type Confirmation struct {
	ID                  string    `json:"id"`
	BatchHash           string    `json:"batch_hash"`
	CallerID            string    `json:"caller_id,omitempty"`
	Outcome             string    `json:"outcome,omitempty"`
	HostID              string    `json:"host_id"`
	BoardID             string    `json:"board_id"`
	AgentID             string    `json:"agent_id"`
	Action              string    `json:"action"`
	Time                time.Time `json:"time"`
	Mode                string    `json:"mode"`
	Source              string    `json:"source"`
	Markers             []string  `json:"harness_markers"`
	BoardLine           string    `json:"board_line"`
	Pending             bool      `json:"-"`
	PendingRegistration bool      `json:"pending_registration,omitempty"`
}

// ApplyPermissions durably accepts the batch and recovers prior work before accepting another.
// The publisher must be idempotent by batch ID. AgentGrants and Confirmations
// are safe to call inside it; mutation methods must not be reentered.
func (s *State) applyPermissionsScoped(ctx context.Context, boardID string, batch PermissionBatch, publish func(context.Context, PermissionBatch) error) error {
	if err := validateOperatorPermissionBatch(boardID, batch); err != nil {
		return err
	}
	return s.withPermissions(ctx, func(dir *os.File, host string) error {
		tx := permissionTransaction{ctx: ctx, dir: dir, host: host, publish: publish, boardID: boardID}
		return s.applyOperatorPermissionsLocked(tx, batch)
	})
}

func validateOperatorPermissionBatch(boardID string, batch PermissionBatch) error {
	if boardID != "" && boardID != batch.BoardID {
		return invalid("permission board scope conflicts")
	}
	if batch.CallerID != "" {
		return invalid("caller input contains derived caller identity")
	}
	for _, change := range batch.Changes {
		if change.Outcome != "" {
			return invalid("caller input contains derived outcome")
		}
	}
	if batch.IntentHash != "" {
		return invalid("permission intent requires replay API")
	}
	return validatePermissionBatch(batch, false)
}

func (s *State) applyOperatorPermissionsLocked(tx permissionTransaction, batch PermissionBatch) error {
	if batch.ID != "" {
		if err := refuseCancelledPermission(tx.dir, tx.host, batch.ID); err != nil {
			return err
		}
	}
	if err := s.preemptPendingPermissions(tx, batch); err != nil {
		return err
	}
	if err := s.recoverPermissionTransaction(tx); err != nil {
		return err
	}
	if batch.ID == "" {
		id := make([]byte, 16)
		if _, err := rand.Read(id); err != nil {
			return failure("permission identifier", err)
		}
		batch.ID = hex.EncodeToString(id)
	}
	if err := refuseCancelledPermission(tx.dir, tx.host, batch.ID); err != nil {
		return err
	}
	canonical, err := canonicalizeDeniedFile(tx, batch)
	if err != nil {
		return err
	}
	return s.acceptPermissions(tx, canonical)
}
