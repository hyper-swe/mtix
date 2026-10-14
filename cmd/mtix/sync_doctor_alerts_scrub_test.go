// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests nested operator diagnostic output through the central sync scrubber.
package main

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/oplocal"
)

func TestScrubDoctorReport_RecentAlerts_MasksNestedDiagnostic(t *testing.T) {
	saveAndResetApp(t)
	source := DoctorReport{Checks: []DoctorCheck{{Name: "alerts.recent", Recent: []oplocal.Alert{{Agent: "postgres://test:syntheticPassword@invalid.test/db", Detail: "postgres://test:syntheticPassword@invalid.test/db", Fix: "postgres://test:syntheticPassword@invalid.test/db"}}}}}
	result := scrubDoctorReport(source)
	data, err := json.Marshal(result)
	require.NoError(t, err)
	require.NotContains(t, string(data), "syntheticPassword")
	require.Contains(t, source.Checks[0].Recent[0].Agent, "syntheticPassword", "scrubbing preserves the supplied report")
}
