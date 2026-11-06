// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/sync/pushlock"
)

// TestSeedFreshStore_StartsHookScanFloorAtTailAndRecordsHubMark: the cloned
// history wakes no lane (the floor is the journal tail) and the store counts as
// hub-connected with the worktree's secrets as the evidence.
func TestSeedFreshStore_StartsHookScanFloorAtTailAndRecordsHubMark(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	hub := &fakeLateHub{pullEvents: offlineEvents(t)}
	st, err := sqlite.New(filepath.Join(t.TempDir(), "fresh"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	require.NoError(t, seedFreshStore(ctx, st, io.Discard, hub))

	tail, err := st.JournalTail(ctx)
	require.NoError(t, err)
	require.Positive(t, tail, "the hub history was cloned")
	var floor int64
	require.NoError(t, st.QueryRow(ctx, `SELECT cursor FROM hook_dispatch_cursor WHERE id = 1`).Scan(&floor))
	assert.Equal(t, tail, floor, "the hook scan floor starts at the tail: no wake for cloned history")
	status, err := service.NewHubMarkService(st).Inspect(ctx, service.HubEvidence{})
	require.NoError(t, err)
	assert.True(t, status.Connected)
}

// TestSeedFreshStore_EmptyHub_StillRecordsTheHubMark: with no history to infer
// the mark from, the secrets evidence alone makes the store a hub board.
func TestSeedFreshStore_EmptyHub_StillRecordsTheHubMark(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	st, err := sqlite.New(filepath.Join(t.TempDir(), "fresh"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	require.NoError(t, seedFreshStore(ctx, st, io.Discard, &fakeLateHub{}))

	mark, err := st.HubMark(ctx)
	require.NoError(t, err)
	assert.True(t, mark.Set)
	assert.Equal(t, "secrets", mark.Source)
}

func TestSeedFreshStore_StoreAlreadyHoldsEvents_RefusedBeforeAnyPull(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	hub := &fakeLateHub{pullEvents: offlineEvents(t)}
	st, err := sqlite.New(filepath.Join(t.TempDir(), "fresh"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	require.NoError(t, st.CreateNode(ctx, mkPGNode("TEST-1", "", 0, 1, "written by another process")))

	err = seedFreshStore(ctx, st, io.Discard, hub)

	require.ErrorIs(t, err, model.ErrConflict)
	assert.Empty(t, hub.pullCalls, "nothing is cloned over a store that holds events")
}

// writerWorktree builds a hub-connected worktree store whose events are all
// settled, plus a second open handle: another process that has the store open.
func writerWorktree(t *testing.T) (root string, writer *sqlite.Store) {
	t.Helper()
	root, writer, _ = writerWorktreeOnHub(t)
	return root, writer
}

// writerWorktreeOnHub is writerWorktree that also returns the events the hub
// holds for the store's node: the worktree's journal says they were applied
// from it, so the hub that serves them proves the node is there.
func writerWorktreeOnHub(t *testing.T) (root string, writer *sqlite.Store, hubEvents []*model.SyncEvent) {
	t.Helper()
	ctx := context.Background()
	base, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	common := filepath.Join(base, "common", ".git")
	admin := filepath.Join(common, "worktrees", "lane")
	root = filepath.Join(base, "wt")
	require.NoError(t, os.MkdirAll(admin, 0o700))
	require.NoError(t, os.MkdirAll(worktreeDataDir(root), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: "+admin+"\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(admin, "commondir"), []byte(common+"\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(admin, "gitdir"), []byte(filepath.Join(root, ".git")+"\n"), 0o600))
	st, err := sqlite.New(worktreeDataDir(root), slog.Default())
	require.NoError(t, err)
	require.NoError(t, st.CreateNode(ctx, mkPGNode("TEST-1", "", 0, 1, "settled work")))
	hubEvents, err = readPendingBatch(ctx, st, 100)
	require.NoError(t, err)
	for i, e := range hubEvents {
		e.CreatedAt = offlineEventsPushedAt.Add(time.Duration(i) * time.Second)
	}
	_, err = st.WriteDB().ExecContext(ctx, `UPDATE sync_events SET sync_status = 'applied'`)
	require.NoError(t, err)
	_, err = service.NewHubMarkService(st).HubConnected(ctx, service.HubEvidence{SecretsDSN: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return root, st, hubEvents
}

// TestEnrolWorktree_WriterOpenDuringEnrol_EventIsNeverLost: another process that
// had the store open writes while the clone runs. The write lands in the
// moved-aside store through its open descriptor; enrolment must notice, put the
// original store back with that event in it, and not record the enrolment.
func TestEnrolWorktree_WriterOpenDuringEnrol_EventIsNeverLost(t *testing.T) {
	ctx := context.Background()
	root, writer, hubEvents := writerWorktreeOnHub(t)
	var recorded bool
	hub := hubOf(hubEvents)
	ports := newWorktreeEnrolPortsWith(slog.Default(), io.Discard, fakeDial(hub),
		func(context.Context, service.EnrollmentRequest) error { recorded = true; return nil },
		func(context.Context, string) (bool, error) { return recorded, nil })
	ports.Clone = func(c context.Context, r string) error {
		require.NoError(t, cloneWorktreeFrom(c, slog.Default(), io.Discard, r, hub)) // the clone the board proof then accepts
		// The concurrent writer commits an event now.
		return writer.CreateNode(c, mkPGNode("TEST-2", "", 0, 2, "written during the enrolment"))
	}
	svc := service.NewWorktreeEnrolment(service.WorktreeEnrolOptions{Ports: ports, Clock: func() time.Time { return time.Date(2026, 11, 3, 1, 2, 3, 0, time.UTC) }})

	_, err := svc.EnrolWorktree(ctx, service.EnrollmentRequest{Path: root, Mode: "yes", Source: "test"})

	require.ErrorIs(t, err, model.ErrConflict)
	assert.False(t, recorded, "the enrolment is not recorded over an incomplete carry")
	require.NoError(t, writer.Close())
	back, err := sqlite.New(worktreeDataDir(root), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = back.Close() })
	_, getErr := back.GetNode(ctx, "TEST-2")
	require.NoError(t, getErr, "the event the concurrent writer committed is in the restored store")
	_, getErr = back.GetNode(ctx, "TEST-1")
	require.NoError(t, getErr)
	marker, err := service.ReadEnrolMarker(filepath.Join(root, ".mtix"))
	require.NoError(t, err)
	assert.Nil(t, marker)
}

func TestInitApp_EnrolmentRunningOrInterrupted_RefusesBeforeCreatingAStore(t *testing.T) {
	identityOperatorState(t)
	saveAndResetApp(t)
	tmp := t.TempDir()
	mtixDir := filepath.Join(tmp, ".mtix")
	require.NoError(t, os.MkdirAll(mtixDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(mtixDir, "config.yaml"), []byte("prefix: TEST\n"), 0o644))
	old, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Chdir(old) })
	require.NoError(t, os.Chdir(tmp))
	require.NoError(t, service.WriteEnrolMarker(mtixDir, service.EnrolMarker{Aside: "aside", Backup: "bk", Started: "t", PID: 77}))

	err = initApp(&cobra.Command{Use: "list"}, "")
	require.ErrorIs(t, err, service.ErrEnrolInterrupted)
	assert.Nil(t, app.store)
	_, statErr := os.Lstat(filepath.Join(mtixDir, "data"))
	assert.ErrorIs(t, statErr, os.ErrNotExist, "no store is created in the data directory the enrolment moved")

	lock, err := pushlock.AcquireNamed(filepath.Join(mtixDir, "enroll.d"), "enroll.lock")
	require.NoError(t, err)
	defer func() { require.NoError(t, lock.Release()) }()
	err = initApp(&cobra.Command{Use: "doctor"}, "")
	require.ErrorIs(t, err, service.ErrEnrolInProgress, "even doctor is refused while the enrolment runs")
	assert.Contains(t, err.Error(), "pid 77")
}

func TestDoctor_EnrolInterrupted_ReportsMarkerWithExactRecovery(t *testing.T) {
	initTestApp(t)
	aside := filepath.Join(filepath.Dir(app.mtixDir), "data.pre-enrol-T")
	require.NoError(t, os.MkdirAll(aside, 0o700))
	require.NoError(t, service.WriteEnrolMarker(app.mtixDir, service.EnrolMarker{Aside: aside, Backup: "/x/bk.db", Started: "2026-11-03T01:02:03Z", PID: 9}))
	svc := service.NewDoctorService(service.DoctorOptions{})

	require.NoError(t, registerEnrolInterruptedDoctor(svc, app.mtixDir))

	rows, runErr := svc.Run(context.Background(), service.DoctorRunOptions{})
	require.Error(t, runErr, "a failing check fails the doctor run")
	var found *service.DoctorResult
	for i := range rows {
		if rows[i].ID == "board.enrol-interrupted" {
			found = &rows[i]
		}
	}
	require.NotNil(t, found)
	assert.Equal(t, service.DoctorFail, found.Status)
	assert.Contains(t, found.Fix, aside)
	assert.Contains(t, found.Fix, "in-progress.json")
	assert.Equal(t, aside, found.Data["aside_dir"])
}

func TestDoctor_NoMarker_RegistersNothing(t *testing.T) {
	initTestApp(t)
	svc := service.NewDoctorService(service.DoctorOptions{})
	require.NoError(t, registerEnrolInterruptedDoctor(svc, app.mtixDir))
	rows, err := svc.Run(context.Background(), service.DoctorRunOptions{})
	require.NoError(t, err)
	assert.Empty(t, rows)
}
