// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
)

type fakeTrips struct {
	rows []oplocal.Alert
	err  error
}

func (f fakeTrips) Recent(context.Context, int) ([]oplocal.Alert, error) { return f.rows, f.err }

func TestWakeGuardTripsDoctor_Branches(t *testing.T) {
	at := time.Date(2026, 10, 28, 1, 2, 3, 0, time.UTC)
	var many []oplocal.Alert
	for i := 0; i < 25; i++ {
		many = append(many, oplocal.Alert{Agent: fmt.Sprintf("a%d", i), Class: oplocal.AlertClassWakeBackoff, LastAt: at, FirstAt: at, Count: 1, Fix: "mtix config set wake.backoff_after_unacked 6"})
	}
	for _, tc := range []struct {
		name   string
		reader service.WakeGuardTripsReader
		status service.DoctorStatus
		trips  int
	}{
		{"nil reader fails", nil, service.DoctorFail, 0},
		{"read error warns", fakeTrips{err: errors.New("denied")}, service.DoctorWarn, 0},
		{"no trips is info", fakeTrips{rows: []oplocal.Alert{{Agent: "a", Class: oplocal.AlertClassBudgetRewind}}}, service.DoctorInfo, 0},
		{"trips are bounded to twenty", fakeTrips{rows: many}, service.DoctorWarn, 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := service.WakeGuardTripsDoctorCheck(tc.reader).Run(t.Context())
			require.Equal(t, tc.status, got.Status)
			if tc.trips > 0 {
				require.Len(t, got.Data["trips"], tc.trips)
				require.Equal(t, "mtix config set wake.backoff_after_unacked 6", got.Fix)
			}
		})
	}
}
