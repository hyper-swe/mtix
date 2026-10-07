// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"database/sql"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/sync/workflow"
	"time"
)

// Keep durable test fixtures and fault-injection seams outside handlers.
type InboxStore interface {
	InboxList(context.Context, string) ([]sqlite.InboxEvent, error)
	InboxWait(context.Context, string, time.Duration) ([]sqlite.InboxEvent, error)
	InboxAck(context.Context, string, int64) error
}

func testInboxService(backend InboxStore, acknowledgers ...service.InboxAckStore) *service.InboxService {
	var ack service.InboxAckStore = backend
	if len(acknowledgers) > 0 && acknowledgers[0] != nil {
		ack = acknowledgers[0]
	}
	return service.NewInboxServiceWithReads(ack, backend)
}

func testReadNodeService(backend store.Store) *service.NodeService {
	return service.NewNodeService(backend, nil, nil, nil, time.Now)
}

func testDependencyService(backend store.Store) *service.DependencyService {
	return service.NewDependencyServiceFromNodeService(testReadNodeService(backend))
}

type testWorkflowService struct{ db *sql.DB }

func (s testWorkflowService) DetectState(ctx context.Context, dir string) (workflow.Report, error) {
	return workflow.DetectState(ctx, s.db, dir)
}
