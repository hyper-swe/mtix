// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests the stock doctor registration against retained wake rows and protocol metadata.
package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func TestDoctor_WakeLedger_DuplicateRegistrationPreservesOriginal(t *testing.T) {
	doctorCollectionApp(t)
	svc := service.NewDoctorService(service.DoctorOptions{})
	require.NoError(t, svc.Register(service.DoctorCheck{ID: "wake.ledger", Run: func(context.Context) service.DoctorResult {
		return service.DoctorResult{Status: service.DoctorInfo, Detail: "original diagnostic"}
	}}))
	err := registerConfiguredWakeLedgerDoctor(svc)
	require.ErrorIs(t, err, model.ErrAlreadyExists)
	require.ErrorContains(t, err, "register wake ledger diagnostic")
	rows, err := svc.Run(t.Context(), service.DoctorRunOptions{})
	require.NoError(t, err)
	count := 0
	for _, row := range rows {
		if row.ID == "wake.ledger" {
			count++
			require.Equal(t, "original diagnostic", row.Detail)
		}
	}
	require.Equal(t, 1, count)
}

func TestDoctorCLI_WakeLedger_HelpDescribesRefusal(t *testing.T) {
	root := doctorCLIBoard(t, "")
	out := doctorCLIExecute(t, root, "", "doctor", "--help")
	require.Equal(t, 0, out.exit, out.stderr)
	require.Empty(t, out.stderr)
	for _, phrase := range []string{"wake.ledger", "retained", "newer dispatch protocol reports FAIL", "compatible binary", "restart the dispatcher", "never prunes wake rows or lowers the protocol floor"} {
		require.Contains(t, out.stdout, phrase)
	}
}

func seedWakeDoctorBoard(t *testing.T, protocol int) (string, *sqlite.Store) {
	t.Helper()
	root := doctorCLIBoard(t, "")
	s, err := sqlite.New(filepath.Join(root, ".mtix", "data", "mtix.db"), nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	s.SetClock(func() time.Time { return time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC) })
	key := model.WakeKey{AgentID: "test-agent", HostID: "test-host", UID: "test-uid", EventSeq: 7, Epoch: 1}
	claim, won, err := s.ClaimWake(t.Context(), key, time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	require.NoError(t, s.RecordWakeOutcome(t.Context(), claim, model.WakeError))
	key.EventSeq++
	_, won, err = s.ClaimWake(t.Context(), key, time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	require.NoError(t, s.SetMinDispatchProtocol(t.Context(), protocol))
	return root, s
}

func assertEmptyWakeDoctorInfo(t *testing.T, id, status, detail, fix string) bool {
	t.Helper()
	switch id {
	case "wake.ledger":
		require.Contains(t, detail, "0 wake rows")
		require.Contains(t, detail, "0 claimed")
	case "budget.rewind":
		require.Equal(t, "No protected recipient accounting exists. Identity-less restores at an unseen path may under-count history, never over-count. An identity-less database at a previously used path is treated as that path's board and can charge its own history. Existing recipient credits and debt are retained.", detail)
	default:
		return false
	}
	require.Equal(t, "info", status)
	require.Empty(t, fix)
	return true
}

func TestDoctorCLI_WakeLedger_ReportsCountsWithoutRepair(t *testing.T) {
	for _, tc := range []struct {
		name     string
		protocol int
		exit     int
		status   string
	}{{"supported", 1, 0, "info"}, {"newer floor", 2, 2, "fail"}} {
		t.Run(tc.name, func(t *testing.T) {
			root, s := seedWakeDoctorBoard(t, tc.protocol)
			before, err := s.WakeLedgerStats(t.Context())
			require.NoError(t, err)
			out := doctorCLIExecute(t, root, "", "doctor", "--json", "--fix")
			require.Equal(t, tc.exit, out.exit, out.stderr)
			found := false
			for _, row := range doctorCLIRows(t, out.stdout) {
				if row["id"] == "wake.ledger" {
					found = true
					require.Equal(t, tc.status, row["status"])
					require.Contains(t, row["detail"], "2 wake rows")
					require.Contains(t, row["detail"], "1 claimed")
				}
			}
			require.True(t, found, "stock doctor must register retained wake-ledger diagnostics")
			after, err := s.WakeLedgerStats(t.Context())
			require.NoError(t, err)
			require.Equal(t, before, after)
			floor, err := s.MinDispatchProtocol(t.Context())
			require.NoError(t, err)
			require.Equal(t, tc.protocol, floor)
		})
	}
}
