// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

func outboundPGRemote(ctx context.Context, t *testing.T, pool *transport.Pool) error {
	t.Helper()
	remote, err := sqlite.New(filepath.Join(t.TempDir(), "remote.db"), slog.Default())
	if err != nil {
		return err
	}
	t.Cleanup(func() { require.NoError(t, remote.Close()) })
	remote.SetClock(outboundCLIClock)
	if err := remote.CreateNode(ctx, mkPGNode("TEST-9", "", 0, 9, "remote")); err != nil {
		return err
	}
	var stderr bytes.Buffer
	_, err = pushLoop(ctx, &stderr, pool, remote)
	return err
}

func outboundPGDoctor(ctx context.Context, opts transport.Options) (DoctorCheck, error) {
	app.jsonOutput = true
	var stdout, stderr bytes.Buffer
	if err := runSyncDoctor(ctx, &stdout, &stderr, nil, opts); err != nil && !errors.Is(err, errDoctorChecksFailed) {
		return DoctorCheck{}, err
	}
	var report DoctorReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		return DoctorCheck{}, fmt.Errorf("decode doctor JSON: %w", err)
	}
	for _, check := range report.Checks {
		if check.Name == "sync.exchange-record" {
			return check, nil
		}
	}
	return DoctorCheck{}, fmt.Errorf("doctor omitted received provenance")
}

func TestRunSyncPull_PGOutboundThenActualPushAndNewPull_PublishesKnownZero(t *testing.T) {
	pool := openCmdHub(t)
	dsn := requireCmdPG(t)
	outboundCLIInit(t)
	t.Setenv(transport.EnvDSN, dsn)
	var stdout, stderr bytes.Buffer
	opts := transport.Options{InsecureTLS: true}
	require.NoError(t, runSyncPull(t.Context(), &stdout, &stderr, nil, opts, 1))
	before := outboundCLIReceipt(t)
	require.NoError(t, app.store.CreateNode(t.Context(), mkPGNode("TEST-2", "", 0, 2, "local")))
	require.NoError(t, outboundPGRemote(t.Context(), t, pool))
	require.NoError(t, runSyncPull(t.Context(), &stdout, &stderr, nil, opts, 1))
	remote, err := app.store.GetNode(t.Context(), "TEST-9")
	require.NoError(t, err)
	require.Equal(t, "remote", remote.Title)
	require.Equal(t, before, outboundCLIReceipt(t))
	reason, err := outboundCLIReason(t.Context())
	require.NoError(t, err)
	require.Equal(t, "pending-outbound", reason)
	check, err := outboundPGDoctor(t.Context(), opts)
	require.NoError(t, err)
	require.True(t, check.Pass)
	require.True(t, check.Warn)
	require.Contains(t, check.Detail, "pending-outbound")
	require.Contains(t, check.Fix, "new, complete")
	require.NoError(t, runSyncPush(t.Context(), &stdout, &stderr, nil, opts, false))
	require.Equal(t, before, outboundCLIReceipt(t), "actual push acknowledgment cannot publish")
	reason, err = outboundCLIReason(t.Context())
	require.NoError(t, err)
	require.Equal(t, "pending-outbound", reason)
	var pending int
	require.NoError(t, app.store.QueryRow(t.Context(), `SELECT COUNT(*) FROM sync_events WHERE sync_status=?`, "pending").Scan(&pending))
	require.Zero(t, pending)
	require.NoError(t, runSyncPull(t.Context(), &stdout, &stderr, nil, opts, 1))
	require.NotEqual(t, before, outboundCLIReceipt(t))
	outboundCLIRequireZero(t)
}
