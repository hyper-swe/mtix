// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
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
	"github.com/hyper-swe/mtix/internal/store"
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
	// notice receives the lost-value notice; dirName is the worktree's directory name.
	notice  io.Writer
	dirName string
}

// seedEnrolHub puts two messages for lane-a on the hub, and builds a used
// worktree: a store that pulled them, with the first message acked.
func newEnrolPGFixture(t *testing.T) *enrolPGFixture { return newEnrolPGFixtureNamed(t, "wt") }

// newEnrolPGFixtureNamed builds the fixture in a worktree directory of the given name.
func newEnrolPGFixtureNamed(t *testing.T, dirName string) *enrolPGFixture {
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

	f := &enrolPGFixture{pool: pool, dirName: dirName}
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
	f.root = filepath.Join(base, f.dirName)
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
	}, func(context.Context, string) (bool, error) { return len(f.enrolled) > 0, nil })
	return service.NewWorktreeEnrolment(service.WorktreeEnrolOptions{Ports: ports, Notice: f.notice})
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
	}, func(context.Context, string) (bool, error) { return false, nil })
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

// writeBoardFromWorktree writes the worktree's tasks.json as its store's own
// export, the state every command leaves it in.
func (f *enrolPGFixture) writeBoardFromWorktree(t *testing.T) {
	t.Helper()
	st := f.open(t)
	require.NoError(t, service.NewSyncService(st, slog.Default(), time.Now).AutoExport(context.Background(), filepath.Join(f.root, ".mtix")))
	require.NoError(t, st.Close())
}

// TestEnrolWorktree_RealHub_ProvenBoard_EnrolsAndTheFirstWriteKeepsEveryNode_PG
// is the production proof of MTIX-112.17.16.6: a worktree whose tasks.json is
// its store's export and whose nodes are all on the real hub enrols through the
// shipped ports, the first command leaves the clone alone, and the first write
// keeps every node and reaches the hub.
func TestEnrolWorktree_RealHub_ProvenBoard_EnrolsAndTheFirstWriteKeepsEveryNode_PG(t *testing.T) {
	ctx := context.Background()
	f := newEnrolPGFixture(t)
	f.writeBoardFromWorktree(t)

	_, err := f.enrol()
	require.NoError(t, err)
	require.Len(t, f.enrolled, 1)

	clone := f.open(t)
	mtixDir := filepath.Join(f.root, ".mtix")
	syncSvc := service.NewSyncService(clone, slog.Default(), time.Now)
	require.NoError(t, syncSvc.AutoImport(ctx, mtixDir))
	require.NoError(t, clone.CreateNode(ctx, mkPGNode("TEST-3", "", 0, 3, "first write after enrol")))
	require.NoError(t, syncSvc.AutoExport(ctx, mtixDir))
	assert.ElementsMatch(t, []string{"TEST-1", "TEST-2", "TEST-3"}, nodeIDsInBoard(t, f.root), "the first write keeps every node")
	pending := pendingEventIDs(t, clone)
	require.NotEmpty(t, pending)
	var pushErr bytes.Buffer
	_, err = pushLoop(ctx, &pushErr, f.pool, clone)
	require.NoError(t, err, pushErr.String())
	onHub, err := f.pool.FetchEventsByID(ctx, pending)
	require.NoError(t, err)
	assert.Len(t, onHub, len(pending), "the first write reached the hub")
}

// TestEnrolWorktree_RealHub_FileDiffers_RefusedAndThePrintedFixWorks_PG: a
// tasks.json holding a node the store lacks refuses against the real hub with
// nothing moved; the printed fix (mtix sync --fix) is run for real and the
// enrolment then succeeds.
func TestEnrolWorktree_RealHub_FileDiffers_RefusedAndThePrintedFixWorks_PG(t *testing.T) {
	f := newEnrolPGFixture(t)
	boardFileHoldingNodes(t, filepath.Join(f.root, ".mtix"), map[string]string{"TEST-7": "only in tasks.json"})
	board, storeBefore := fileBytes(t, boardFilePath(f.root)), treeBytes(t, f.data)

	_, err := f.enrol()

	var refusal *service.WorktreeBoardError
	require.ErrorAs(t, err, &refusal)
	assert.Equal(t, service.BoardFileDiffers, refusal.Reason)
	assert.Contains(t, err.Error(), "TEST-7")
	assert.Empty(t, f.enrolled)
	assert.Equal(t, storeBefore, treeBytes(t, f.data), "the store is byte-identical")
	assert.Equal(t, board, fileBytes(t, boardFilePath(f.root)))

	runMtixSyncFix(t, f.root)
	_, err = f.enrol()

	require.NoError(t, err)
	assert.Len(t, f.enrolled, 1)
}

// TestEnrolWorktree_RealHub_StoreOnlyNode_Refused_PG: a node the store holds
// with no journal row (what an import writes) is on no hub. The enrolment
// refuses against the real hub, naming it, and moves nothing.
func TestEnrolWorktree_RealHub_StoreOnlyNode_Refused_PG(t *testing.T) {
	ctx := context.Background()
	f := newEnrolPGFixture(t)
	st := f.open(t)
	require.NoError(t, st.CreateNode(ctx, mkPGNode("TEST-9", "", 0, 9, "imported, never journalled")))
	_, err := st.WriteDB().ExecContext(ctx, `DELETE FROM sync_events WHERE node_id = 'TEST-9'`)
	require.NoError(t, err)
	require.NoError(t, st.Close())
	f.writeBoardFromWorktree(t)
	storeBefore := treeBytes(t, f.data)

	_, err = f.enrol()

	var refusal *service.WorktreeBoardError
	require.ErrorAs(t, err, &refusal)
	assert.Equal(t, service.BoardHubDiffers, refusal.Reason)
	assert.Equal(t, []string{"TEST-9"}, refusal.IDs())
	assert.Empty(t, f.enrolled)
	assert.Equal(t, storeBefore, treeBytes(t, f.data), "the store is byte-identical")
	matches, _ := filepath.Glob(filepath.Join(f.root, ".mtix", "data.pre-enrol-*"))
	assert.Empty(t, matches)
}

// localWorkPushed gives the fixture's worktree store work it authored itself
// (comment, claim, completion, dependency, edit, cancel; and, when hard is set,
// a defer wake time, a cascade delete and an undelete) and pushes it to the real
// hub, so the board is fully pushed.
func (f *enrolPGFixture) localWorkPushed(t *testing.T, hard bool) {
	t.Helper()
	ctx := context.Background()
	st := f.open(t)
	now := time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC)
	require.NoError(t, st.CreateNode(ctx, mkPGNode("TEST-11", "", 0, 11, "local parent")))
	require.NoError(t, st.CreateNode(ctx, mkPGNode("TEST-11.1", "TEST-11", 1, 1, "local child")))
	require.NoError(t, st.CreateNode(ctx, mkPGNode("TEST-12", "", 0, 12, "local work")))
	require.NoError(t, st.AppendAnnotation(ctx, "TEST-12", model.Annotation{ID: "local-comment", Author: "lane", Text: "a comment", CreatedAt: now}))
	require.NoError(t, st.AppendAnnotation(ctx, "TEST-12", model.Annotation{ID: "addressed-comment", Author: "lane", Text: "for you", CreatedAt: now.Add(time.Second), Addressee: "lane-b"}))
	require.NoError(t, st.ClaimNode(ctx, "TEST-12", "lane-a"))
	require.NoError(t, st.TransitionStatus(ctx, "TEST-12", model.StatusInProgress, "start", "lane-a"))
	require.NoError(t, st.TransitionStatus(ctx, "TEST-12", model.StatusDone, "finished", "lane-a"))
	require.NoError(t, st.AddDependency(ctx, &model.Dependency{FromID: "TEST-12", ToID: "TEST-11", DepType: model.DepTypeBlocks, CreatedAt: now}))
	// MTIX-112.17.16.12: a cascade delete and a cascade cancel emit one event
	// per descendant, so a clone of the hub holds the same descendants.
	require.NoError(t, st.CreateNode(ctx, mkPGNode("TEST-14", "", 0, 14, "cascade deleted")))
	require.NoError(t, st.CreateNode(ctx, mkPGNode("TEST-14.1", "TEST-14", 1, 1, "cascade deleted child")))
	require.NoError(t, st.CreateNode(ctx, mkPGNode("TEST-15", "", 0, 15, "cascade cancelled")))
	require.NoError(t, st.CreateNode(ctx, mkPGNode("TEST-15.1", "TEST-15", 1, 1, "cascade cancelled child")))
	require.NoError(t, st.DeleteNode(ctx, "TEST-14", true, "lane"))
	require.NoError(t, st.CancelNode(ctx, "TEST-15", "dropped", "lane", true))
	if hard {
		until := now.Add(48 * time.Hour)
		require.NoError(t, st.CreateNode(ctx, mkPGNode("TEST-13", "", 0, 13, "deferred")))
		require.NoError(t, st.DeferNode(ctx, "TEST-13", &until, "later", "lane"))
	}
	var pushErr bytes.Buffer
	_, err := pushLoop(ctx, &pushErr, f.pool, st)
	require.NoError(t, err, pushErr.String())
	require.NoError(t, st.Close())
	f.writeBoardFromWorktree(t)
}

// TestEnrolWorktree_RealHub_FullyPushedLocalWork_Enrols_PG: comments, a claim,
// completion, a dependency and nested tasks authored locally and pushed to the
// real hub enrol; no work is lost.
func TestEnrolWorktree_RealHub_FullyPushedLocalWork_Enrols_PG(t *testing.T) {
	f := newEnrolPGFixture(t)
	f.localWorkPushed(t, false)

	_, err := f.enrol()

	require.NoError(t, err)
	require.Len(t, f.enrolled, 1)
	clone := f.open(t)
	for _, id := range []string{"TEST-1", "TEST-2", "TEST-11", "TEST-11.1", "TEST-12"} {
		_, getErr := clone.GetNode(context.Background(), id)
		require.NoError(t, getErr, id)
	}
	for _, id := range []string{"TEST-14", "TEST-14.1"} {
		_, getErr := clone.GetNode(context.Background(), id)
		require.ErrorIs(t, getErr, model.ErrNotFound, "%s: the cascade delete reached the hub", id)
	}
	for _, id := range []string{"TEST-15", "TEST-15.1"} {
		got, getErr := clone.GetNode(context.Background(), id)
		require.NoError(t, getErr, id)
		assert.Equal(t, model.StatusCancelled, got.Status, "%s: the cascade cancel reached the hub", id)
	}
}

// TestEnrolWorktree_RealHub_BehindBoard_Enrols_PG: a teammate changed a node on
// the real hub after this worktree pulled; the board is behind, and enrols with
// the hub's version.
func TestEnrolWorktree_RealHub_BehindBoard_Enrols_PG(t *testing.T) {
	ctx := context.Background()
	f := newEnrolPGFixture(t)
	f.writeBoardFromWorktree(t)
	peer, err := sqlite.New(filepath.Join(t.TempDir(), "peer"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = peer.Close() })
	_, _, err = pullLoop(ctx, testIngest(nil), f.pool, peer, transport.PullCursor{}, 100)
	require.NoError(t, err)
	desc := "teammate edit"
	require.NoError(t, peer.UpdateNode(ctx, "TEST-1", &store.NodeUpdate{Description: &desc}))
	require.NoError(t, peer.DeleteNode(ctx, "TEST-2", false, "teammate"))
	var pushErr bytes.Buffer
	_, err = pushLoop(ctx, &pushErr, f.pool, peer)
	require.NoError(t, err, pushErr.String())

	_, err = f.enrol()

	var refusal *service.WorktreeBoardError
	if errors.As(err, &refusal) {
		t.Logf("DIFF %v", refusal.Diff)
	}
	require.NoError(t, err)
	clone := f.open(t)
	kept, err := clone.GetNode(ctx, "TEST-1")
	require.NoError(t, err)
	assert.Equal(t, "teammate edit", kept.Description)
	_, err = clone.GetNode(ctx, "TEST-2")
	require.ErrorIs(t, err, model.ErrNotFound, "the hub's deletion is kept")
}

// TestEnrolWorktree_RealHub_NotReplicatedWork_RefusesThenAcceptHub_PG: a defer
// wake time does not reach a clone of the hub, so the board refuses with the
// field named; accept-hub enrols and keeps the old store. A cascade delete and
// a cascade cancel in the same board do reach the hub (MTIX-112.17.16.12), so
// they are not named.
func TestEnrolWorktree_RealHub_NotReplicatedWork_RefusesThenAcceptHub_PG(t *testing.T) {
	f := newEnrolPGFixture(t)
	f.localWorkPushed(t, true)
	storeBefore := treeBytes(t, f.data)

	_, err := f.enrol()

	var refusal *service.WorktreeBoardError
	require.ErrorAs(t, err, &refusal)
	assert.Contains(t, refusal.Diff["TEST-13"], "defer_until")
	assert.Equal(t, []string{"TEST-13"}, refusal.IDs(), "the cascade delete and cancel replicate")
	assert.Empty(t, f.enrolled)
	assert.Equal(t, storeBefore, treeBytes(t, f.data), "the store is byte-identical")

	res, err := f.service().EnrolWorktree(context.Background(), service.EnrollmentRequest{Path: f.root, Mode: "yes", Source: "test", AcceptHub: refusal.Digest()})
	require.NoError(t, err)
	assert.DirExists(t, res.AsideDir)
	assert.NotEmpty(t, res.HubDiffReport)
}
