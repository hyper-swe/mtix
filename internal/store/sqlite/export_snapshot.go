// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Export produces a complete JSON export of the database per FR-7.8.
// All nodes, dependencies, agents and sessions share one read-only WAL snapshot.
func (s *Store) Export(ctx context.Context, project, mtixVersion string) (*ExportData, error) {
	data, err := s.readExportSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	sortForChecksum(data.Nodes, data.Dependencies)
	checksum, err := computeExportChecksum(data.Nodes, data.Dependencies)
	if err != nil {
		return nil, fmt.Errorf("compute checksum: %w", err)
	}
	data.Version = 1
	data.SchemaVersion = SchemaVersionV1
	// Keep the injected clock: unchanged stores with a fixed clock export the
	// same bytes; production's default clock retains its existing timestamp.
	data.ExportedAt = s.clock().UTC().Format(time.RFC3339)
	data.MtixVersion = mtixVersion
	data.Project = project
	data.NodeCount = len(data.Nodes)
	data.Checksum = checksum
	return data, nil
}

func (s *Store) readExportSnapshot(ctx context.Context) (data *ExportData, err error) {
	// The reader pool begins deferred; a WAL snapshot never takes writer ownership.
	tx, err := s.readDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, exportReadError("begin export snapshot", err)
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			data = nil
			err = errors.Join(err, exportReadError("rollback export snapshot", rollbackErr))
		}
	}()
	nodes, err := s.exportNodes(ctx, tx)
	if err != nil {
		return nil, exportReadError("export nodes", err)
	}
	deps, err := s.exportDependencies(ctx, tx)
	if err != nil {
		return nil, exportReadError("export dependencies", err)
	}
	agents, err := s.exportAgents(ctx, tx)
	if err != nil {
		return nil, exportReadError("export agents", err)
	}
	sessions, err := s.exportSessions(ctx, tx)
	if err != nil {
		return nil, exportReadError("export sessions", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, exportReadError("commit export snapshot", err)
	}
	return &ExportData{Nodes: nodes, Dependencies: deps, Agents: agents, Sessions: sessions}, nil
}
