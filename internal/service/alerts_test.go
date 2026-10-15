// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests operator diagnostics against actual board journal and lease state.
package service_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
)

func operatorAlertsFixture(t *testing.T) *oplocal.State {
	t.Helper()
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	root, err := os.MkdirTemp(home, ".mtix-alert-service-test-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(root)) })
	root, err = filepath.EvalSymlinks(root)
	require.NoError(t, err)
	return oplocal.New(oplocal.Env{GOOS: runtime.GOOS, Home: root, Values: map[string]string{"APPDATA": filepath.Join(root, "config")}})
}

func TestAlertsRaise_InjectedClock_CountsRecent(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	svc := service.NewAlerts(operatorAlertsFixture(t), func() time.Time { return now })
	input := service.AlertRequest{Agent: "test-agent", Class: oplocal.AlertClassWakeBackoff}
	first, err := svc.Raise(t.Context(), input)
	require.NoError(t, err)
	require.Equal(t, uint64(1), first.Count)
	now = now.Add(time.Minute)
	second, err := svc.Raise(t.Context(), input)
	require.NoError(t, err)
	require.Equal(t, uint64(2), second.Count)
	rows, err := svc.Recent(t.Context(), 0)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, second, rows[0])
}

func TestAlertRaise_NeverWritesJournalEvent(t *testing.T) {
	nodes, st, bc := newTestNodeService(t)
	_, err := nodes.CreateNode(t.Context(), &service.CreateNodeRequest{Project: "TEST", Title: "diagnostic boundary"})
	require.NoError(t, err)
	require.NoError(t, st.WithTx(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(t.Context(), "INSERT INTO wake_leases VALUES (?,?,?,?,?,?,?,?)", "test-agent", "test-host", "test-uid", "0123456789abcdef0123456789abcdef", 7, "2026-10-12T12:00:00Z", "2026-10-12T12:15:00Z", 1)
		return err
	}))
	var journalBefore, journalAfter int
	require.NoError(t, st.QueryRow(t.Context(), "SELECT COUNT(*) FROM sync_events").Scan(&journalBefore))
	var highBefore, dirtyBefore, highAfter, dirtyAfter int
	require.NoError(t, st.QueryRow(t.Context(), "SELECT high_water,dirty FROM wake_leases WHERE agent_id=?", "test-agent").Scan(&highBefore, &dirtyBefore))
	broadcasts := len(bc.events)
	svc := service.NewAlerts(operatorAlertsFixture(t), func() time.Time { return time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC) })
	_, err = svc.Raise(t.Context(), service.AlertRequest{Agent: "test-agent", Class: oplocal.AlertClassWakeBackoff})
	require.NoError(t, err)
	rows, err := svc.Recent(t.Context(), 0)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.NoError(t, st.QueryRow(t.Context(), "SELECT COUNT(*) FROM sync_events").Scan(&journalAfter))
	require.NoError(t, st.QueryRow(t.Context(), "SELECT high_water,dirty FROM wake_leases WHERE agent_id=?", "test-agent").Scan(&highAfter, &dirtyAfter))
	require.Equal(t, journalBefore, journalAfter)
	require.Equal(t, highBefore, highAfter)
	require.Equal(t, dirtyBefore, dirtyAfter)
	require.Len(t, bc.events, broadcasts)
}

func TestAlerts_ConstructorInputs_RequireDependencies(t *testing.T) {
	state := operatorAlertsFixture(t)
	for _, tc := range []struct {
		name  string
		state *oplocal.State
		clock func() time.Time
	}{{"state", nil, time.Now}, {"clock", state, nil}} {
		t.Run(tc.name, func(t *testing.T) { require.Panics(t, func() { service.NewAlerts(tc.state, tc.clock) }) })
	}
}

func TestAlerts_InvalidRequests_ReturnsWrappedErrors(t *testing.T) {
	svc := service.NewAlerts(operatorAlertsFixture(t), func() time.Time { return time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC) })
	for _, agent := range []string{"", " ", strings.Repeat("x", 65), "\xff", "test\x1b", "test\u200b"} {
		t.Run(fmt.Sprintf("agent-%x", agent), func(t *testing.T) {
			_, err := svc.Raise(t.Context(), service.AlertRequest{Agent: agent, Class: oplocal.AlertClassWakeBackoff})
			require.ErrorIs(t, err, model.ErrInvalidInput)
		})
	}
	_, err := svc.Raise(t.Context(), service.AlertRequest{Agent: "test", Class: "unknown"})
	require.ErrorIs(t, err, model.ErrInvalidInput)
	_, err = svc.Recent(t.Context(), -1)
	require.ErrorIs(t, err, model.ErrInvalidInput)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = svc.Raise(ctx, service.AlertRequest{Agent: "test", Class: oplocal.AlertClassWakeBackoff})
	require.ErrorIs(t, err, context.Canceled)
	_, err = svc.Recent(ctx, 0)
	require.ErrorIs(t, err, context.Canceled)
}
