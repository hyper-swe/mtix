// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
)

func TestWakeLedgerDoctor_UnavailableReads_ReportFailure(t *testing.T) {
	check := service.WakeLedgerDoctorCheck(nil)
	row := check.Run(t.Context())
	require.Equal(t, service.DoctorFail, row.Status)
	require.Contains(t, row.Fix, "mtix doctor")
	s, _, _ := wakeServiceStore(t)
	require.NoError(t, s.Close())
	row = service.WakeLedgerDoctorCheck(s).Run(t.Context())
	require.Equal(t, service.DoctorFail, row.Status)
	require.Contains(t, row.Detail, "retained wake rows")
	require.Contains(t, row.Fix, "local board")
}

func TestWakeLedgerDoctor_ProtocolReadFailure_RetainsCounts(t *testing.T) {
	s, _, _ := wakeServiceStore(t)
	cause := errors.New("protocol metadata unavailable")
	row := service.WakeLedgerDoctorCheck(wakeProtocolFault{Store: s, cause: cause}).Run(t.Context())
	require.Equal(t, service.DoctorFail, row.Status)
	require.Contains(t, row.Detail, cause.Error())
	require.Contains(t, row.Detail, "0 wake rows")
	require.Contains(t, row.Fix, "metadata")
}

func TestWakeLedgerDoctor_InvalidFloor_ReportsMetadataRepair(t *testing.T) {
	s, _, _ := wakeServiceStore(t)
	row := service.WakeLedgerDoctorCheck(wakeProtocolFault{Store: s, floor: -1}).Run(t.Context())
	require.Equal(t, service.DoctorFail, row.Status)
	require.Contains(t, row.Fix, "metadata")
	require.NotContains(t, row.Fix, "protocol -1")
}
