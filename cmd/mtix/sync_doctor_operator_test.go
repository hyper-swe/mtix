// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package main

import (
	"bytes"
	"context"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestSyncDoctor_PlacementLimit_WarnsWithoutChangingPass(t *testing.T) {
	report := appendOperatorPlacementCheck(DoctorReport{OverallPass: true})
	require.True(t, report.OverallPass)
	require.Len(t, report.Checks, 1)
	require.True(t, report.Checks[0].Warn)
	require.True(t, report.Checks[0].Pass)
	require.Contains(t, report.Checks[0].Fix, "XDG_CONFIG_HOME")
	require.Contains(t, report.Checks[0].Fix, "group-writable")
	require.Contains(t, report.Checks[0].Fix, "directory-service")
	report = appendOperatorPlacementCheck(DoctorReport{})
	require.False(t, report.OverallPass)
}
func TestRunSyncDoctor_MissingDSN_ReportsPlacementLimit(t *testing.T) {
	initTestApp(t)
	t.Setenv(transport.EnvDSN, "")
	var out, errOut bytes.Buffer
	app.jsonOutput = true
	require.ErrorIs(t, runSyncDoctor(context.Background(), &out, &errOut, nil, transport.Options{}), errDoctorChecksFailed)
	check := doctorCheckFromJSON(t, out.Bytes(), "operator state placement")
	require.Equal(t, oplocal.PlacementLimit, check.Detail)
	require.True(t, check.Pass)
	require.True(t, check.Warn)
	require.Contains(t, check.Fix, "mtix hooks status --json")
	app.jsonOutput = false
	out.Reset()
	require.ErrorIs(t, runSyncDoctor(context.Background(), &out, &errOut, nil, transport.Options{}), errDoctorChecksFailed)
	require.Contains(t, out.String(), "[WARN] operator state placement")
	require.Contains(t, out.String(), oplocal.PlacementLimit)
}
