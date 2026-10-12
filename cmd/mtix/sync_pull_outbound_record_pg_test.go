// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Real hub tests verify outbound acknowledgment before a new eligible receipt.
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
	"time"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store"
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
	remote.SetClock(app.store.Now)
	if err := remote.CreateNode(ctx, mkPGNode("TEST-9", "", 0, 9, "remote")); err != nil {
		return err
	}
	pending, err := remote.ReadPendingEvents(ctx, 2)
	if err != nil {
		return err
	}
	require.Len(t, pending, 1)
	require.NoError(t, transport.ValidatePushEvent(pending[0], remote.Now()))
	accepted, conflicts, err := pool.PushEvents(ctx, pending)
	if err != nil {
		return err
	}
	require.Equal(t, []string{pending[0].EventID}, accepted)
	require.Empty(t, conflicts)
	var delivered int
	require.NoError(t, pool.Inner().QueryRow(ctx, `SELECT COUNT(*) FROM sync_events WHERE event_id=$1 AND uid=$2 AND node_id=$3 AND payload=$4::jsonb`, pending[0].EventID, pending[0].UID, pending[0].NodeID, string(pending[0].Payload)).Scan(&delivered))
	require.Equal(t, 1, delivered, "the exact remote identity and content reached the hub")
	if err := markPushed(ctx, remote, accepted); err != nil {
		return err
	}
	var retained, remaining int
	require.NoError(t, remote.QueryRow(ctx, `SELECT COUNT(CASE WHEN event_id=? AND sync_status=? THEN 1 END),COUNT(CASE WHEN sync_status=? THEN 1 END) FROM sync_events`, pending[0].EventID, "pushed", "pending").Scan(&retained, &remaining))
	require.Equal(t, 1, retained)
	require.Zero(t, remaining)
	return nil
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
	require.NoError(t, outboundPGInjectClock(t.Context(), pool))
	require.NoError(t, runSyncPull(t.Context(), &stdout, &stderr, nil, opts, 1))
	before := outboundCLIReceipt(t)
	require.NoError(t, app.store.CreateNode(t.Context(), mkPGNode("TEST-2", "", 0, 2, "local")))
	owned, err := outboundPGUpdate(t.Context())
	require.NoError(t, err)
	require.NotEmpty(t, owned.UID)
	require.Equal(t, 2, owned.Events)
	require.Len(t, owned.Statuses, 2)
	outboundPGAssert(t.Context(), t, owned, "pending")
	require.NoError(t, outboundPGRemote(t.Context(), t, pool))
	require.NoError(t, runSyncPull(t.Context(), &stdout, &stderr, nil, opts, 1))
	remote, err := app.store.GetNode(t.Context(), "TEST-9")
	require.NoError(t, err)
	require.Equal(t, "remote", remote.Title)
	outboundPGAssert(t.Context(), t, owned, "pending")
	received, err := outboundPGSnapshot(t.Context(), "TEST-9")
	require.NoError(t, err)
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
	outboundPGAssert(t.Context(), t, owned, "pushed")
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
	outboundPGAssert(t.Context(), t, owned, "pushed")
	outboundPGAssert(t.Context(), t, received, "")
}

func outboundPGInjectClock(ctx context.Context, pool *transport.Pool) error {
	var observed time.Time
	if err := pool.Inner().QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&observed); err != nil {
		return fmt.Errorf("read fixture clock: %w", err)
	}
	fixed := observed.UTC()
	clock := func() time.Time { return fixed }
	app.store.SetClock(clock)
	app.syncSvc = service.NewSyncService(app.store, slog.Default(), clock)
	return nil
}

type outboundPGState struct {
	ID, UID, Journal string
	Projection       []byte
	Events           int
	Statuses         map[string]string
}

func outboundPGSnapshot(ctx context.Context, id string) (outboundPGState, error) {
	node, err := app.store.GetNode(ctx, id)
	if err != nil {
		return outboundPGState{}, fmt.Errorf("read owned projection: %w", err)
	}
	state := outboundPGState{ID: id, UID: node.UID}
	state.Projection, err = json.Marshal(node)
	if err != nil {
		return state, fmt.Errorf("encode owned projection: %w", err)
	}
	var statuses string
	err = app.store.QueryRow(ctx, `SELECT json_group_array(json_object('event_id',event_id,'node_id',node_id,'uid',uid,'op_type',op_type,'payload',payload)),json_group_object(event_id,sync_status),COUNT(*) FROM (SELECT * FROM sync_events WHERE node_id=? ORDER BY rowid)`, id).Scan(&state.Journal, &statuses, &state.Events)
	if err != nil {
		return state, fmt.Errorf("read owned journal: %w", err)
	}
	if err := json.Unmarshal([]byte(statuses), &state.Statuses); err != nil {
		return state, fmt.Errorf("decode journal status: %w", err)
	}
	return state, nil
}

func outboundPGUpdate(ctx context.Context) (outboundPGState, error) {
	description := "local authored description"
	if err := app.store.UpdateNode(ctx, "TEST-2", &store.NodeUpdate{Description: &description}); err != nil {
		return outboundPGState{}, fmt.Errorf("update owned node: %w", err)
	}
	return outboundPGSnapshot(ctx, "TEST-2")
}

func outboundPGAssert(ctx context.Context, t *testing.T, expected outboundPGState, status string) {
	t.Helper()
	require.NotEmpty(t, expected.UID)
	actual, err := outboundPGSnapshot(ctx, expected.ID)
	require.NoError(t, err)
	require.Equal(t, expected.Projection, actual.Projection)
	require.Equal(t, expected.UID, actual.UID)
	require.Equal(t, expected.Journal, actual.Journal)
	require.Equal(t, expected.Events, actual.Events)
	require.Len(t, actual.Statuses, len(expected.Statuses))
	for id, before := range expected.Statuses {
		require.Contains(t, actual.Statuses, id)
		if status != "" {
			before = status
		}
		require.Equal(t, before, actual.Statuses[id], "same pinned event must survive acknowledgment")
	}
}
