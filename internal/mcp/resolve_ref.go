// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"errors"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// nodeRefResolver is the slice of the store MCP reference resolution needs: a
// node fetch plus the uid resolver from MTIX-30.1 (ADR-003 §5). store.Store
// satisfies it.
type nodeRefResolver interface {
	GetNode(ctx context.Context, id string) (*model.Node, error)
	ResolveDisplayPathByUID(ctx context.Context, uid string) (string, error)
}

// resolveNodeRef resolves an MCP node reference to its live node, routing
// through the durable uid so a stale-but-renamed reference still resolves
// (ADR-003 §5).
//
// Agents reference nodes by the dot-path display id (ADR-003 §3, §8), which is
// the common path. A reference that is not a valid display id is treated as a
// durable uid and resolved uid -> display_path -> node, so a node re-resolved by
// uid after a renumber is still found. Agents never need to surface a uid for
// the normal case; this only adds tolerance, it does not change the surface.
//
// Returns model.ErrNotFound if neither path resolves.
func resolveNodeRef(ctx context.Context, r nodeRefResolver, ref string) (*model.Node, error) {
	if ref == "" {
		return nil, fmt.Errorf("node reference is required: %w", model.ErrInvalidInput)
	}

	if model.ValidateNodeID(ref) == nil {
		return r.GetNode(ctx, ref)
	}

	displayPath, err := r.ResolveDisplayPathByUID(ctx, ref)
	if err != nil {
		if errors.Is(err, model.ErrNotFound) {
			return nil, fmt.Errorf("node %q not found by id or uid: %w", ref, model.ErrNotFound)
		}
		return nil, err
	}
	return r.GetNode(ctx, displayPath)
}
