// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// taskHistoryReader is implemented by stores that keep task change history.
type taskHistoryReader interface {
	ListTaskHistory(ctx context.Context, id string, limit int) ([]model.HistoryEntry, error)
}

// DefaultHistoryLimit is how many history entries `mtix show` lists by default.
const DefaultHistoryLimit = 50

// GetNodeHistory returns the change history of a task, newest first, at most
// limit entries (DefaultHistoryLimit when limit is zero or less). Free-text
// fields carry no old or new text (MTIX-95.31.19.4, ADR-006 A3). A store that
// keeps no history yields none.
func (svc *NodeService) GetNodeHistory(ctx context.Context, id string, limit int) ([]model.HistoryEntry, error) {
	if limit < 1 {
		limit = DefaultHistoryLimit
	}
	reader, ok := svc.store.(taskHistoryReader)
	if !ok {
		return nil, nil
	}
	entries, err := reader.ListTaskHistory(ctx, id, limit)
	if err != nil {
		return nil, wrapAPIOperation(fmt.Sprintf("get history of %s", id), err)
	}
	return entries, nil
}
