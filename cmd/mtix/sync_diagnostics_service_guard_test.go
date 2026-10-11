// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/stretchr/testify/require"
)

func TestSyncDiagnostics_MissingServiceFailsWithoutPanic(t *testing.T) {
	for _, mode := range []string{"status", "doctor"} {
		t.Run(mode, func(t *testing.T) {
			pendingExportDoctorFixture(t)
			app.syncSvc = nil
			var out, errOut bytes.Buffer
			var err error
			require.NotPanics(t, func() {
				if mode == "status" {
					err = runSyncStatus(context.Background(), &out, &errOut)
				} else {
					err = runSyncDoctor(context.Background(), &out, &errOut, nil, transport.Options{})
				}
			})
			if mode == "status" {
				require.ErrorContains(t, err, "local sync service not initialized")
				require.Empty(t, out.String())
				return
			}
			require.ErrorIs(t, err, errDoctorChecksFailed)
			require.Contains(t, out.String(), "[FAIL] pending exports")
			require.Contains(t, out.String(), "local sync service not initialized")
		})
	}
}
