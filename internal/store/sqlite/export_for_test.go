// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// MakeExportDep creates an exportDep for use in external tests.
func MakeExportDep(fromID, toID, depType, createdAt string) exportDep {
	return exportDep{
		FromID:    fromID,
		ToID:      toID,
		DepType:   depType,
		CreatedAt: createdAt,
	}
}

// RecomputeExportChecksum recomputes checksum for modified export data in tests.
func RecomputeExportChecksum(t *testing.T, data *ExportData) string {
	t.Helper()
	checksum, err := computeExportChecksum(data.Nodes, data.Dependencies)
	require.NoError(t, err)
	return checksum
}
