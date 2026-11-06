// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"log/slog"
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

// Real-Postgres proofs that an enrolment never passes silently when the hub
// moved on an entity this store's own journal touched (MTIX-112.17.16.18,
// ruling r2 point 1), and that the row advice never leads to a duplicate
// (point 2). The store under test is the worktree store of newEnrolPGFixture;
// the teammate is a peer store pushing to the same hub.

// teammate pulls the hub into a peer store, runs edit on it and pushes.
func (f *enrolPGFixture) teammate(t *testing.T, edit func(ctx context.Context, peer *sqlite.Store)) {
	t.Helper()
	ctx := context.Background()
	peer, err := sqlite.New(filepath.Join(t.TempDir(), "peer"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = peer.Close() })
	_, _, err = pullLoop(ctx, testIngest(nil), f.pool, peer, transport.PullCursor{}, 100)
	require.NoError(t, err)
	edit(ctx, peer)
	var pushErr bytes.Buffer
	_, err = pushLoop(ctx, &pushErr, f.pool, peer)
	require.NoError(t, err, pushErr.String())
}

// ownWork runs edit on the worktree store, pushes it and refreshes tasks.json.
func (f *enrolPGFixture) ownWork(t *testing.T, edit func(ctx context.Context, st *sqlite.Store)) {
	t.Helper()
	ctx := context.Background()
	st := f.open(t)
	edit(ctx, st)
	var pushErr bytes.Buffer
	_, err := pushLoop(ctx, &pushErr, f.pool, st)
	require.NoError(t, err, pushErr.String())
	require.NoError(t, st.Close())
	f.writeBoardFromWorktree(t)
}

func str(s string) *string { return &s }

// TestEnrolWorktree_RealHub_CreatedNodeEditedByTeammate_NamesTheFields: a node
// this store created and pushed, then retitled and re-described by a teammate.
func TestEnrolWorktree_RealHub_CreatedNodeEditedByTeammate_NamesTheFields(t *testing.T) {
	var notice bytes.Buffer
	f := newEnrolPGFixture(t)
	f.notice = &notice
	f.ownWork(t, func(ctx context.Context, st *sqlite.Store) {
		n := mkPGNode("TEST-3", "", 0, 3, "own title")
		n.Description = "own description"
		n.ContentHash = n.ComputeHash()
		require.NoError(t, st.CreateNode(ctx, n))
	})
	f.teammate(t, func(ctx context.Context, peer *sqlite.Store) {
		for _, title := range []string{"t1", "t2", "t3"} {
			require.NoError(t, peer.UpdateNode(ctx, "TEST-3", &store.NodeUpdate{Title: str(title), Description: str("d " + title)}))
		}
	})

	res, err := f.enrol()

	require.NoError(t, err)
	require.NotNil(t, res.LostLocal, "a create the hub moved on from must be named, never silent")
	assert.Equal(t, []string{"content_hash", "description", "title"}, res.LostLocal.Diff["TEST-3"], "the hash is derived from the edited fields")
	assert.Contains(t, notice.String(), "TEST-3")
	assert.FileExists(t, res.LostLocalReport)
}

// TestEnrolWorktree_RealHub_LinkRemovedByTeammate_NamesTheLink: a dependency
// this store added and pushed and a teammate removed.
func TestEnrolWorktree_RealHub_LinkRemovedByTeammate_NamesTheLink(t *testing.T) {
	f := newEnrolPGFixture(t)
	f.ownWork(t, func(ctx context.Context, st *sqlite.Store) {
		require.NoError(t, st.AddDependency(ctx, &model.Dependency{FromID: "TEST-1", ToID: "TEST-2", DepType: model.DepTypeBlocks, CreatedAt: time.Now().UTC()}))
	})
	f.teammate(t, func(ctx context.Context, peer *sqlite.Store) {
		require.NoError(t, peer.RemoveDependency(ctx, "TEST-1", "TEST-2", model.DepTypeBlocks))
	})

	res, err := f.enrol()

	require.NoError(t, err)
	require.NotNil(t, res.LostLocal)
	assert.Equal(t, []string{"(dependencies)"}, res.LostLocal.Diff["TEST-1"])
}

// TestEnrolWorktree_RealHub_LinkRestoredByTeammate_NamesTheLink: this store
// removed a dependency and a teammate linked it again.
func TestEnrolWorktree_RealHub_LinkRestoredByTeammate_NamesTheLink(t *testing.T) {
	f := newEnrolPGFixture(t)
	dep := &model.Dependency{FromID: "TEST-1", ToID: "TEST-2", DepType: model.DepTypeBlocks, CreatedAt: time.Now().UTC()}
	f.ownWork(t, func(ctx context.Context, st *sqlite.Store) { require.NoError(t, st.AddDependency(ctx, dep)) })
	f.ownWork(t, func(ctx context.Context, st *sqlite.Store) {
		require.NoError(t, st.RemoveDependency(ctx, "TEST-1", "TEST-2", model.DepTypeBlocks))
	})
	f.teammate(t, func(ctx context.Context, peer *sqlite.Store) {
		// The peer pulled the removal; linking again is the hub's later write.
		require.NoError(t, peer.AddDependency(ctx, dep))
	})

	res, err := f.enrol()

	require.NoError(t, err)
	require.NotNil(t, res.LostLocal)
	assert.Equal(t, []string{"(dependencies)"}, res.LostLocal.Diff["TEST-1"])
}

// TestEnrolWorktree_RealHub_NodeDeletedByTeammate_NamesTheDeletion: this store
// retitled a node and pushed; a teammate deleted it.
func TestEnrolWorktree_RealHub_NodeDeletedByTeammate_NamesTheDeletion(t *testing.T) {
	f := newEnrolPGFixture(t)
	f.ownWork(t, func(ctx context.Context, st *sqlite.Store) {
		require.NoError(t, st.UpdateNode(ctx, "TEST-1", &store.NodeUpdate{Title: str("own title")}))
	})
	f.teammate(t, func(ctx context.Context, peer *sqlite.Store) {
		require.NoError(t, peer.DeleteNode(ctx, "TEST-1", false, "mate"))
	})

	res, err := f.enrol()

	require.NoError(t, err)
	require.NotNil(t, res.LostLocal)
	assert.Contains(t, res.LostLocal.Diff["TEST-1"], "deleted_at")
}

// TestEnrolWorktree_RealHub_BothSidesComment_NothingLost: the clone holds this
// store's comment and the teammate's, so there is nothing to re-enter, and a
// notice that said so would double the comment on the hub (review F3).
func TestEnrolWorktree_RealHub_BothSidesComment_NothingLost(t *testing.T) {
	var notice bytes.Buffer
	f := newEnrolPGFixture(t)
	f.notice = &notice
	now := time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC)
	f.ownWork(t, func(ctx context.Context, st *sqlite.Store) {
		require.NoError(t, st.CreateNode(ctx, mkPGNode("TEST-3", "", 0, 3, "own node")))
		require.NoError(t, st.AppendAnnotation(ctx, "TEST-3", model.Annotation{ID: "own", Author: "lane", Text: "mine", CreatedAt: now}))
	})
	f.teammate(t, func(ctx context.Context, peer *sqlite.Store) {
		require.NoError(t, peer.AppendAnnotation(ctx, "TEST-3", model.Annotation{ID: "mate", Author: "mate", Text: "theirs", CreatedAt: now.Add(time.Second)}))
	})

	res, err := f.enrol()

	require.NoError(t, err)
	assert.Nil(t, res.LostLocal)
	assert.Empty(t, notice.String())
}

// TestEnrolWorktree_RealHub_PushedLabelsEdit_Enrols: a labels edit that was
// pushed is on the hub; the clone writes the list with other spacing and that
// must not refuse (review F4).
func TestEnrolWorktree_RealHub_PushedLabelsEdit_Enrols(t *testing.T) {
	f := newEnrolPGFixture(t)
	f.ownWork(t, func(ctx context.Context, st *sqlite.Store) {
		require.NoError(t, st.UpdateNode(ctx, "TEST-1", &store.NodeUpdate{Labels: []string{"x", "y"}}))
	})

	res, err := f.enrol()

	require.NoError(t, err)
	assert.Nil(t, res.LostLocal)
}

// storeOnlyUnjournaled gives the worktree store a node (or, with dep set, a
// dependency) that no journal row explains, as an import merge writes it.
func (f *enrolPGFixture) storeOnlyUnjournaled(t *testing.T, write func(ctx context.Context, st *sqlite.Store), nodeID string) {
	t.Helper()
	ctx := context.Background()
	st := f.open(t)
	write(ctx, st)
	_, err := st.WriteDB().ExecContext(ctx, `DELETE FROM sync_events WHERE node_id = ? AND sync_status != 'applied'`, nodeID)
	require.NoError(t, err)
	require.NoError(t, st.Close())
	f.writeBoardFromWorktree(t)
}

func (f *enrolPGFixture) countTitled(t *testing.T, title string) int {
	t.Helper()
	var n int
	require.NoError(t, f.open(t).WriteDB().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM nodes WHERE title = ? AND deleted_at IS NULL`, title).Scan(&n))
	return n
}

// refusalOf enrols and returns the board refusal.
func (f *enrolPGFixture) refusalOf(t *testing.T) *service.WorktreeBoardError {
	t.Helper()
	_, err := f.enrol()
	var refusal *service.WorktreeBoardError
	require.ErrorAs(t, err, &refusal)
	return refusal
}

// acceptFinding enrols again naming the refusal's digest.
func (f *enrolPGFixture) acceptFinding(t *testing.T, r *service.WorktreeBoardError) {
	t.Helper()
	_, err := f.service().EnrolWorktree(context.Background(), service.EnrollmentRequest{Path: f.root, Mode: "yes", Source: "test", AcceptHub: r.Digest()})
	require.NoError(t, err)
}

// TestEnrolWorktree_RealHub_RowTheHubHolds_AdviceNeverLeadsToADuplicate: the
// store holds a node with no journal row and the hub holds it too (above the
// store's pull cursor). The text must not tell the operator to create it again;
// following it (accepting) leaves one node (review F2).
func TestEnrolWorktree_RealHub_RowTheHubHolds_AdviceNeverLeadsToADuplicate(t *testing.T) {
	f := newEnrolPGFixture(t)
	f.teammate(t, func(ctx context.Context, peer *sqlite.Store) {
		require.NoError(t, peer.CreateNode(ctx, mkPGNode("TEST-30", "", 0, 30, "held by the hub")))
	})
	f.storeOnlyUnjournaled(t, func(ctx context.Context, st *sqlite.Store) {
		require.NoError(t, st.CreateNode(ctx, mkPGNode("TEST-30", "", 0, 30, "held by the hub")))
	}, "TEST-30")

	refusal := f.refusalOf(t)

	msg := refusal.Error()
	assert.NotContains(t, msg, "create it again")
	assert.NotContains(t, msg, "mtix create")
	assert.Contains(t, msg, "the hub already holds TEST-30")
	f.acceptFinding(t, refusal)
	assert.Equal(t, 1, f.countTitled(t, "held by the hub"))
}

// TestEnrolWorktree_RealHub_RowTheHubLacks_PrintedCreateLeavesOneNode: the node
// is on no hub; the printed create command, run as printed, then a push and an
// accept leave exactly one node with that title on the clone.
func TestEnrolWorktree_RealHub_RowTheHubLacks_PrintedCreateLeavesOneNode(t *testing.T) {
	f := newEnrolPGFixture(t)
	f.storeOnlyUnjournaled(t, func(ctx context.Context, st *sqlite.Store) {
		require.NoError(t, st.CreateNode(ctx, mkPGNode("TEST-9", "", 0, 9, "on no hub")))
	}, "TEST-9")

	refusal := f.refusalOf(t)

	msg := refusal.Error()
	assert.Contains(t, msg, "the hub lacks TEST-9")
	assert.Contains(t, msg, "mtix create")
	out, err := runShellWithMtix(t, f, "mtix create 'on no hub'")
	require.NoError(t, err, out)
	out, err = runShellWithMtix(t, f, "mtix sync push --insecure-tls")
	require.NoError(t, err, out)
	f.writeBoardFromWorktree(t)
	second := f.refusalOf(t)
	f.acceptFinding(t, second)
	assert.Equal(t, 1, f.countTitled(t, "on no hub"), "one node, not a duplicate")
}

// TestEnrolWorktree_RealHub_DependencyRows_AdviceNamesDependenciesNotTheNode:
// three store-only dependency rows from a node the hub holds. The text must say
// dependency rows, name the dependency command, and never say the hub lacks the
// node (review F2, second case).
func TestEnrolWorktree_RealHub_DependencyRows_AdviceNamesDependenciesNotTheNode(t *testing.T) {
	f := newEnrolPGFixture(t)
	f.storeOnlyUnjournaled(t, func(ctx context.Context, st *sqlite.Store) {
		require.NoError(t, st.AddDependency(ctx, &model.Dependency{FromID: "TEST-1", ToID: "TEST-2", DepType: model.DepTypeBlocks, CreatedAt: time.Now().UTC()}))
	}, "TEST-1")

	refusal := f.refusalOf(t)

	msg := refusal.Error()
	assert.Contains(t, msg, "dependency row(s) from TEST-1")
	assert.Contains(t, msg, "mtix dep add")
	assert.NotContains(t, msg, "the hub lacks TEST-1")
	assert.NotContains(t, msg, "create it again")
}

// TestEnrolWorktree_RealHub_DependencyTheHubHolds_NoReAddAdvice: the hub holds
// the dependency above the store's cursor; re-adding is not advised.
func TestEnrolWorktree_RealHub_DependencyTheHubHolds_NoReAddAdvice(t *testing.T) {
	f := newEnrolPGFixture(t)
	dep := &model.Dependency{FromID: "TEST-1", ToID: "TEST-2", DepType: model.DepTypeBlocks, CreatedAt: time.Now().UTC()}
	f.teammate(t, func(ctx context.Context, peer *sqlite.Store) { require.NoError(t, peer.AddDependency(ctx, dep)) })
	f.storeOnlyUnjournaled(t, func(ctx context.Context, st *sqlite.Store) { require.NoError(t, st.AddDependency(ctx, dep)) }, "TEST-1")

	refusal := f.refusalOf(t)

	msg := refusal.Error()
	assert.NotContains(t, msg, "mtix dep add")
	assert.Contains(t, msg, "the hub already holds")
}
