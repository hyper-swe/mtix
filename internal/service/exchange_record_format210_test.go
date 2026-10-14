// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"strings"
	"testing"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

// TestExchangeRecord_Format210_IsRecordedLikeCurrent: a file in export format
// 2.1.0 (history export on) gets an exchange record, as 2.0.0 does; a version
// this build does not know is still unavailable.
func TestExchangeRecord_Format210_IsRecordedLikeCurrent(t *testing.T) {
	data := exchangeReference(t, exchangeCurrentJSON)
	data.SchemaVersion = sqlite.SchemaVersionHistory
	r, err := buildExchangeRecord(data, "git-import", strings.Repeat("a", 64), "2026-10-01T00:00:00Z")
	require.NoError(t, err)
	require.Equal(t, "current", r.BoardForm)

	data.SchemaVersion = "2.2.0"
	_, err = buildExchangeRecord(data, "git-import", strings.Repeat("a", 64), "2026-10-01T00:00:00Z")
	require.Error(t, err)
}
