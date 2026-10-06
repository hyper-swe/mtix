// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// BackupResult describes a verified backup per FR-6.3a.
type BackupResult struct {
	Path     string
	Size     int64
	Verified bool
}

// IntegrityReport preserves the HTTP structural and UID diagnostics per FR-6.3.
type IntegrityReport struct {
	Status    string
	UIDReport string
}

// AdminService owns backup, verification and lifecycle per FR-6.3/FR-7.1.
type AdminService struct{ backend *sqlite.Store }

// NewAdminService configures the admin service per FR-6.3.
func NewAdminService(st *sqlite.Store) *AdminService { return &AdminService{backend: st} }

// Verify performs the existing SQLite integrity and UID checks per FR-6.3.
func (svc *AdminService) Verify(ctx context.Context) (*IntegrityReport, error) {
	var status string
	// Fixed SQLite structural diagnostic; no dynamic SQL.
	if err := svc.backend.QueryRow(ctx, "PRAGMA integrity_check").Scan(&status); err != nil {
		return nil, wrapAPIOperation("verify integrity", err)
	}
	report, err := svc.backend.DuplicateNodeUIDsReport(ctx)
	if err != nil {
		return nil, wrapAPIOperation("verify UID uniqueness", err)
	}
	return &IntegrityReport{Status: status, UIDReport: report}, nil
}

// Backup creates the existing verified backup per FR-6.3a.
func (svc *AdminService) Backup(ctx context.Context, path string) (*BackupResult, error) {
	result, err := svc.backend.Backup(ctx, path)
	if err != nil {
		return nil, wrapAPIOperation("backup database", err)
	}
	return &BackupResult{Path: result.Path, Size: result.Size, Verified: result.Verified}, nil
}

// Close releases database connections after server shutdown per FR-7.1.
func (svc *AdminService) Close() error {
	if err := svc.backend.Close(); err != nil {
		return wrapAPIOperation("close database", err)
	}
	return nil
}
