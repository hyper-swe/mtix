// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// PermissionStore is the existing board boundary used by wake permission policy.
type PermissionStore interface {
	store.Store
	HubMarkStore
	ListRegistrations(context.Context, string, string) ([]sqlite.Registration, error)
	AppendAnnotationOnce(context.Context, string, model.Annotation) error
	PermissionAddressedAgents(context.Context) ([]string, error)
	PermissionPublicationExists(context.Context, string) (bool, error)
}

// PermissionBoard supplies current board and independently observed hub evidence.
type PermissionBoard struct {
	ID       string
	Checkout string
	Hub      oplocal.HubIdentity
	Evidence HubEvidence
}

// PermissionDeps injects host scope, protected state and a clock.
type PermissionDeps struct {
	Store  PermissionStore
	State  *oplocal.State
	HostID string
	UID    string
	Clock  func() time.Time
}

// WakePermissions manages operator-local grants; it never installs a wake route.
type WakePermissions struct{ PermissionDeps }

func NewWakePermissions(d PermissionDeps) *WakePermissions {
	return &WakePermissions{PermissionDeps: d}
}

// Wakeable evaluates registered host ids with deny taking precedence over implicit eligibility.
func (s *WakePermissions) Wakeable(ctx context.Context, board PermissionBoard, agent string) (bool, string, error) {
	registered, err := s.registered(ctx, agent)
	if err != nil {
		return false, "not_registered", err
	}
	grants, err := s.State.AgentGrants()
	if err != nil {
		return false, "operator state unreadable", fmt.Errorf("read wake grants: %w", err)
	}
	for _, grant := range grants {
		if grant.HostID == s.HostID && grant.BoardID == board.ID && grant.AgentID == agent {
			if grant.Denied {
				return false, "denied", nil
			}
			if grant.Quarantined {
				return false, "quarantined", nil
			}
		}
	}
	if !registered {
		return false, "not_registered", nil
	}
	status, err := NewHubMarkService(s.Store).Inspect(ctx, board.Evidence)
	if err != nil {
		return false, "hub mark unreadable", err
	}
	if !status.Connected {
		return true, "implicit local registration", nil
	}
	strict, reports, err := s.State.StrictBoardIdentityForAgent(agent)
	if err != nil || len(reports) != 0 {
		return false, "operator state unreadable", err
	}
	if s.hasConfirmedPermissionGrant(grants, board, agent, strict) {
		return true, "confirmed", nil
	}
	return false, "unconfirmed", nil
}

func (s *WakePermissions) hasConfirmedPermissionGrant(grants []oplocal.AgentGrant, board PermissionBoard, agent string, strict bool) bool {
	for _, grant := range grants {
		if grant.PendingRegistration && (!grant.Hub.Known() || !board.Hub.Known() || grant.Hub != board.Hub) {
			continue
		}
		if !grant.Denied && grant.Matches(s.HostID, board.ID, board.Hub, agent, strict) {
			return true
		}
	}
	return false
}

func (s *WakePermissions) registered(ctx context.Context, agent string) (bool, error) {
	if err := validateAgentID(agent); err != nil {
		return false, err
	}
	rows, err := s.Store.ListRegistrations(ctx, s.HostID, s.UID)
	if err != nil {
		return false, fmt.Errorf("read local registrations: %w", err)
	}
	for _, row := range rows {
		if row.AgentID == agent {
			return true, nil
		}
	}
	return false, nil
}

// ApplyBatch validates every id before a protected, recoverable batch is accepted.
// File callers set Mode=file; CLI callers use interactive or yes.
func (s *WakePermissions) ApplyBatch(ctx context.Context, board PermissionBoard, batch oplocal.PermissionBatch) error {
	if batch.Mode != "file" && batch.Mode != "interactive" && batch.Mode != "yes" {
		return fmt.Errorf("permission mode is invalid: %w", model.ErrInvalidInput)
	}
	if batch.CallerID != "" || batch.PublicationID != "" {
		return fmt.Errorf("caller cannot supply permission occurrence association: %w", model.ErrInvalidInput)
	}
	if err := s.ValidateChanges(ctx, batch.Changes); err != nil {
		return err
	}
	batch.Changes = append([]oplocal.PermissionChange{}, batch.Changes...)
	batch.Markers = append([]string{}, batch.Markers...)
	batch.BoardID, batch.Hub = board.ID, board.Hub
	var err error
	if batch.ID == "" && batch.Mode == "file" {
		batch.ID, err = permissionRandomID()
		if err != nil {
			return err
		}
	}
	state := s.State.WithPermissionNoticeProbe(s.permissionNoticeUnavailable)
	if batch.ID != "" {
		err = state.ApplyPermissionIntentScoped(ctx, board.ID, batch, s.preparePermissionBatch, s.permissionPublisher(board.ID))
	} else {
		batch, err = s.preparePermissionBatch(ctx, batch)
		if err == nil {
			err = state.ApplyPermissionsScoped(ctx, board.ID, batch, s.permissionPublisher(board.ID))
		}
	}
	if err != nil {
		return fmt.Errorf("apply wake permissions: %w", err)
	}
	return nil
}

func (s *WakePermissions) preparePermissionBatch(ctx context.Context, batch oplocal.PermissionBatch) (oplocal.PermissionBatch, error) {
	var err error
	batch, err = s.permissionOccurrence(ctx, batch)
	if err != nil {
		return batch, err
	}
	batch.Changes = append([]oplocal.PermissionChange{}, batch.Changes...)
	for i, change := range batch.Changes {
		registered, registrationErr := s.registered(ctx, change.AgentID)
		if registrationErr != nil {
			return batch, registrationErr
		}
		batch.Changes[i].PendingRegistration = change.Action == "allow" && !registered
	}
	node, err := s.permissionNode(ctx)
	if err != nil {
		return batch, err
	}
	batch.NodeID, batch.Time = node, s.Clock().UTC()
	batch.PublicationID, err = s.newPermissionPublicationID(ctx)
	if err != nil {
		return batch, err
	}
	return batch, nil
}

// ValidateChanges checks the entire batch before even a legacy identity is minted.
func (s *WakePermissions) ValidateChanges(_ context.Context, changes []oplocal.PermissionChange) error {
	if len(changes) == 0 || len(changes) > 1024 {
		return fmt.Errorf("permission batch size is invalid: %w", model.ErrInvalidInput)
	}
	seen := map[string]bool{}
	for _, change := range changes {
		if change.Outcome != "" {
			return fmt.Errorf("caller cannot supply permission outcome: %w", model.ErrInvalidInput)
		}
		if seen[change.AgentID] || (change.Action != "allow" && change.Action != "deny") {
			return fmt.Errorf("permission change is invalid: %w", model.ErrInvalidInput)
		}
		if err := validateAgentID(change.AgentID); err != nil {
			return err
		}
		seen[change.AgentID] = true
	}
	return nil
}

func (s *WakePermissions) permissionNode(ctx context.Context) (string, error) {
	nodes, _, err := s.Store.ListNodes(ctx, store.NodeFilter{}, store.ListOptions{Limit: -1})
	if err != nil {
		return "", fmt.Errorf("select permission notice target: %w", err)
	}
	var roots []string
	for _, node := range nodes {
		if node.ParentID == "" && node.DeletedAt == nil {
			roots = append(roots, node.ID)
		}
	}
	sort.Strings(roots)
	if len(roots) == 0 {
		return "", nil
	}
	return roots[0], nil
}

// PermissionNotice is the only identifying-free board annotation for each batch.
const PermissionNotice = "wake permissions changed on a host (see that host's mtix doctor)"

func (s *WakePermissions) publishPermissionBatch(ctx context.Context, batch oplocal.PermissionBatch) error {
	if batch.NodeID == "" {
		return nil
	}
	unavailable, err := s.permissionNoticeUnavailable(ctx, batch)
	if err != nil {
		return err
	}
	if unavailable {
		return oplocal.ErrPermissionNoticeUndeliverable
	}
	err = s.Store.AppendAnnotationOnce(ctx, batch.NodeID, model.Annotation{
		ID: permissionPublicationID(batch), Author: "mtix-permissions", Text: PermissionNotice, CreatedAt: batch.Time,
	})
	if errors.Is(err, model.ErrNotFound) {
		return fmt.Errorf("publish permission notice: %w", oplocal.ErrPermissionNoticeUndeliverable)
	}
	return err
}

// PendingRegistrations lists active preconfirmations awaiting this host's registration.
// Historical confirmation rows remain immutable after registration or revocation.
func (s *WakePermissions) PendingRegistrations(ctx context.Context, board PermissionBoard) ([]string, error) {
	grants, err := s.State.AgentGrants()
	if err != nil {
		return nil, fmt.Errorf("read pending registration grants: %w", err)
	}
	rows, err := s.Store.ListRegistrations(ctx, s.HostID, s.UID)
	if err != nil {
		return nil, fmt.Errorf("read pending registrations: %w", err)
	}
	registered := map[string]bool{}
	for _, row := range rows {
		registered[row.AgentID] = true
	}
	var ids []string
	for _, grant := range grants {
		if grant.HostID == s.HostID && grant.BoardID == board.ID && grant.PendingRegistration && !grant.Denied && !grant.Quarantined && !registered[grant.AgentID] {
			ids = append(ids, grant.AgentID)
		}
	}
	sort.Strings(ids)
	return ids, nil
}
