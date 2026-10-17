// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// exportSchemaCheckID is the id of the export schema doctor row.
const exportSchemaCheckID = "export.schema"

// exportSchemaFix is the remedy printed when .mtix/tasks.json declares a
// schema other than the one this binary writes (MTIX-168).
const exportSchemaFix = "every write re-exports .mtix/tasks.json in this binary's schema, and sync.auto_sync does not stop it " +
	"(it turns off automatic import only); to upgrade deliberately, run mtix sync and, when it reports no pending import, " +
	"mtix sync --fix, then commit .mtix/tasks.json and update any reader that rejects unknown fields; to keep the older " +
	"schema, write to this board only with the mtix version that wrote it"

// registerExportSchemaCheck adds the export schema check (MTIX-168) when the
// board has a .mtix/tasks.json: PASS when the schema_version it declares is
// the one this binary writes, WARN with the fix when it differs, because the
// next write rewrites the file in this binary's schema. The expected schema
// follows history.export. The file is read once, here.
func registerExportSchemaCheck(svc *service.DoctorService) error {
	if app.mtixDir == "" || app.configSvc == nil {
		return nil
	}
	fileVersion, err := boardDeclaredSchema(filepath.Join(app.mtixDir, "tasks.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil // no board file to compare
	}
	row := exportSchemaResult(fileVersion, currentExportSchema(app.configSvc.HistoryExport()), err)
	return svc.Register(service.DoctorCheck{ID: exportSchemaCheckID, Run: func(context.Context) service.DoctorResult { return row }})
}

// currentExportSchema returns the schema_version this binary writes.
func currentExportSchema(historyExport bool) string {
	if historyExport {
		return sqlite.SchemaVersionHistory
	}
	return sqlite.SchemaVersionV1
}

// boardDeclaredSchema returns the schema_version the export file at path
// declares; an export without one reads as 1.0.0 (FR-15.2g).
func boardDeclaredSchema(path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec // the board's fixed export file under the project's .mtix directory
	if err != nil {
		return "", fmt.Errorf("open %s: %w", filepath.Base(path), err)
	}
	defer func() { _ = f.Close() }()
	var head struct {
		SchemaVersion string `json:"schema_version"`
	}
	if err := json.NewDecoder(f).Decode(&head); err != nil {
		return "", fmt.Errorf("read schema_version of %s: %w", filepath.Base(path), err)
	}
	if head.SchemaVersion == "" {
		return "1.0.0", nil
	}
	return head.SchemaVersion, nil
}

func exportSchemaResult(fileVersion, exportVersion string, err error) service.DoctorResult {
	if err != nil {
		return service.DoctorResult{Status: service.DoctorWarn,
			Detail: fmt.Sprintf("could not read the schema of .mtix/tasks.json: %v", err),
			Fix:    "check .mtix/tasks.json (for example with git diff) and run mtix sync to see whether an import of it is pending"}
	}
	if fileVersion == exportVersion {
		return service.DoctorResult{Status: service.DoctorPass,
			Detail: fmt.Sprintf(".mtix/tasks.json declares schema %s, the schema this binary writes", fileVersion)}
	}
	return service.DoctorResult{Status: service.DoctorWarn, Fix: exportSchemaFix,
		Detail: fmt.Sprintf(".mtix/tasks.json declares schema %s, but this binary writes schema %s; "+
			"the next write rewrites the file in schema %s", fileVersion, exportVersion, exportVersion)}
}
