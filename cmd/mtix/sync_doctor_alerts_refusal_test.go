// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests local refusal diagnostics through the existing doctor command.
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

func TestRunSyncDoctor_OperatorAlerts_UnreadableStateFailsClosed(t *testing.T) {
	for _, format := range []string{"json", "text"} {
		t.Run(format, func(t *testing.T) {
			state := alertDoctorFixture(t)
			seedDoctorAlert(t, state)
			writeProtectedAlertFixture(t, state, "malformed-alert-fixture", "alerts.jsonl", []byte("{\n"))
			path, err := state.Path()
			require.NoError(t, err)
			before, err := os.ReadFile(filepath.Join(path, "alerts.jsonl"))
			require.NoError(t, err)
			app.jsonOutput = format == "json"
			var out, errOut bytes.Buffer
			require.ErrorIs(t, runSyncDoctor(t.Context(), &out, &errOut, nil, transport.Options{}), errDoctorChecksFailed)
			if app.jsonOutput {
				check := doctorCheckFromJSON(t, out.Bytes(), "alerts.recent")
				require.False(t, check.Pass)
				require.False(t, check.Warn)
				require.Contains(t, check.Detail, "operator state unreadable")
				require.Contains(t, check.Fix, "Preserve operator state")
			} else {
				require.Contains(t, out.String(), "[FAIL] alerts.recent")
				require.Contains(t, out.String(), "Preserve operator state")
			}
			after, err := os.ReadFile(filepath.Join(path, "alerts.jsonl"))
			require.NoError(t, err)
			require.Equal(t, before, after)
		})
	}
}
