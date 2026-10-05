// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
)

// DependencyService owns dependency persistence and committed events per FR-4.
type DependencyService struct{ nodes *NodeService }

// NewDependencyService injects dependency persistence and broadcasts per FR-4/FR-7.5.
func NewDependencyService(st store.Store, bc EventBroadcaster, logger *slog.Logger, clock func() time.Time) *DependencyService {
	return &DependencyService{nodes: NewNodeService(st, bc, nil, logger, clock)}
}

// AddDependency persists a dependency, including cycle rules, per FR-4.3/FR-4.4.
func (svc *DependencyService) AddDependency(ctx context.Context, dep *model.Dependency) error {
	if err := svc.nodes.store.AddDependency(ctx, dep); err != nil {
		return wrapAPIOperation("add dependency", err)
	}
	svc.nodes.broadcastEvent(ctx, EventDependencyAdded, dep.FromID, dep.CreatedBy, nil)
	return nil
}

// RemoveDependency removes the requested edge and broadcasts per FR-4/FR-7.5.
func (svc *DependencyService) RemoveDependency(ctx context.Context, from, to string, kind model.DepType) error {
	if err := svc.nodes.store.RemoveDependency(ctx, from, to, kind); err != nil {
		return wrapAPIOperation("remove dependency", err)
	}
	svc.nodes.broadcastEvent(ctx, EventDependencyRemoved, from, "", nil)
	return nil
}

// GetBlockers retrieves unresolved blocking edges per FR-4.2.
func (svc *DependencyService) GetBlockers(ctx context.Context, id string) ([]*model.Dependency, error) {
	deps, err := svc.nodes.store.GetBlockers(ctx, id)
	if err != nil {
		return nil, wrapAPIOperation(fmt.Sprintf("get blockers of %s", id), err)
	}
	return deps, nil
}

// NewDependencyServiceFromNodeService shares the existing persistence and event
// publisher per FR-4/FR-7.5 without exposing either to handlers.
func NewDependencyServiceFromNodeService(nodes *NodeService) *DependencyService {
	return &DependencyService{nodes: nodes}
}
