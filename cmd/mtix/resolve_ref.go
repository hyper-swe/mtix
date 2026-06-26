// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// nodeRefResolver is the slice of the store CLI reference resolution needs: a
// node fetch plus the uid resolvers from MTIX-30.1 (ADR-003 §5). *sqlite.Store
// satisfies it.
type nodeRefResolver interface {
	GetNode(ctx context.Context, id string) (*model.Node, error)
	ResolveDisplayPathByUID(ctx context.Context, uid string) (string, error)
}

// resolveNodeRef resolves a node reference to its live node, routing through the
// durable uid so a stale-but-renamed reference still resolves (ADR-003 §5).
//
// The surface reference is the dot-path display id (ADR-003 §3): a well-formed
// id is looked up directly. A reference that is NOT a valid display id is
// treated as a durable uid and resolved uid -> display_path -> node, so a
// reference recorded as a uid (or a renumbered node re-resolved by uid) is still
// found. This keeps resolution reference-survives-renumber correct without ever
// requiring callers to surface a uid for the common case.
//
// Returns model.ErrNotFound if neither path resolves.
func resolveNodeRef(ctx context.Context, r nodeRefResolver, ref string) (*model.Node, error) {
	if ref == "" {
		return nil, fmt.Errorf("node reference is required: %w", model.ErrInvalidInput)
	}

	// A well-formed display path is the primary, common-case reference.
	if model.ValidateNodeID(ref) == nil {
		return r.GetNode(ctx, ref)
	}

	// Otherwise treat the reference as a durable uid and re-derive the current
	// display path before fetching — this is the path that survives a renumber.
	displayPath, err := r.ResolveDisplayPathByUID(ctx, ref)
	if err != nil {
		if errors.Is(err, model.ErrNotFound) {
			return nil, fmt.Errorf("node %q not found by id or uid: %w", ref, model.ErrNotFound)
		}
		return nil, err
	}
	return r.GetNode(ctx, displayPath)
}
