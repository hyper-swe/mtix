// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

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
