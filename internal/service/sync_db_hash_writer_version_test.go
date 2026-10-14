// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"testing"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

// TestExportHash_Format210_IgnoresWriterVersion: the conflict-baseline hash of
// a 2.1.0 board does not depend on the version of the mtix that wrote it, so
// an upgrade raises no false conflict; a 2.0.0 hash is unchanged.
func TestExportHash_Format210_IgnoresWriterVersion(t *testing.T) {
	data := exchangeReference(t, exchangeCurrentJSON)
	hashWith := func(schema, version string) string {
		d := *data
		d.SchemaVersion, d.MtixVersion = schema, version
		h, err := exportHash(&d, formCurrent)
		require.NoError(t, err)
		return h
	}
	require.Equal(t, hashWith(sqlite.SchemaVersionHistory, "0.6.0"), hashWith(sqlite.SchemaVersionHistory, "0.7.0"))
	require.NotEqual(t, hashWith("2.0.0", "0.6.0"), hashWith("2.0.0", "0.7.0"), "2.0.0 hashing is unchanged")
}
