// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"fmt"
	"math"

	"github.com/hyper-swe/mtix/internal/model"
)

// ProgressReport is one consistent view of a node's progress and the
// exclusion metadata that explains it per FR-5.6, FR-5.6a and FR-5.6b.
// Every surface (REST, CLI JSON, MCP, gRPC) renders this one value.
type ProgressReport struct {
	ID                        string          `json:"id"`
	Title                     string          `json:"title"`
	Status                    model.Status    `json:"status"`
	Progress                  float64         `json:"progress"`
	DoneCount                 int             `json:"done_count"`
	TotalCount                int             `json:"total_count"`
	ExcludedCount             int             `json:"excluded_count"`
	InvalidatedCount          int             `json:"invalidated_count"`
	HasInvalidatedDescendants bool            `json:"has_invalidated_descendants"`
	AllChildrenExcluded       bool            `json:"all_children_excluded"`
	Children                  []ProgressChild `json:"children"`
}

// ProgressChild is a direct child as seen in the same snapshot.
type ProgressChild struct {
	ID       string       `json:"id"`
	Title    string       `json:"title"`
	Status   model.Status `json:"status"`
	Progress float64      `json:"progress"`
}

// isInvalidated reports whether a node is pending re-evaluation (FR-5.6).
func isInvalidated(n *model.Node) bool {
	return n.InvalidatedAt != nil || n.Status == model.StatusInvalidated
}

// isProgressExcluded reports whether a direct child leaves the denominator
// (FR-5.4, FR-5.6).
func isProgressExcluded(n *model.Node) bool {
	return n.Status == model.StatusCancelled || isInvalidated(n)
}

// GetProgressReport reads the subtree once (a single store query, so one
// snapshot) and derives progress and exclusion metadata from it. A read
// failure is returned as an error and never as zero progress.
func (svc *NodeService) GetProgressReport(ctx context.Context, id string) (*ProgressReport, error) {
	nodes, err := svc.store.GetTree(ctx, id, math.MaxInt32)
	if err != nil {
		return nil, wrapAPIOperation(fmt.Sprintf("get progress of %s", id), err)
	}
	if len(nodes) == 0 || nodes[0].ID != id {
		return nil, wrapAPIOperation(fmt.Sprintf("get progress of %s", id),
			fmt.Errorf("subtree of %s is missing its root: %w", id, model.ErrNotFound))
	}
	return buildProgressReport(nodes[0], nodes[1:]), nil
}

// buildProgressReport derives the report from the root and its descendants.
func buildProgressReport(root *model.Node, descendants []*model.Node) *ProgressReport {
	r := &ProgressReport{ID: root.ID, Title: root.Title, Status: root.Status,
		Progress: root.Progress, Children: []ProgressChild{}}
	children := 0
	for _, n := range descendants {
		if isInvalidated(n) {
			r.InvalidatedCount++
		}
		if n.ParentID != root.ID {
			continue
		}
		children++
		r.Children = append(r.Children, ProgressChild{ID: n.ID, Title: n.Title, Status: n.Status, Progress: n.Progress})
		switch {
		case isProgressExcluded(n):
			r.ExcludedCount++
		case n.Status == model.StatusDone:
			r.DoneCount++
			r.TotalCount++
		default:
			r.TotalCount++
		}
	}
	r.HasInvalidatedDescendants = r.InvalidatedCount > 0
	r.AllChildrenExcluded = children > 0 && r.TotalCount == 0
	return r
}
