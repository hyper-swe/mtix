// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Host passes use real SQLite registration, inbox, guarded delivery and protected operator state.
package service_test

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/testutil"
)

type hostFixture struct {
	t                         *testing.T
	state                     *oplocal.State
	root, key, instance, host string
	store                     *sqlite.Store
	now                       time.Time
	dials, hooks              int
	secrets                   []string
	adapter                   *scanAdapter
}

func newHostFixture(t *testing.T) *hostFixture {
	t.Helper()
	f := &hostFixture{t: t, state: testutil.OperatorState(t), root: t.TempDir(), now: time.Date(2026, 10, 30, 5, 0, 0, 0, time.UTC), adapter: &scanAdapter{}}
	var hostErr error
	f.host, hostErr = f.state.HostID()
	require.NoError(t, hostErr)
	dir := filepath.Join(f.root, ".mtix")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "data"), 0700))
	var err error
	f.key, err = service.CreateBoardID(dir)
	require.NoError(t, err)
	f.store, err = sqlite.New(filepath.Join(dir, "data", "mtix.db"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, f.store.Close()) })
	f.store.SetClock(func() time.Time { return f.now })
	f.instance, err = f.store.StoreInstanceID(t.Context())
	require.NoError(t, err)
	f.adapter.live.Store(true)
	require.NoError(t, f.state.ListBoardInstance(t.Context(), oplocal.ListedBoard{Key: f.key, Path: f.root, StoreInstanceID: f.instance}))
	n := &model.Node{ID: "TEST-1", Project: "TEST", Seq: 1, Title: "Task", Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1, NodeType: model.NodeTypeStory, Creator: "sender", ContentHash: model.ComputeContentHash("Task", "", "", "", nil), CreatedAt: f.now, UpdatedAt: f.now}
	require.NoError(t, f.store.CreateNode(t.Context(), n))
	return f
}

func (f *hostFixture) board() *service.HostBoard {
	f.t.Helper()
	b, err := service.NewHostBoard(service.HostBoardOptions{Store: f.store, State: f.state, Path: f.root, Host: service.RegistrationHost{HostID: f.host, UID: "test-uid", Version: "test", Protocol: model.AttachProtocol}, PID: 42, Clock: func() time.Time { return f.now }, Liveness: &scanLiveness{}, Adapters: []service.WakeAdapter{f.adapter},
		Contact: service.NewContactScheduler(func() time.Time { return f.now }, func() (service.ContactInputs, error) {
			return service.ContactInputs{Mode: service.HubContactMode{Kind: service.HubContactInterval, Interval: time.Second}}, nil
		}),
		Pull: func(_ context.Context, dsn string) error { f.dials++; f.secrets = append(f.secrets, dsn); return nil }, Hooks: func(context.Context) { f.hooks++ }})
	require.NoError(f.t, err)
	return b
}

func (f *hostFixture) pass() error {
	d, err := service.NewHostDispatcher(service.HostDispatcherOptions{State: f.state, OpenBoard: func(context.Context, oplocal.ListedBoard) (*service.HostBoard, error) { return f.board(), nil }})
	require.NoError(f.t, err)
	return d.Pass(f.t.Context())
}

func TestHostDispatcher_ListedOnlyBoard_NoDialNoHook(t *testing.T) {
	f := newHostFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(f.root, ".mtix", "secrets"), []byte("checkout-secret"), 0600))
	require.NoError(t, f.pass())
	require.Zero(t, f.dials)
	require.Zero(t, f.hooks)
	beats, err := f.store.ListDispatcherHeartbeats(t.Context(), f.host, "test-uid")
	require.NoError(t, err)
	require.Len(t, beats, 1)
	require.EqualValues(t, 42, beats[0].PID)
}

func TestHostDispatcher_EnrolledBoard_UsesCheckoutSecretsOnly(t *testing.T) {
	f := newHostFixture(t)
	t.Setenv("MTIX_SYNC_DSN", "exported-host-secret")
	require.NoError(t, os.WriteFile(filepath.Join(f.root, ".mtix", "secrets"), []byte("checkout-secret"), 0600))
	enroll := service.NewHostEnrollment(service.HostEnrollmentOptions{State: f.state, Clock: func() time.Time { return f.now }, OpenBoard: func(context.Context, string) (*service.HostBoard, error) { return f.board(), nil }, ObserveHub: func(_ context.Context, dsn string) (oplocal.HubIdentity, error) {
		require.Equal(t, "checkout-secret", dsn)
		return oplocal.HubIdentity{Kind: "postgres", ProjectPrefix: "TEST", FirstEventHash: "observed"}, nil
	}})
	require.NoError(t, enroll.Enroll(t.Context(), service.EnrollmentRequest{Path: f.root, Mode: "yes", Source: "test"}))
	require.NoError(t, f.pass())
	require.Equal(t, []string{"checkout-secret"}, f.secrets)
	require.Equal(t, 1, f.hooks)
	require.NoError(t, enroll.Unenroll(t.Context(), service.EnrollmentRequest{Path: f.root, Mode: "yes", Source: "test"}))
	require.NoError(t, f.pass())
	require.Equal(t, 1, f.dials)
	require.Equal(t, 1, f.hooks)
	records, err := enroll.List(t.Context())
	require.NoError(t, err)
	require.Empty(t, records)
	n, err := f.store.GetNode(t.Context(), "TEST-1")
	require.NoError(t, err)
	require.Len(t, n.Annotations, 2)
	for _, a := range n.Annotations {
		require.Equal(t, service.HostEnrollmentNotice, a.Text)
		require.NotContains(t, a.Text, f.root)
	}
}

func TestHostDispatcher_ChangedStoreInstance_RefusesBeforeEffects(t *testing.T) {
	f := newHostFixture(t)
	_, err := f.store.WriteDB().ExecContext(t.Context(), "UPDATE meta SET value=? WHERE key=?", "22222222-2222-4222-8222-222222222222", "store_instance_id")
	require.NoError(t, err)
	require.Error(t, f.pass())
	require.Zero(t, f.dials)
	require.Zero(t, f.hooks)
}

func (f *hostFixture) event() {
	f.t.Helper()
	_, err := f.store.WriteDB().ExecContext(f.t.Context(), `INSERT INTO agent_registrations
 (agent_id,harness,uid,host_id,origin,epoch,high_water,pending_hold,registered_at,last_seen,registrar_version,attach_protocol)
 VALUES (?,?,?,?,?,?,0,0,?,?,?,?)`, "worker", "claude", "test-uid", f.host, "manual", 1, f.now.Format(time.RFC3339), f.now.Format(time.RFC3339), "test", model.AttachProtocol)
	require.NoError(f.t, err)
	require.NoError(f.t, f.store.SetAnnotations(f.t.Context(), "TEST-1", []model.Annotation{{ID: "01ANN00001", Author: "sender", Addressee: "worker", Text: "private body", CreatedAt: f.now}}))
}

func TestHostDispatcher_GuardedScanner_FencedOnceAndProtocolFloor(t *testing.T) {
	f := newHostFixture(t)
	f.event()
	require.NoError(t, f.pass())
	require.Len(t, f.adapter.seqs(), 1)
	require.NoError(t, f.pass())
	require.Len(t, f.adapter.seqs(), 1)
	var attempt model.WakeAttempt
	var outcome model.WakeOutcome
	require.NoError(t, f.store.ReadDB().QueryRowContext(t.Context(), "SELECT agent_id,host_id,uid,event_seq,epoch,attempt_token,outcome FROM wake_ledger").Scan(&attempt.Key.AgentID, &attempt.Key.HostID, &attempt.Key.UID, &attempt.Key.EventSeq, &attempt.Key.Epoch, &attempt.Token, &outcome))
	require.NotEmpty(t, attempt.Token)
	require.Equal(t, model.WakeSent, outcome)
	for _, tc := range []struct {
		token string
		want  error
	}{{"", model.ErrInvalidInput}, {"stale-" + attempt.Token, model.ErrConflict}} {
		stale := attempt
		stale.Token = tc.token
		require.ErrorIs(t, f.store.RecordWakeResult(t.Context(), stale, model.WakeResult{Outcome: model.WakeUndeliverable, Reason: "late-stale-result"}), tc.want)
	}
	var retainedToken string
	require.NoError(t, f.store.ReadDB().QueryRowContext(t.Context(), "SELECT attempt_token,outcome FROM wake_ledger").Scan(&retainedToken, &outcome))
	require.Equal(t, attempt.Token, retainedToken)
	require.Equal(t, model.WakeSent, outcome)
	require.NoError(t, f.store.SetMinDispatchProtocol(t.Context(), model.AttachProtocol+1))
	require.ErrorIs(t, f.pass(), service.ErrWakeProtocolUnsupported)
	require.Len(t, f.adapter.seqs(), 1)
}

func TestHostDispatcher_ManualPullIntoUnenrolledWorktree_WakesNothing(t *testing.T) {
	f := newHostFixture(t)
	f.event()
	_, err := f.store.MarkHubConnected(t.Context(), "applied_events")
	require.NoError(t, err)
	hub := oplocal.HubIdentity{Kind: "postgres", ProjectPrefix: "TEST", FirstEventHash: "manual-pull"}
	require.NoError(t, f.state.ObserveBoard(f.root, f.key, hub))
	grants := service.NewWakePermissions(service.PermissionDeps{Store: f.store, State: f.state, HostID: f.host, UID: "test-uid", Clock: func() time.Time { return f.now }})
	require.NoError(t, grants.ApplyBatch(t.Context(), service.PermissionBoard{ID: f.key, Checkout: f.root, Hub: hub}, oplocal.PermissionBatch{Mode: "yes", Source: "test operator", Changes: []oplocal.PermissionChange{{AgentID: "worker", Action: "allow"}}}))
	require.NoError(t, f.pass())
	require.Empty(t, f.adapter.seqs())
	require.Zero(t, f.dials)
	require.Zero(t, f.hooks)
}

func TestHostDispatcher_EmptyCandidateSet_ProtocolFloorStillEnforced(t *testing.T) {
	f := newHostFixture(t)
	require.NoError(t, f.store.SetMinDispatchProtocol(t.Context(), model.AttachProtocol+1))
	require.ErrorIs(t, f.pass(), service.ErrWakeProtocolUnsupported)
	require.Empty(t, f.adapter.seqs())
}
