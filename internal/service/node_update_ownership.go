// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"fmt"

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
)

// UpdateIdentity names who performs an update (MTIX-325).
type UpdateIdentity struct {
	Actor  string
	Force  bool
	Reason string
	System bool
}

type updateIdentityKey struct{}

// WithUpdateIdentity attaches the acting identity to ctx.
func WithUpdateIdentity(ctx context.Context, id UpdateIdentity) context.Context {
	return context.WithValue(ctx, updateIdentityKey{}, id)
}

// guardAssigneeChange refuses an update that clears or changes another
// agent's claim unless the identity is the assignee, a system actor, or
// forced with a reason (MTIX-325). A forced change attaches an audit entry
// that the store writes with the update. It fails closed without identity.
func (svc *NodeService) guardAssigneeChange(ctx context.Context, id string, updates *store.NodeUpdate) error {
	if updates.Assignee == nil {
		return nil
	}
	ident, _ := ctx.Value(updateIdentityKey{}).(UpdateIdentity)
	if ident.System {
		return nil
	}
	node, err := svc.store.GetNode(ctx, id)
	if err != nil {
		return wrapAPIOperation(fmt.Sprintf("update node %s", id), err)
	}
	prev := node.Assignee
	if prev == "" || prev == *updates.Assignee || prev == ident.Actor {
		return nil
	}
	if !ident.Force {
		return fmt.Errorf("update node %s: claimed by %s, not %s; pass --force to change it: %w",
			id, format.SafeLine(prev), format.SafeLine(ident.Actor), model.ErrAlreadyClaimed)
	}
	if err := validateForcedUpdate(id, ident); err != nil {
		return err
	}
	updates.Audit = forcedUpdateAudit(ident, prev, *updates.Assignee)
	return nil
}

func validateForcedUpdate(id string, ident UpdateIdentity) error {
	if err := validateAgentID(ident.Actor); err != nil {
		return err
	}
	if ident.Actor == "" {
		return fmt.Errorf("update node %s: forced assignee change needs an acting agent identity: %w", id, model.ErrInvalidInput)
	}
	if ident.Reason == "" {
		return fmt.Errorf("update node %s: forced assignee change needs a reason: %w", id, model.ErrInvalidInput)
	}
	return nil
}

func forcedUpdateAudit(ident UpdateIdentity, prev, next string) *model.ActivityEntry {
	if next == "" {
		return &model.ActivityEntry{
			Type: model.ActivityTypeUnclaim, Author: ident.Actor,
			Text: fmt.Sprintf("forced unclaim by %s of claim held by %s: %s", ident.Actor, prev, ident.Reason),
		}
	}
	return &model.ActivityEntry{
		Type: model.ActivityTypeClaim, Author: ident.Actor,
		Text: fmt.Sprintf("forced assignee change by %s of claim held by %s to %s: %s", ident.Actor, prev, next, ident.Reason),
	}
}
