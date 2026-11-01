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
	return s.finishExport(data, project, mtixVersion)
}

func (s *Store) finishExport(data *ExportData, project, mtixVersion string) (*ExportData, error) {
	sortForChecksum(data.Nodes, data.Dependencies)
	checksum, err := computeExportChecksumH(data.Nodes, data.Dependencies, data.History)
	if err != nil {
		return nil, fmt.Errorf("compute checksum: %w", err)
	}
	idsChecksum, err := computeIDsChecksum(data.Nodes, data.History)
	if err != nil {
		return nil, fmt.Errorf("compute ids checksum: %w", err)
	}
	data.IDsChecksum = idsChecksum
	data.Version = 1
	data.SchemaVersion = SchemaVersionV1
	if data.historyOn {
		data.SchemaVersion = SchemaVersionHistory
		if mtixVersion == "" {
			mtixVersion = s.writerVersion
		}
	}
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
	return s.readExportSnapshotWith(ctx, nil)
}

func (s *Store) readExportSnapshotWith(ctx context.Context, boundary func(*sql.Tx) error) (data *ExportData, err error) {
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
	history, err := s.readExportHistory(ctx, tx)
	if err != nil {
		return nil, exportReadError("export history", err)
	}
	if s.afterExportBoardRead != nil {
		s.afterExportBoardRead()
	}
	if boundary != nil {
		if err := boundary(tx); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, exportReadError("commit export snapshot", err)
	}
	out := &ExportData{Nodes: nodes, Dependencies: deps, Agents: agents, Sessions: sessions}
	if history != nil {
		out.historyOn = true
		out.History = attachHistory(out.Nodes, history)
	}
	return out, nil
}
