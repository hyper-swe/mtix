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
	"strconv"
	"strings"

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

// exportSchemaNewerFix is the remedy when .mtix/tasks.json declares a schema
// NEWER than this binary writes: the binary is the stale side (MTIX-168).
const exportSchemaNewerFix = "this mtix is older than the one that wrote .mtix/tasks.json: upgrade mtix to that version or newer " +
	"before writing to this board, because every write re-exports the file in this binary's older schema " +
	"(sync.auto_sync does not stop that); do not run mtix sync --fix with this binary"

// schemaNewer reports whether the dotted numeric version a is newer than b;
// a version that does not parse is never reported as newer (MTIX-168).
func schemaNewer(a, b string) bool {
	av, aok := parseSchemaVersion(a)
	bv, bok := parseSchemaVersion(b)
	if !aok || !bok {
		return false
	}
	for i := range av {
		if av[i] != bv[i] {
			return av[i] > bv[i]
		}
	}
	return false
}

// parseSchemaVersion parses MAJOR.MINOR.PATCH into three numbers.
func parseSchemaVersion(v string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(v, ".")
	if len(parts) != len(out) {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

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
	fix := exportSchemaFix
	if schemaNewer(fileVersion, exportVersion) {
		fix = exportSchemaNewerFix
	}
	return service.DoctorResult{Status: service.DoctorWarn, Fix: fix,
		Detail: fmt.Sprintf(".mtix/tasks.json declares schema %s, but this binary writes schema %s; "+
			"the next write rewrites the file in schema %s", fileVersion, exportVersion, exportVersion)}
}
