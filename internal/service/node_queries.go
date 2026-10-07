// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
)

// NodeFilter is the service query contract per FR-17.1 and MP-3.
type NodeFilter store.NodeFilter

// ListOptions controls service query pagination per FR-7.6.
type ListOptions store.ListOptions

// NodeUpdate carries partial node changes across the service boundary per FR-7.3.
type NodeUpdate store.NodeUpdate

// ProjectInfo identifies project scopes and counts per MP-4.
type ProjectInfo store.ProjectInfo

// ApplyUpdate validates and persists a service update per FR-7.3 and FR-7.5.
func (svc *NodeService) ApplyUpdate(ctx context.Context, id string, update *NodeUpdate) error {
	return svc.UpdateNode(ctx, id, (*store.NodeUpdate)(update))
}

// ListNodes queries filtered nodes per FR-17.1 and FR-7.6.
func (svc *NodeService) ListNodes(ctx context.Context, filter NodeFilter, opts ListOptions) ([]*model.Node, int, error) {
	nodes, total, err := svc.store.ListNodes(ctx, store.NodeFilter(filter), store.ListOptions(opts))
	if err != nil {
		return nil, 0, wrapAPIOperation("list nodes", err)
	}
	return nodes, total, nil
}

// GetActivity retrieves activity entries per FR-3.6.
func (svc *NodeService) GetActivity(ctx context.Context, id string, limit, offset int) ([]model.ActivityEntry, error) {
	entries, err := svc.store.GetActivity(ctx, id, limit, offset)
	if err != nil {
		return nil, wrapAPIOperation(fmt.Sprintf("get activity for %s", id), err)
	}
	return entries, nil
}

// GetDirectChildren retrieves child nodes per FR-7.3.
func (svc *NodeService) GetDirectChildren(ctx context.Context, id string) ([]*model.Node, error) {
	nodes, err := svc.store.GetDirectChildren(ctx, id)
	if err != nil {
		return nil, wrapAPIOperation(fmt.Sprintf("get children of %s", id), err)
	}
	return nodes, nil
}

// GetAncestorChain retrieves the ordered context chain per FR-12.2.
func (svc *NodeService) GetAncestorChain(ctx context.Context, id string) ([]*model.Node, error) {
	nodes, err := svc.store.GetAncestorChain(ctx, id)
	if err != nil {
		return nil, wrapAPIOperation(fmt.Sprintf("get ancestors of %s", id), err)
	}
	return nodes, nil
}

// GetSiblings retrieves neighboring context nodes per FR-12.2.
func (svc *NodeService) GetSiblings(ctx context.Context, id string) ([]*model.Node, error) {
	nodes, err := svc.store.GetSiblings(ctx, id)
	if err != nil {
		return nil, wrapAPIOperation(fmt.Sprintf("get siblings of %s", id), err)
	}
	return nodes, nil
}

// DistinctProjects retrieves project scopes per MP-4.
func (svc *NodeService) DistinctProjects(ctx context.Context) ([]ProjectInfo, error) {
	projects, err := svc.store.DistinctProjects(ctx)
	if err != nil {
		return nil, wrapAPIOperation("list projects", err)
	}
	if projects == nil {
		return nil, nil
	}
	out := make([]ProjectInfo, len(projects))
	for i, p := range projects {
		out[i] = ProjectInfo(p)
	}
	return out, nil
}

// ResolveDisplayPathByUID retrieves the current display ID per ADR-003 §5.
// The UID remains stable across renumbering; errors retain transport messages.
func (svc *NodeService) ResolveDisplayPathByUID(ctx context.Context, uid string) (string, error) {
	path, err := svc.store.ResolveDisplayPathByUID(ctx, uid)
	if err != nil {
		return "", wrapAPIOperation("resolve node UID", err)
	}
	return path, nil
}
