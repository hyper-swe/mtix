// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSyncDoctorAlertsHelp_DescribesLocalCheck(t *testing.T) {
	cmd := newSyncDoctorCmd()
	require.Contains(t, cmd.Long, "alerts.recent")
	require.Contains(t, cmd.Long, "latest 20")
	require.Contains(t, cmd.Long, "never wake")
}
