// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// Real-Postgres tests of the hub-board worktree enrolment (MTIX-112.17.16).
// Gated on MTIX_PG_TEST_DSN through requireCmdPG.

type enrolPGFixture struct {
	pool     *transport.Pool
	root     string
	data     string
	ev1      string
	ev2      string
	enrolled []service.EnrollmentRequest
}

// seedEnrolHub puts two messages for lane-a on the hub, and builds a used
// worktree: a store that pulled them, with the first message acked.
func newEnrolPGFixture(t *testing.T) *enrolPGFixture {
	t.Helper()
	ctx := context.Background()
	pool := openCmdHub(t)
	dsn := requireCmdPGDSNOnly(t)
	t.Setenv(transport.EnvDSN, "") // the worktree's own secrets are the only source

	seed, err := sqlite.New(filepath.Join(t.TempDir(), "seed"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = seed.Close() })
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, seed.CreateNode(ctx, mkPGNode("TEST-1", "", 0, 1, "task one")))
	require.NoError(t, seed.CreateNode(ctx, mkPGNode("TEST-2", "", 0, 2, "task two")))
	require.NoError(t, seed.SetAnnotations(ctx, "TEST-1", []model.Annotation{{ID: "a1", Author: "seed", Text: "first", CreatedAt: now, Addressee: "lane-a"}}))
	require.NoError(t, seed.SetAnnotations(ctx, "TEST-2", []model.Annotation{{ID: "a2", Author: "seed", Text: "second", CreatedAt: now, Addressee: "lane-a"}}))
	var seedErr bytes.Buffer
	_, err = pushLoop(ctx, &seedErr, pool, seed)
	require.NoError(t, err, seedErr.String())

	f := &enrolPGFixture{pool: pool}
	f.buildWorktree(t, dsn)
	return f
}

func requireCmdPGDSNOnly(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv(envCmdPGTestDSN)
	require.NotEmpty(t, dsn)
	return cmdPGFixtureDSN(t, dsn)
}

func (f *enrolPGFixture) buildWorktree(t *testing.T, dsn string) {
	t.Helper()
	ctx := context.Background()
	base, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	common := filepath.Join(base, "common", ".git")
	admin := filepath.Join(common, "worktrees", "lane")
	f.root = filepath.Join(base, "wt")
	f.data = filepath.Join(f.root, ".mtix", "data")
	require.NoError(t, os.MkdirAll(admin, 0o700))
	require.NoError(t, os.MkdirAll(f.data, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(f.root, ".git"), []byte("gitdir: "+admin+"\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(admin, "commondir"), []byte(common+"\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(admin, "gitdir"), []byte(filepath.Join(f.root, ".git")+"\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(f.root, ".mtix", "secrets"), []byte(dsn+"\n"), 0o600))

	st, err := sqlite.New(f.data, slog.Default())
	require.NoError(t, err)
	_, _, err = pullLoop(ctx, testIngest(nil), f.pool, st, transport.PullCursor{}, 100)
	require.NoError(t, err)
	inbox, err := st.InboxList(ctx, "lane-a")
	require.NoError(t, err)
	require.Len(t, inbox, 2)
	f.ev1, f.ev2 = inbox[0].EventID, inbox[1].EventID
	require.NoError(t, st.InboxAck(ctx, "lane-a", inbox[0].Seq))
	require.NoError(t, st.Close())
}

func (f *enrolPGFixture) service() *service.WorktreeEnrolment {
	ports := newWorktreeEnrolPorts(slog.Default(), io.Discard, transport.Options{InsecureTLS: true}, func(_ context.Context, r service.EnrollmentRequest) error {
		f.enrolled = append(f.enrolled, r)
		return nil
	})
	return service.NewWorktreeEnrolment(service.WorktreeEnrolOptions{Ports: ports})
}

func (f *enrolPGFixture) enrol() (*service.WorktreeEnrolResult, error) {
	return f.service().EnrolWorktree(context.Background(), service.EnrollmentRequest{Path: f.root, Mode: "yes", Source: "test"})
}

func (f *enrolPGFixture) open(t *testing.T) *sqlite.Store {
	t.Helper()
	st, err := sqlite.New(f.data, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestEnrolWorktree_UsedStore_AcksCarriedAndReplyReachesHub_PG(t *testing.T) {
	ctx := context.Background()
	f := newEnrolPGFixture(t)

	res, err := f.enrol()
	require.NoError(t, err)

	require.Len(t, f.enrolled, 1)
	assert.DirExists(t, res.AsideDir)
	assert.FileExists(t, res.BackupPath)
	st := f.open(t)
	left, err := st.InboxList(ctx, "lane-a")
	require.NoError(t, err)
	require.Len(t, left, 1, "the acked message is not delivered again")
	assert.Equal(t, f.ev2, left[0].EventID, "the unacked message is delivered again")

	// The enrolled worktree's lane replies; the reply reaches the hub.
	now := time.Now().UTC()
	require.NoError(t, st.SetAnnotations(ctx, "TEST-2", []model.Annotation{
		{ID: "a2", Author: "seed", Text: "second", CreatedAt: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), Addressee: "lane-a"},
		{ID: "reply", Author: "lane-a", Text: "done", CreatedAt: now, Addressee: "seed"},
	}))
	pending := pendingEventIDs(t, st)
	require.Len(t, pending, 1)
	var pushErr bytes.Buffer
	_, err = pushLoop(ctx, &pushErr, f.pool, st)
	require.NoError(t, err, pushErr.String())
	onHub, err := f.pool.FetchEventsByID(ctx, pending)
	require.NoError(t, err)
	assert.Len(t, onHub, 1, "the reply reached the hub from the enrolled worktree")
}

func pendingEventIDs(t *testing.T, st *sqlite.Store) []string {
	t.Helper()
	evs, err := st.LocalEvents(context.Background())
	require.NoError(t, err)
	var out []string
	for _, e := range evs {
		if e.Status == "pending" {
			out = append(out, e.ID)
		}
	}
	return out
}

func TestEnrolWorktree_PendingEventOrHubLackingEvent_RefusedNothingMoved_PG(t *testing.T) {
	for _, kind := range []string{"pending", "pushed but not on the hub"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			f := newEnrolPGFixture(t)
			st := f.open(t)
			require.NoError(t, st.CreateNode(ctx, mkPGNode("TEST-9", "", 0, 9, "local work")))
			if kind != "pending" {
				_, err := st.WriteDB().ExecContext(ctx, `UPDATE sync_events SET sync_status = 'pushed' WHERE sync_status = 'pending'`)
				require.NoError(t, err)
			}
			require.NoError(t, st.Close())
			_, statErr := os.Stat(filepath.Join(f.data, "mtix.db"))
			require.NoError(t, statErr)

			_, err := f.enrol()

			var refusal *service.WorktreeLocalEventsError
			require.ErrorAs(t, err, &refusal)
			assert.Contains(t, err.Error(), "TEST-9")
			assert.Empty(t, f.enrolled)
			kept := f.open(t)
			left, listErr := kept.InboxList(ctx, "lane-a")
			require.NoError(t, listErr)
			assert.Len(t, left, 1, "the store is untouched: its ack is still there")
			matches, _ := filepath.Glob(filepath.Join(f.root, ".mtix", "data.pre-enrol-*"))
			assert.Empty(t, matches, "nothing was moved aside")
		})
	}
}

func TestEnrolWorktree_EnrolFails_StoreRestoredAndUsable_PG(t *testing.T) {
	ctx := context.Background()
	f := newEnrolPGFixture(t)
	ports := newWorktreeEnrolPorts(slog.Default(), io.Discard, transport.Options{InsecureTLS: true}, func(context.Context, service.EnrollmentRequest) error {
		return io.ErrUnexpectedEOF
	})
	svc := service.NewWorktreeEnrolment(service.WorktreeEnrolOptions{Ports: ports})

	_, err := svc.EnrolWorktree(ctx, service.EnrollmentRequest{Path: f.root, Mode: "yes", Source: "test"})

	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	st := f.open(t)
	left, err := st.InboxList(ctx, "lane-a")
	require.NoError(t, err)
	require.Len(t, left, 1, "the original store is back with its ack")
	matches, _ := filepath.Glob(filepath.Join(f.root, ".mtix", "data.pre-enrol-*"))
	assert.Empty(t, matches)
}
