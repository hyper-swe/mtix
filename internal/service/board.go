// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// BoardRequest selects the nodes shown on a board (MTIX-255.1). Filter is the
// shared node filter (Under, LabelsAny, Assignee, Project and so on); its
// Status must be empty because the board's columns are the statuses. Limit is
// the maximum number of items per column and must be at least 1.
type BoardRequest struct {
	Filter NodeFilter
	Limit  int
}

// BoardItem is one card on the board. The JSON shape is a stable contract.
type BoardItem struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Priority int    `json:"priority"`
	Assignee string `json:"assignee"`
}

// BoardColumn groups cards by one status. Count is the total number of
// matching nodes, independent of the item limit; Items is never nil.
type BoardColumn struct {
	Name  string      `json:"name"`
	Count int         `json:"count"`
	Items []BoardItem `json:"items"`
}

// Board is the read-only kanban projection returned by NodeService.Board.
type Board struct {
	Columns []BoardColumn `json:"columns"`
}

// Board builds the read-only status board of MTIX-255.1 (FR-17 filters). Each
// column is one ListNodes query for one status, so every filter and project
// rule of ListNodes applies unchanged and each column count is that query's
// total, which --limit does not affect. Columns follow boardStatuses and
// empty columns are kept so the counts sum to every matching node.
//
// Returns ErrInvalidInput when Limit is below 1 or the filter carries a Status.
func (svc *NodeService) Board(ctx context.Context, req BoardRequest) (*Board, error) {
	if req.Limit < 1 {
		return nil, fmt.Errorf("board limit must be at least 1: %w", model.ErrInvalidInput)
	}
	if len(req.Filter.Status) > 0 {
		return nil, fmt.Errorf("board columns are statuses, status filter not allowed: %w", model.ErrInvalidInput)
	}
	statuses := boardStatuses()
	board := &Board{Columns: make([]BoardColumn, 0, len(statuses))}
	for _, status := range statuses {
		filter := req.Filter
		filter.Status = []model.Status{status}
		nodes, total, err := svc.ListNodes(ctx, filter, ListOptions{Limit: req.Limit})
		if err != nil {
			return nil, fmt.Errorf("board column %s: %w", status, err)
		}
		board.Columns = append(board.Columns, boardColumn(status, nodes, total))
	}
	return board, nil
}

// boardStatuses returns the board's column order: work in flight first, then
// parked, then finished. It holds every model.AllStatuses value, so the column
// counts always sum to every matching node (guarded by a test).
func boardStatuses() []model.Status {
	return []model.Status{
		model.StatusOpen, model.StatusInProgress, model.StatusBlocked, model.StatusDeferred,
		model.StatusDone, model.StatusCancelled, model.StatusInvalidated,
	}
}

// boardColumn projects nodes into a column with a non-nil item slice.
func boardColumn(status model.Status, nodes []*model.Node, total int) BoardColumn {
	items := make([]BoardItem, 0, len(nodes))
	for _, n := range nodes {
		items = append(items, BoardItem{ID: n.ID, Title: n.Title, Priority: int(n.Priority), Assignee: n.Assignee})
	}
	return BoardColumn{Name: string(status), Count: total, Items: items}
}
