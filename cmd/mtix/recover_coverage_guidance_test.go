// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// FR-19.1: recovery help names the clean-coverage condition and redelivery cost.
func TestRecoverHelp_ExplainsUnprovenCoverage(t *testing.T) {
	help := newRecoverCmd().Long
	require.Contains(t, help, "only when")
	require.Contains(t, help, "already-read mail")
	require.Contains(t, help, "mtix export")
	require.NotContains(t, help, "acknowledgement coverage are recovered too")
}

// FR-19.1: the count alone does not establish that the source is damaged.
func TestRecoverInboxNote_HealthySource_DoesNotClaimDamage(t *testing.T) {
	initTestApp(t)
	envelopeCLIEvents(t, "healthy unread mail")
	var out bytes.Buffer
	writeRecoverInboxNote(t.Context(), &out, filepath.Join(app.mtixDir, "data", "mtix.db"))
	require.Contains(t, out.String(), "source database")
	require.NotContains(t, out.String(), "damaged database")
}
