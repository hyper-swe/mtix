// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// RerunStrategy defines the strategy for a rerun operation.
type RerunStrategy string

const (
	// RerunAll invalidates all descendants and resets them to open.
	RerunAll RerunStrategy = "all"

	// RerunOpenOnly invalidates and resets only non-done descendants.
	RerunOpenOnly RerunStrategy = "open_only"

	// RerunDelete invalidates each descendant first (FR-3.5b), then soft-deletes.
	RerunDelete RerunStrategy = "delete"

	// RerunReview sets descendants to invalidated for manual review.
	RerunReview RerunStrategy = "review"
)

// Rerun invalidates and processes descendants of a node per FR-6.3.
// Implements four strategies: --all, --open-only, --delete, --review.
// Handles FR-3.5c: auto-reopens terminal parents before processing.
func (svc *NodeService) Rerun(
	ctx context.Context, nodeID string, strategy RerunStrategy, reason, author string,
) error {
	// FR-3.5c: If parent is terminal, auto-reopen it first.
	if err := svc.ensureNonTerminal(ctx, nodeID, author); err != nil {
		return fmt.Errorf("ensure non-terminal %s: %w", nodeID, err)
	}

	// Get all direct children to process.
	children, err := svc.store.GetDirectChildren(ctx, nodeID)
	if err != nil {
		return fmt.Errorf("get children of %s: %w", nodeID, err)
	}

	if len(children) == 0 {
		return nil // Nothing to rerun.
	}

	for _, child := range children {
		if err := svc.rerunDescendant(ctx, child, strategy, reason, author); err != nil {
			return fmt.Errorf("rerun descendant %s: %w", child.ID, err)
		}
	}

	// Broadcast batch invalidation event per FR-7.5a.
	svc.broadcastEvent(ctx, EventNodesInvalidated, nodeID, author, nil)
	svc.broadcastEvent(ctx, EventProgressChanged, nodeID, author, nil)

	return nil
}

// rerunDescendant processes a single descendant and its children (depth-first).
func (svc *NodeService) rerunDescendant(
	ctx context.Context, node *model.Node, strategy RerunStrategy,
	reason, author string,
) error {
	children, err := svc.store.GetDirectChildren(ctx, node.ID)
	if err != nil {
		return fmt.Errorf("get children of %s: %w", node.ID, err)
	}
	for _, child := range children {
		if err := svc.rerunDescendant(ctx, child, strategy, reason, author); err != nil {
			return err
		}
	}

	switch strategy {
	case RerunAll:
		return svc.rerunAllNode(ctx, node, reason, author)
	case RerunOpenOnly:
		return svc.rerunOpenOnlyNode(ctx, node, reason, author)
	case RerunDelete:
		return svc.rerunDeleteNode(ctx, node, reason, author)
	case RerunReview:
		return svc.rerunReviewNode(ctx, node, reason, author)
	default:
		return fmt.Errorf("unknown rerun strategy %q: %w", strategy, model.ErrInvalidInput)
	}
}

// rerunAllNode invalidates the node and resets to open.
func (svc *NodeService) rerunAllNode(
	ctx context.Context, node *model.Node, reason, author string,
) error {
	// Invalidate (state machine allows any → invalidated as auto_only).
	if err := svc.store.TransitionStatus(
		ctx, node.ID, model.StatusInvalidated, reason, author,
	); err != nil {
		return err
	}
	// Reset: invalidated → open (via restore path).
	return svc.store.TransitionStatus(ctx, node.ID, model.StatusOpen, "reset by rerun", author)
}

// rerunOpenOnlyNode invalidates and resets only non-done descendants.
func (svc *NodeService) rerunOpenOnlyNode(
	ctx context.Context, node *model.Node, reason, author string,
) error {
	if node.Status == model.StatusDone {
		return nil // Skip done nodes.
	}
	if err := svc.store.TransitionStatus(
		ctx, node.ID, model.StatusInvalidated, reason, author,
	); err != nil {
		return err
	}
	return svc.store.TransitionStatus(ctx, node.ID, model.StatusOpen, "reset by rerun", author)
}

// rerunDeleteNode invalidates each descendant first (FR-3.5b), then soft-deletes.
func (svc *NodeService) rerunDeleteNode(
	ctx context.Context, node *model.Node, reason, author string,
) error {
	// FR-3.5b: Invalidate BEFORE soft-delete.
	if err := svc.store.TransitionStatus(
		ctx, node.ID, model.StatusInvalidated, reason, author,
	); err != nil {
		return err
	}
	return svc.store.DeleteNode(ctx, node.ID, false, author)
}

// rerunReviewNode sets descendant to invalidated for manual review.
func (svc *NodeService) rerunReviewNode(
	ctx context.Context, node *model.Node, reason, author string,
) error {
	return svc.store.TransitionStatus(ctx, node.ID, model.StatusInvalidated, reason, author)
}

// maxEnsureConflicts bounds how many times ensureNonTerminal re-decides after
// another writer changed the node between its read and its write;
// maxEnsureSteps bounds the total reads, so successful restores cannot loop it
// forever either.
const (
	maxEnsureConflicts = 3
	maxEnsureSteps     = 8
)

// ensureNonTerminal implements FR-3.5c: auto-reopen terminal parents before rerun.
// If the node is invalidated, restore first, then reopen if still terminal.
// Each write is guarded by the status it was decided on (MTIX-151): if another
// writer changed the node in between, the node is re-read and re-decided, so a
// reopen is never applied over a node someone else already moved. A restore
// that commits is progress, not a conflict, and does not use up the conflict
// budget.
func (svc *NodeService) ensureNonTerminal(ctx context.Context, nodeID, author string) error {
	conflicts := 0
	for step := 0; step < maxEnsureSteps && conflicts < maxEnsureConflicts; step++ {
		node, err := svc.store.GetNode(ctx, nodeID)
		if err != nil {
			return err
		}

		switch {
		case node.Status == model.StatusInvalidated:
			// FR-3.5c: If invalidated, restore first; the restore may leave a
			// terminal status, so decide again on a fresh read.
			restoreErr := svc.Restore(ctx, nodeID, author)
			if restoreErr != nil {
				if !errors.Is(restoreErr, model.ErrInvalidTransition) {
					return fmt.Errorf("restore invalidated %s: %w", nodeID, restoreErr)
				}
				conflicts++ // Another writer took it out of invalidated first.
			}
		case node.Status.IsTerminal():
			// If terminal (done/cancelled), auto-reopen from exactly that status.
			err = svc.store.TransitionStatusFrom(
				ctx, nodeID, node.Status, model.StatusOpen,
				"auto-reopened by rerun", author,
			)
			if err == nil || !errors.Is(err, model.ErrConflict) {
				return err
			}
			conflicts++
		default:
			return nil
		}
	}
	return fmt.Errorf(
		"node %s kept changing while preparing rerun; the rerun did not start, retry it: %w",
		nodeID, model.ErrConflict)
}
