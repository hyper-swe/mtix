// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
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
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// pulledHub is a fake hub that answers id lookups from the events it serves.
type pulledHub struct{ *fakeLateHub }

func (h pulledHub) FetchEventsByID(_ context.Context, ids []string) ([]*model.SyncEvent, error) {
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	var out []*model.SyncEvent
	for _, e := range h.pullEvents {
		if want[e.EventID] {
			out = append(out, e)
		}
	}
	return out, nil
}

// fakeDial dials the same fake hub every time.
func fakeDial(h enrolHub) hubDialer {
	return func(context.Context, string) (enrolHub, func(), error) { return h, func() {}, nil }
}

// enrolOverHub builds the production ports with the hub replaced by fake: every
// port is the shipped one, only the connection is a fake.
func enrolOverHub(dial hubDialer) (*service.WorktreeEnrolment, *bool) {
	recorded := new(bool)
	ports := newWorktreeEnrolPortsWith(slog.Default(), io.Discard, dial,
		func(context.Context, service.EnrollmentRequest) error { *recorded = true; return nil },
		func(context.Context, string) (bool, error) { return *recorded, nil })
	return service.NewWorktreeEnrolment(service.WorktreeEnrolOptions{Ports: ports,
		Clock: func() time.Time { return time.Date(2026, 11, 5, 1, 2, 3, 0, time.UTC) }}), recorded
}

func enrolRequest(root string) service.EnrollmentRequest {
	return service.EnrollmentRequest{Path: root, Mode: "yes", Source: "test"}
}

func fileBytes(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // G304: a test file
	require.NoError(t, err)
	return b
}

// writeBoardFromStore writes <root>/.mtix/tasks.json as the store's own export.
func writeBoardFromStore(t *testing.T, root string, st *sqlite.Store) {
	t.Helper()
	require.NoError(t, service.NewSyncService(st, slog.Default(), time.Now).AutoExport(context.Background(), filepath.Join(root, ".mtix")))
	require.FileExists(t, boardFilePath(root))
}

// hubEventsAfter returns the events a second replica adds to the hub after it
// pulled base: what a teammate's change looks like to the enrolling worktree.
func hubEventsAfter(t *testing.T, base []*model.SyncEvent, change func(ctx context.Context, st *sqlite.Store)) []*model.SyncEvent {
	t.Helper()
	ctx := context.Background()
	st, err := sqlite.New(filepath.Join(t.TempDir(), "peer"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	_, _, err = pullLoop(ctx, testIngest(nil), &fakeLateHub{pullEvents: base}, st, transport.PullCursor{}, 100)
	require.NoError(t, err)
	change(ctx, st)
	added, err := readPendingBatch(ctx, st, 100)
	require.NoError(t, err)
	require.NotEmpty(t, added)
	for i, e := range added {
		e.CreatedAt = offlineEventsPushedAt.Add(time.Duration(10+i) * time.Second)
	}
	return added
}

func nodeIDsInBoard(t *testing.T, root string) []string {
	t.Helper()
	var exported struct {
		Nodes []struct {
			ID string `json:"id"`
		} `json:"nodes"`
	}
	require.NoError(t, json.Unmarshal(fileBytes(t, boardFilePath(root)), &exported))
	var ids []string
	for _, n := range exported.Nodes {
		ids = append(ids, n.ID)
	}
	return ids
}

// assertNothingChanged proves a refused enrolment left no trace.
func assertNothingChanged(t *testing.T, root string, storeBefore map[string]string, board []byte, recorded *bool) {
	t.Helper()
	mtixDir := filepath.Join(root, ".mtix")
	assert.False(t, *recorded)
	assert.Equal(t, storeBefore, treeBytes(t, worktreeDataDir(root)), "the store is byte-identical")
	if board != nil {
		assert.Equal(t, board, fileBytes(t, boardFilePath(root)), "tasks.json is byte-identical")
	}
	_, statErr := os.Lstat(filepath.Join(mtixDir, "enroll.d", "in-progress.json"))
	assert.ErrorIs(t, statErr, os.ErrNotExist, "no marker was written")
	for _, glob := range []string{"preflight-*", "../data.pre-enrol-*", "../backups/*"} {
		left, err := filepath.Glob(filepath.Join(mtixDir, "enroll.d", glob))
		require.NoError(t, err)
		assert.Empty(t, left, glob)
	}
}

// runMtixSyncFix runs `mtix sync --fix` in the worktree the way the printed
// refusal tells the operator to: the real initApp for that directory, then the
// real command body.
func runMtixSyncFix(t *testing.T, root string) { runMtixSyncFixWithConfig(t, root, "") }

// runMtixSyncFixWithConfig is runMtixSyncFix over a config.yaml with extra lines.
func runMtixSyncFixWithConfig(t *testing.T, root, extraConfig string) {
	t.Helper()
	identityOperatorState(t)
	saveAndResetApp(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, ".mtix", "config.yaml"), []byte("prefix: TEST\nmax_depth: 10\nagent_stale_threshold: 30m\n"+extraConfig), 0o600))
	old, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Chdir(old) })
	require.NoError(t, os.Chdir(root))
	require.NoError(t, initApp(&cobra.Command{Use: "sync"}, ""))
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	require.NoError(t, runSync(cmd, true))
	require.NoError(t, app.store.Close())
	app.store = nil
}

// TestEnrolWorktree_RealPath_FileDiffersFromTheStore_RefusesAndTheFixWorks drives
// the shipped ports over a fake hub. tasks.json holds a node the store lacks:
// the enrolment refuses with the nodes named and leaves the store, the file and
// the host untouched. Then the printed fix is run for real (mtix sync --fix),
// the enrolment runs again and succeeds, and the first write after it still
// has every node.
func TestEnrolWorktree_RealPath_FileDiffersFromTheStore_RefusesAndTheFixWorks(t *testing.T) {
	ctx := context.Background()
	root, writer, hubEvents := writerWorktreeOnHub(t)
	require.NoError(t, writer.Close())
	boardFileHoldingNodes(t, filepath.Join(root, ".mtix"), map[string]string{"TEST-7": "only in tasks.json"})
	hub := hubOf(append(hubEvents, offlineEvents(t)...))
	svc, recorded := enrolOverHub(fakeDial(hub))
	board, storeBefore := fileBytes(t, boardFilePath(root)), treeBytes(t, worktreeDataDir(root))

	_, err := svc.EnrolWorktree(ctx, enrolRequest(root))

	var refusal *service.WorktreeBoardError
	require.ErrorAs(t, err, &refusal)
	require.ErrorIs(t, err, model.ErrConflict)
	assert.Equal(t, service.BoardFileDiffers, refusal.Reason)
	assert.Contains(t, err.Error(), "TEST-7")
	assert.Contains(t, err.Error(), "mtix sync --fix")
	assertNothingChanged(t, root, storeBefore, board, recorded)

	runMtixSyncFix(t, root) // the printed fix
	_, err = svc.EnrolWorktree(ctx, enrolRequest(root))
	require.NoError(t, err)
	assert.True(t, *recorded)

	clone, err := sqlite.New(worktreeDataDir(root), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = clone.Close() })
	syncSvc := service.NewSyncService(clone, slog.Default(), time.Now)
	require.NoError(t, syncSvc.AutoImport(ctx, filepath.Join(root, ".mtix")))
	require.NoError(t, clone.CreateNode(ctx, mkPGNode("TEST-8", "", 0, 8, "first write after enrol")))
	require.NoError(t, syncSvc.AutoExport(ctx, filepath.Join(root, ".mtix")))
	assert.ElementsMatch(t, []string{"TEST-1", "TEST-8", "TEST-9"}, nodeIDsInBoard(t, root), "the first write keeps every node")
}

// TestEnrolWorktree_RealPath_LocalChangeTheHubLacks_Refused: the store holds a
// change nothing journalled (what an import writes), for each kind of field an
// agent can change. The file is the store's own export, so only the node proof
// can see it. Nothing moves.
func TestEnrolWorktree_RealPath_LocalChangeTheHubLacks_Refused(t *testing.T) {
	changes := map[string]string{
		"status":     `UPDATE nodes SET status = 'done' WHERE id = 'TEST-1'`,
		"priority":   `UPDATE nodes SET priority = 1 WHERE id = 'TEST-1'`,
		"assignee":   `UPDATE nodes SET assignee = 'someone' WHERE id = 'TEST-1'`,
		"annotation": `UPDATE nodes SET annotations = '[{"id":"a","author":"x","text":"t","created_at":"2026-06-01T00:00:00Z"}]' WHERE id = 'TEST-1'`,
		"title":      `UPDATE nodes SET title = 'edited' WHERE id = 'TEST-1'`,
		"dependency": `INSERT INTO nodes (id, depth, seq, project, title, created_at, updated_at, uid) VALUES ('TEST-5', 0, 5, 'TEST', 't', 'x', 'x', 'u5');` +
			`INSERT INTO dependencies (from_id, to_id, dep_type, created_at) VALUES ('TEST-1', 'TEST-5', 'blocks', 'x')`,
		"deletion":   `UPDATE nodes SET deleted_at = '2026-07-01T00:00:00Z' WHERE id = 'TEST-1'`,
		"a new node": `INSERT INTO nodes (id, depth, seq, project, title, created_at, updated_at, uid) VALUES ('TEST-6', 0, 6, 'TEST', 'new', 'x', 'x', 'u6')`,
	}
	for name, stmt := range changes {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			root, writer, hubEvents := writerWorktreeOnHub(t)
			_, err := writer.WriteDB().ExecContext(ctx, stmt)
			require.NoError(t, err)
			writeBoardFromStore(t, root, writer)
			require.NoError(t, writer.Close())
			svc, recorded := enrolOverHub(fakeDial(hubOf(hubEvents)))
			board, storeBefore := fileBytes(t, boardFilePath(root)), treeBytes(t, worktreeDataDir(root))

			_, err = svc.EnrolWorktree(ctx, enrolRequest(root))

			var refusal *service.WorktreeBoardError
			require.ErrorAs(t, err, &refusal)
			assert.Equal(t, service.BoardHubDiffers, refusal.Reason)
			assert.Contains(t, err.Error(), "accept-hub")
			assertNothingChanged(t, root, storeBefore, board, recorded)
		})
	}
}

// TestEnrolWorktree_RealPath_AnotherTaskUnderTheSameID_Refused: the hub holds a
// different task (another uid) under the id the store uses. The local task is
// not on the hub, whatever the ids say.
func TestEnrolWorktree_RealPath_AnotherTaskUnderTheSameID_Refused(t *testing.T) {
	ctx := context.Background()
	root, writer, _ := writerWorktreeOnHub(t)
	require.NoError(t, writer.Close())
	hub := hubOf(hubEventsHolding(t, map[string]string{"TEST-1": "someone else's task"}))
	svc, recorded := enrolOverHub(fakeDial(hub))
	storeBefore := treeBytes(t, worktreeDataDir(root))

	_, err := svc.EnrolWorktree(ctx, enrolRequest(root))

	// The journal's event is not on this hub either, so the event check refuses first;
	// the node proof is pinned by the unit table (same id, other uid).
	var events *service.WorktreeLocalEventsError
	require.ErrorAs(t, err, &events)
	assertNothingChanged(t, root, storeBefore, nil, recorded)
}

// TestEnrolWorktree_RealPath_HubAhead_EnrolsAndTheCloneStaysTheHubs: the file is
// the store's own export and the hub has moved on (a teammate edited the node,
// another deleted it): the hub copy is authoritative, so the enrolment succeeds
// and no later command imports the older file over the clone.
func TestEnrolWorktree_RealPath_HubAhead_EnrolsAndTheCloneStaysTheHubs(t *testing.T) {
	cases := []struct {
		name   string
		change func(ctx context.Context, st *sqlite.Store)
		check  func(t *testing.T, clone *sqlite.Store)
	}{
		{"the hub edited the node", func(ctx context.Context, st *sqlite.Store) {
			desc := "hub edit"
			require.NoError(t, st.UpdateNode(ctx, "TEST-1", &store.NodeUpdate{Description: &desc}))
		}, func(t *testing.T, clone *sqlite.Store) {
			kept, err := clone.GetNode(context.Background(), "TEST-1")
			require.NoError(t, err)
			assert.Equal(t, "hub edit", kept.Description, "the file's older copy is not imported over the hub's")
		}},
		{"the hub deleted the node", func(ctx context.Context, st *sqlite.Store) {
			require.NoError(t, st.DeleteNode(ctx, "TEST-1", false, "teammate"))
		}, func(t *testing.T, clone *sqlite.Store) {
			_, err := clone.GetNode(context.Background(), "TEST-1")
			require.ErrorIs(t, err, model.ErrNotFound, "a deletion on the hub is not undone by the older file")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			root, writer, hubEvents := writerWorktreeOnHub(t)
			writeBoardFromStore(t, root, writer)
			require.NoError(t, writer.Close())
			hub := hubOf(append(append([]*model.SyncEvent(nil), hubEvents...), hubEventsAfter(t, hubEvents, tc.change)...))
			svc, recorded := enrolOverHub(fakeDial(hub))

			_, err := svc.EnrolWorktree(ctx, enrolRequest(root))
			require.NoError(t, err)
			assert.True(t, *recorded)

			clone, err := sqlite.New(worktreeDataDir(root), slog.Default())
			require.NoError(t, err)
			t.Cleanup(func() { _ = clone.Close() })
			pendingBefore, err := clone.LocalEvents(ctx)
			require.NoError(t, err)
			require.NoError(t, service.NewSyncService(clone, slog.Default(), time.Now).AutoImport(ctx, filepath.Join(root, ".mtix")))
			tc.check(t, clone)
			pendingAfter, err := clone.LocalEvents(ctx)
			require.NoError(t, err)
			assert.Equal(t, pendingBefore, pendingAfter, "the first command wrote no event")
		})
	}
}

// changingDial serves the first hub for its first n dials and the second after:
// a hub that changes between the preflight and the clone.
func changingDial(first, second enrolHub, n int) hubDialer {
	calls := 0
	return func(context.Context, string) (enrolHub, func(), error) {
		calls++
		if calls <= n {
			return first, func() {}, nil
		}
		return second, func() {}, nil
	}
}

// TestEnrolWorktree_RealPath_HubChangesBetweenPreflightAndClone_VerifyRefuses
// pins the production VerifyBoard wiring: the hub that served the preflight held
// the node, the one that served the clone does not. The enrolment must refuse
// after the swap, restore the original store, and not record anything. With
// VerifyBoard wired to nothing the enrolment would succeed.
func TestEnrolWorktree_RealPath_HubChangesBetweenPreflightAndClone_VerifyRefuses(t *testing.T) {
	ctx := context.Background()
	root, writer, hubEvents := writerWorktreeOnHub(t)
	writeBoardFromStore(t, root, writer)
	require.NoError(t, writer.Close())
	holding := hubOf(hubEvents)
	lacking := hubOf(offlineEvents(t))
	svc, recorded := enrolOverHub(changingDial(holding, lacking, 2)) // dials: event lookup, preflight; then the clone
	board, storeBefore := fileBytes(t, boardFilePath(root)), treeBytes(t, worktreeDataDir(root))

	_, err := svc.EnrolWorktree(ctx, enrolRequest(root))

	var refusal *service.WorktreeBoardError
	require.ErrorAs(t, err, &refusal)
	assert.Contains(t, refusal.IDs(), "TEST-1")
	assert.Contains(t, err.Error(), "original store was restored")
	assert.False(t, *recorded)
	assert.Equal(t, storeBefore, treeBytes(t, worktreeDataDir(root)), "the original store is back, byte for byte")
	assert.Equal(t, board, fileBytes(t, boardFilePath(root)))
}

// TestEnrolWorktree_RealPath_BoardFileEditedAfterPreflight_VerifyRefuses: the
// file passed the preflight, then changed while the clone ran. The post-swap
// proof repeats the file rule, refuses, and the original store goes back.
func TestEnrolWorktree_RealPath_BoardFileEditedAfterPreflight_VerifyRefuses(t *testing.T) {
	ctx := context.Background()
	root, writer, hubEvents := writerWorktreeOnHub(t)
	writeBoardFromStore(t, root, writer)
	require.NoError(t, writer.Close())
	hub := hubOf(hubEvents)
	calls := 0
	dial := func(context.Context, string) (enrolHub, func(), error) {
		calls++
		if calls == 3 { // event lookup, preflight, then the clone: edit the file now
			boardFileHoldingNodes(t, filepath.Join(root, ".mtix"), map[string]string{"TEST-7": "edited meanwhile"})
		}
		return hub, func() {}, nil
	}
	svc, recorded := enrolOverHub(dial)
	storeBefore := treeBytes(t, worktreeDataDir(root))

	_, err := svc.EnrolWorktree(ctx, enrolRequest(root))

	var refusal *service.WorktreeBoardError
	require.ErrorAs(t, err, &refusal)
	assert.Equal(t, service.BoardFileDiffers, refusal.Reason)
	assert.Contains(t, err.Error(), "original store was restored")
	assert.False(t, *recorded)
	assert.Equal(t, storeBefore, treeBytes(t, worktreeDataDir(root)))
}

// TestEnrolWorktree_RealPath_PreflightFailures_RefuseBeforeAnythingMoves: a
// board file that cannot be parsed, a hub that cannot be read, and a clone that
// fails are errors, never a pass.
func TestEnrolWorktree_RealPath_PreflightFailures_RefuseBeforeAnythingMoves(t *testing.T) {
	cases := []struct {
		name  string
		board string
		hub   func(events []*model.SyncEvent) enrolHub
		want  string
	}{
		{"malformed board file", "{not json", func(e []*model.SyncEvent) enrolHub { return hubOf(e) }, "read the first board"},
		{"hub cannot be read", "", func(e []*model.SyncEvent) enrolHub {
			h := hubOf(e)
			h.pullErr = io.ErrUnexpectedEOF
			return h
		}, "copy the applied hub events into the scratch clone"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			root, writer, hubEvents := writerWorktreeOnHub(t)
			require.NoError(t, writer.Close())
			if tc.board != "" {
				require.NoError(t, os.WriteFile(boardFilePath(root), []byte(tc.board), 0o600))
			}
			svc, recorded := enrolOverHub(fakeDial(tc.hub(hubEvents)))
			storeBefore := treeBytes(t, worktreeDataDir(root))

			_, err := svc.EnrolWorktree(ctx, enrolRequest(root))

			require.ErrorContains(t, err, tc.want)
			require.ErrorContains(t, err, "compare the local board with the hub")
			assertNothingChanged(t, root, storeBefore, nil, recorded)
		})
	}
}

func TestBoardFileFinding_UnreadableFileOrStore_IsAnErrorNotAPass(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := sqlite.New(filepath.Join(dir, "d"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	_, err = boardFileFinding(ctx, dir, st) // a directory is not a board file
	require.Error(t, err)
	none, err := boardFileFinding(ctx, filepath.Join(dir, "absent.json"), st)
	require.NoError(t, err)
	assert.Nil(t, none, "an absent file holds nothing to lose")

	closed, err := sqlite.New(filepath.Join(t.TempDir(), "d"), slog.Default())
	require.NoError(t, err)
	require.NoError(t, closed.Close())
	path := filepath.Join(dir, "tasks.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"nodes":[]}`), 0o600))
	_, err = boardFileFinding(ctx, path, closed)
	require.ErrorContains(t, err, "export the store")
}

// treeBytes maps every file under dir to its content, so a test can prove a
// directory is byte-identical before and after (lock files excepted).
func treeBytes(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	require.NoError(t, filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(p) == ".lock" {
			return err // the writer lock files are held and left by every enrolment attempt
		}
		b, readErr := os.ReadFile(p) //nolint:gosec // G304: a test tree
		out[p] = string(b)
		return readErr
	}))
	return out
}

// hubEventsHolding returns the journal events of a scratch store that created
// the given nodes (id, title): what the hub holds once those nodes are pushed.
func hubEventsHolding(t *testing.T, nodes map[string]string) []*model.SyncEvent {
	t.Helper()
	ctx := context.Background()
	b, err := sqlite.New(filepath.Join(t.TempDir(), ".mtix"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = b.Close() })
	seq := 0
	for id, title := range nodes {
		seq++
		require.NoError(t, b.CreateNode(ctx, mkPGNode(id, "", 0, seq, title)))
	}
	events, err := readPendingBatch(ctx, b, 100)
	require.NoError(t, err)
	for i, e := range events {
		e.CreatedAt = offlineEventsPushedAt.Add(time.Duration(i) * time.Second)
	}
	return events
}
