// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests read-only retention reporting through the actual doctor registry.
package service_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

func TestWakeLedgerDoctor_RetainedRows_FixNeverWrites(t *testing.T) {
	for _, floor := range []int{0, 1, 2} {
		t.Run(map[int]string{0: "legacy floor", 1: "current floor", 2: "newer floor"}[floor], func(t *testing.T) {
			s, _, _ := wakeServiceStore(t)
			_, won, err := s.ClaimWake(t.Context(), wakeServiceKey(), time.Minute)
			require.NoError(t, err)
			require.True(t, won)
			require.NoError(t, s.SetMinDispatchProtocol(t.Context(), floor))
			check := service.WakeLedgerDoctorCheck(s)
			require.Nil(t, check.Fixer)
			doctor := service.NewDoctorService(service.DoctorOptions{})
			require.NoError(t, doctor.Register(check))
			var before, after int64
			require.NoError(t, s.WriteDB().QueryRow("SELECT total_changes()").Scan(&before))
			rows, err := doctor.Run(t.Context(), service.DoctorRunOptions{Fix: true})
			if floor > model.AttachProtocol {
				require.ErrorIs(t, err, service.ErrDoctorChecksFailed)
				require.Contains(t, rows[0].Fix, "protocol 2")
			} else {
				require.NoError(t, err)
				require.Equal(t, service.DoctorInfo, rows[0].Status)
			}
			require.Len(t, rows, 1)
			require.Equal(t, "wake.ledger", rows[0].ID)
			require.Contains(t, rows[0].Detail, "1 wake rows")
			require.Contains(t, rows[0].Detail, "1 claimed")
			require.NoError(t, s.WriteDB().QueryRow("SELECT total_changes()").Scan(&after))
			require.Equal(t, before, after)
			stats, err := s.WakeLedgerStats(t.Context())
			require.NoError(t, err)
			require.Equal(t, model.WakeLedgerStats{Rows: 1, Claimed: 1}, stats)
		})
	}
}
