// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// readImportFile opens, decodes and checks an import file before anything
// is written: the FR-15.2g schema check first, then --recompute-checksum
// when asked for. Every error it returns means nothing was written
// (MTIX-95.31.1).
func readImportFile(filePath string, f importFlags) (*sqlite.ExportData, error) {
	// MTIX-2.3.1: stream-decode the export straight off the file via
	// sqlite.DecodeExportData (json.Decoder) rather than os.ReadFile +
	// json.Unmarshal, so a large export is not held as both raw bytes and a
	// parsed tree at peak.
	file, err := os.Open(filePath) //nolint:gosec // filePath is an operator-supplied import path
	if err != nil {
		return nil, fmt.Errorf("read import file %s: %w", filePath, err)
	}
	defer func() { _ = file.Close() }()

	exportData, err := sqlite.DecodeExportData(file)
	if err != nil {
		return nil, fmt.Errorf("parse import file: %w", err)
	}

	// FR-15.2g (MTIX-95.31.1): refuse a file from a newer major schema
	// version before anything else, exactly as auto-import does.
	if schemaErr := checkImportSchemaVersion(exportData); schemaErr != nil {
		return nil, schemaErr
	}

	if f.recomputeChecksum {
		// Recovery path: integrity now attests to the reconstructed
		// content, not the original. Be loud about it.
		fmt.Fprintln(os.Stderr,
			"WARNING: --recompute-checksum replaces the file's integrity checksum; the import attests to the reconstructed content, not the original")
		if recompErr := sqlite.RecomputeExportChecksum(exportData); recompErr != nil {
			return nil, fmt.Errorf("recompute checksum: %w", recompErr)
		}
	}
	return exportData, nil
}

// checkImportSchemaVersion refuses an import file whose major
// schema_version is higher than the one this build writes, with the
// FR-15.2g message auto-import logs, before anything is written
// (MTIX-95.31.1). A client older than the file cannot read every field it
// carries, so importing it would lose data.
func checkImportSchemaVersion(data *sqlite.ExportData) error {
	if err := service.CheckSchemaVersion(data.SchemaVersion); err != nil {
		return fmt.Errorf("import failed: %w", err)
	}
	return nil
}
