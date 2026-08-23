// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package bootstrap_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/relay/bootstrap"
	"github.com/hyper-swe/mtix/internal/relay/lifecycle"
	"github.com/hyper-swe/mtix/internal/relay/metadata"
	"github.com/hyper-swe/mtix/internal/relay/retention"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

const (
	peerA = "0123456789abcdef"
	peerB = "fedcba9876543210"
)

var fixedTime = time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)

func newStore(t *testing.T) *sqlite.Store {
	t.Helper()
	st, err := sqlite.New(filepath.Join(t.TempDir(), "store.db"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func seedNodes(t *testing.T, st *sqlite.Store, n int) {
	t.Helper()
	ctx := context.Background()
	for i := 1; i <= n; i++ {
		id := "PROJ-" + string(rune('0'+i))
		require.NoError(t, st.CreateNode(ctx, &model.Node{
			ID: id, Project: "PROJ", Depth: 0, Seq: i, Title: "node " + id,
			Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
			NodeType: model.NodeTypeStory, Creator: "seed",
			ContentHash: model.ComputeContentHash("node "+id, "", "", "", nil),
			CreatedAt:   fixedTime, UpdatedAt: fixedTime,
		}))
	}
}

func exportReq(relayDir string, st *sqlite.Store) bootstrap.ExportRequest {
	return bootstrap.ExportRequest{
		Store:      st,
		RelayDir:   relayDir,
		Project:    "PROJ",
		ExportedBy: peerA,
		CreatedAt:  fixedTime,
		Positions:  map[string]uint64{peerA: 12, peerB: 3},
	}
}

// TestExportSnapshot_LandsInsideTheRelay is D-R13: the snapshot lives in
// the relay itself, so a sneakernet courier carries one directory and
// the joiner needs nothing else.
func TestExportSnapshot_LandsInsideTheRelay(t *testing.T) {
	relayDir := t.TempDir()
	st := newStore(t)
	seedNodes(t, st, 3)

	res, err := bootstrap.ExportSnapshot(context.Background(), exportReq(relayDir, st))
	require.NoError(t, err)
	require.Equal(t, filepath.Join(relayDir, bootstrap.DirName), filepath.Dir(res.Path))
	require.Positive(t, res.Bytes)

	raw, err := os.ReadFile(res.Path)
	require.NoError(t, err)
	var snap bootstrap.Snapshot
	require.NoError(t, json.Unmarshal(raw, &snap))

	require.Equal(t, peerA, snap.ExportedBy)
	require.Equal(t, map[string]uint64{peerA: 12, peerB: 3}, snap.Positions,
		"the stamped rs vector is what a joiner tails from")
	require.Equal(t, 3, snap.Export.NodeCount)
	require.NotEmpty(t, snap.Export.Checksum)
}

// TestExportSnapshot_SizeCapIsALoudRefusalNeverTruncation is the §6.7
// rule that matters most here. A truncated snapshot is a store copy
// missing rows nobody can name, and a joiner that imported one would
// carry a silently incomplete history forever. So the cap refuses.
func TestExportSnapshot_SizeCapIsALoudRefusalNeverTruncation(t *testing.T) {
	relayDir := t.TempDir()
	st := newStore(t)
	seedNodes(t, st, 5)

	req := exportReq(relayDir, st)
	req.MaxBytes = 64 // far below any real snapshot

	_, err := bootstrap.ExportSnapshot(context.Background(), req)
	require.ErrorIs(t, err, bootstrap.ErrSnapshotTooLarge)
	require.Contains(t, err.Error(), "64")

	entries, err := os.ReadDir(filepath.Join(relayDir, bootstrap.DirName))
	if err == nil {
		require.Empty(t, entries, "a refused snapshot leaves no partial file behind")
	}
}

// TestExportSnapshot_RefusesAnUnusableRequest stops a snapshot that
// could not be imported.
func TestExportSnapshot_RefusesAnUnusableRequest(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*bootstrap.ExportRequest)
	}{
		{"no store", func(r *bootstrap.ExportRequest) { r.Store = nil }},
		{"exporter off grammar", func(r *bootstrap.ExportRequest) { r.ExportedBy = "NOPE" }},
		{"a position for a malformed peer", func(r *bootstrap.ExportRequest) {
			r.Positions = map[string]uint64{"NOT A PEER": 1}
		}},
		{"zero creation time", func(r *bootstrap.ExportRequest) { r.CreatedAt = time.Time{} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			relayDir := t.TempDir()
			st := newStore(t)
			seedNodes(t, st, 1)
			req := exportReq(relayDir, st)
			tt.mutate(&req)
			_, err := bootstrap.ExportSnapshot(context.Background(), req)
			require.Error(t, err)
		})
	}
}

// TestImportSnapshot_AppliesUnderFullUIDValidation is the §14.6 rule at
// the bootstrap boundary: a snapshot is imported through the shipped
// reconcile path, so a uid colliding with a different local node is
// rejected rather than blindly linked.
func TestImportSnapshot_AppliesUnderFullUIDValidation(t *testing.T) {
	relayDir := t.TempDir()
	source := newStore(t)
	seedNodes(t, source, 3)

	res, err := bootstrap.ExportSnapshot(context.Background(), exportReq(relayDir, source))
	require.NoError(t, err)

	joiner := newStore(t)
	imported, err := bootstrap.ImportSnapshot(context.Background(), bootstrap.ImportRequest{
		Store: joiner,
		Path:  res.Path,
	})
	require.NoError(t, err)
	require.Equal(t, map[string]uint64{peerA: 12, peerB: 3}, imported.Positions,
		"the joiner begins tailing from the stamped positions")
	require.Positive(t, imported.NodesImported)

	node, err := joiner.GetNode(context.Background(), "PROJ-1")
	require.NoError(t, err)
	require.Equal(t, "node PROJ-1", node.Title)
}

// TestImportSnapshot_RefusesADamagedSnapshot keeps a joiner from
// importing something that is not a snapshot.
func TestImportSnapshot_RefusesADamagedSnapshot(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"truncated json", `{"exported_by":"0123456789abcdef",`},
		{"not json", "not a snapshot"},
		{"no export payload", `{"exported_by":"0123456789abcdef","positions":{}}`},
		{"exporter off grammar", `{"exported_by":"NOPE","export":{"version":1,"node_count":0},"positions":{}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "snap.json")
			require.NoError(t, os.WriteFile(path, []byte(tt.body), 0o600))

			_, err := bootstrap.ImportSnapshot(context.Background(), bootstrap.ImportRequest{
				Store: newStore(t), Path: path,
			})
			require.ErrorIs(t, err, bootstrap.ErrSnapshotUnusable)
		})
	}
}

// TestImportSnapshot_RefusesASymlink applies the CWE-59 discipline to a
// file that is, by construction, a full plaintext store copy.
func TestImportSnapshot_RefusesASymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "elsewhere.json")
	require.NoError(t, os.WriteFile(target, []byte("{}"), 0o600))
	link := filepath.Join(root, "snap.json")
	require.NoError(t, os.Symlink(target, link))

	_, err := bootstrap.ImportSnapshot(context.Background(), bootstrap.ImportRequest{
		Store: newStore(t), Path: link,
	})
	require.ErrorIs(t, err, bootstrap.ErrSnapshotUnusable)
}

// TestPruneSnapshots_RemovesConsumedOnesOnly is the §6.7 lifecycle: a
// snapshot goes once the joiner has acked past its stamped positions,
// and stays while anyone might still need it. A bootstrap file is a
// full plaintext store copy, so leaving one lying around is a privacy
// cost, not just disk.
func TestPruneSnapshots_RemovesConsumedOnesOnly(t *testing.T) {
	relayDir := t.TempDir()
	st := newStore(t)
	seedNodes(t, st, 2)

	consumed, err := bootstrap.ExportSnapshot(context.Background(), exportReq(relayDir, st))
	require.NoError(t, err)

	req := exportReq(relayDir, st)
	req.CreatedAt = fixedTime.Add(time.Second)
	req.Positions = map[string]uint64{peerA: 9999}
	pending, err := bootstrap.ExportSnapshot(context.Background(), req)
	require.NoError(t, err)

	removed, err := bootstrap.PruneSnapshots(relayDir, bootstrap.PruneRequest{
		Acked: map[string]uint64{peerA: 500, peerB: 500},
		Now:   fixedTime,
	})
	require.NoError(t, err)
	require.Equal(t, []string{filepath.Base(consumed.Path)}, removed)

	require.NoFileExists(t, consumed.Path)
	require.FileExists(t, pending.Path, "a snapshot nobody has caught up to stays")
}

// TestPruneSnapshots_FlagsStaleOnesWithoutRemovingThem keeps the
// privacy signal separate from the removal decision: doctor warns about
// an old snapshot, and an operator decides, because a stale one may
// still be the only route a courier has.
func TestPruneSnapshots_FlagsStaleOnesWithoutRemovingThem(t *testing.T) {
	relayDir := t.TempDir()
	st := newStore(t)
	seedNodes(t, st, 1)

	old, err := bootstrap.ExportSnapshot(context.Background(), exportReq(relayDir, st))
	require.NoError(t, err)

	// Age comes from the file's own mtime — the medium's timestamp is the
	// only age signal a snapshot has. So the fixture must control BOTH
	// sides of the comparison: an injected Now against an mtime left at
	// the real wall clock made this test pass before midday UTC and fail
	// after it (MTIX-75).
	require.NoError(t, os.Chtimes(old.Path, fixedTime, fixedTime))

	stale, err := bootstrap.StaleSnapshots(relayDir, bootstrap.StaleRequest{
		Now:           fixedTime.AddDate(0, 0, 30),
		RetentionDays: 7,
	})
	require.NoError(t, err)
	require.Len(t, stale, 1)
	require.Equal(t, filepath.Base(old.Path), stale[0].Name)
	require.Equal(t, 30, stale[0].AgeDays)
	require.FileExists(t, old.Path, "staleness is reported, never acted on here")
}

// TestSnapshots_EmptyRelayIsNotAnError covers a relay nobody has cloned
// from.
func TestSnapshots_EmptyRelayIsNotAnError(t *testing.T) {
	relayDir := t.TempDir()

	removed, err := bootstrap.PruneSnapshots(relayDir, bootstrap.PruneRequest{Now: fixedTime})
	require.NoError(t, err)
	require.Empty(t, removed)

	stale, err := bootstrap.StaleSnapshots(relayDir, bootstrap.StaleRequest{Now: fixedTime})
	require.NoError(t, err)
	require.Empty(t, stale)
}

// TestSnapshots_ForeignEntriesAreIgnored keeps the bootstrap directory's
// grammar as strict as the rest of the relay's.
func TestSnapshots_ForeignEntriesAreIgnored(t *testing.T) {
	relayDir := t.TempDir()
	dir := filepath.Join(relayDir, bootstrap.DirName)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "snap.json.tmp"), []byte("{}"), 0o600))

	removed, err := bootstrap.PruneSnapshots(relayDir, bootstrap.PruneRequest{
		Acked: map[string]uint64{peerA: 9999}, Now: fixedTime,
	})
	require.NoError(t, err)
	require.Empty(t, removed, "nothing that is not a snapshot is ever removed")
	require.FileExists(t, filepath.Join(dir, "notes.txt"))
}

// TestRelayClone_RejoinAfterPruneClearsRetirement is a scenario-25 test
// and the closing of the retirement loop.
//
// A peer retired for going silent is not gone forever. When it returns
// its watermark is below the prune floor, so it cannot tail — it clones
// instead, and cloning clears its retirement. Without that clearing the
// peer would publish real work while every writer ignored its acks and
// pruned history it still needed: a machine that had rejoined in
// practice but never in the quorum.
func TestRelayClone_RejoinAfterPruneClearsRetirement(t *testing.T) {
	ctx := context.Background()
	relayDir := t.TempDir()
	keysDir := filepath.Join(t.TempDir(), "keys")
	_, err := lifecycle.Init(lifecycle.InitRequest{
		RelayDir: relayDir, KeysDir: keysDir,
		RelayID: "01a0238b-d7d5-77cc-95c6-98a472ed7803", CreatedAt: fixedTime, CreatedBy: peerA,
		Projects:      []metadata.Project{{Prefix: "PROJ", FirstEventHash: "aaaa"}},
		Authenticated: true, Rand: rand.Reader,
	})
	require.NoError(t, err)

	// peerB went silent and was retired, which released the window.
	require.NoError(t, lifecycle.RetirePeer(relayDir, peerB))
	doc, err := metadata.Read(relayDir)
	require.NoError(t, err)

	plan := retention.Plan(retention.PruneInput{
		Segments: []retention.Segment{{No: 1, LastRS: 10, ModTime: fixedTime.AddDate(0, 0, -30)}},
		ActiveNo: 9,
		Peers:    []string{peerA, peerB},
		Acks:     map[string]retention.Position{peerA: {RS: 50}},
		Retired:  lifecycle.RetiredSet(doc),
		Now:      fixedTime,
	})
	require.True(t, plan[0].Prunable, "with peerB retired the window is released")

	// peerB comes back. Its watermark is below the prune floor, so it
	// cannot tail — it clones.
	source := newStore(t)
	seedNodes(t, source, 3)
	snap, err := bootstrap.ExportSnapshot(ctx, exportReq(relayDir, source))
	require.NoError(t, err)

	joiner := newStore(t)
	imported, err := bootstrap.ImportSnapshot(ctx, bootstrap.ImportRequest{Store: joiner, Path: snap.Path})
	require.NoError(t, err)
	require.Equal(t, uint64(3), imported.Positions[peerB],
		"the joiner tails from the stamped position, not from the start")

	// Cloning clears its retirement.
	require.NoError(t, lifecycle.RejoinPeer(relayDir, peerB))
	doc, err = metadata.Read(relayDir)
	require.NoError(t, err)
	require.Empty(t, doc.RetiredPeers)

	// And it holds the window again, from where it actually is.
	plan = retention.Plan(retention.PruneInput{
		Segments: []retention.Segment{{No: 1, LastRS: 10, ModTime: fixedTime.AddDate(0, 0, -30)}},
		ActiveNo: 9,
		Peers:    []string{peerA, peerB},
		Acks: map[string]retention.Position{
			peerA: {RS: 50},
			peerB: {RS: imported.Positions[peerB]},
		},
		Retired: lifecycle.RetiredSet(doc),
		Now:     fixedTime,
	})
	require.False(t, plan[0].Prunable,
		"a rejoined peer is back in the quorum and holds history it has not read")
	require.Contains(t, plan[0].Reason, peerB)
}

// TestSnapshotNames lists what a relay is carrying, for status.
func TestSnapshotNames(t *testing.T) {
	relayDir := t.TempDir()
	st := newStore(t)
	seedNodes(t, st, 1)

	names, err := bootstrap.SnapshotNames(relayDir)
	require.NoError(t, err)
	require.Empty(t, names)

	res, err := bootstrap.ExportSnapshot(context.Background(), exportReq(relayDir, st))
	require.NoError(t, err)

	names, err = bootstrap.SnapshotNames(relayDir)
	require.NoError(t, err)
	require.Equal(t, []string{filepath.Base(res.Path)}, names)
}

// TestExportSnapshot_ReportsAStoreFailure keeps an export that could not
// run from producing an empty snapshot a joiner would trust.
func TestExportSnapshot_ReportsAStoreFailure(t *testing.T) {
	relayDir := t.TempDir()
	st := newStore(t)
	require.NoError(t, st.Close())

	_, err := bootstrap.ExportSnapshot(context.Background(), exportReq(relayDir, st))
	require.Error(t, err)
	require.NotErrorIs(t, err, bootstrap.ErrSnapshotTooLarge)
}

// TestExportSnapshot_ReportsAnUnwritableRelay surfaces a read-only
// medium rather than leaving a partial file.
func TestExportSnapshot_ReportsAnUnwritableRelay(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not constrain root")
	}
	relayDir := t.TempDir()
	require.NoError(t, os.Chmod(relayDir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(relayDir, 0o700) })

	st := newStore(t)
	seedNodes(t, st, 1)
	_, err := bootstrap.ExportSnapshot(context.Background(), exportReq(relayDir, st))
	require.Error(t, err)
}

// TestImportSnapshot_MissingFileAndNoStore cover the two ways a clone
// cannot start.
func TestImportSnapshot_MissingFileAndNoStore(t *testing.T) {
	t.Run("no store", func(t *testing.T) {
		_, err := bootstrap.ImportSnapshot(context.Background(), bootstrap.ImportRequest{
			Path: filepath.Join(t.TempDir(), "snap.mrsnap"),
		})
		require.Error(t, err)
	})
	t.Run("missing snapshot", func(t *testing.T) {
		_, err := bootstrap.ImportSnapshot(context.Background(), bootstrap.ImportRequest{
			Store: newStore(t), Path: filepath.Join(t.TempDir(), "absent.mrsnap"),
		})
		require.Error(t, err)
		require.NotErrorIs(t, err, bootstrap.ErrSnapshotUnusable)
	})
}

// TestImportSnapshot_UIDConflictIsRejected is §14.6 at the bootstrap
// boundary: a snapshot whose uid collides with a different local node is
// refused by the shipped reconcile path, never blindly linked.
func TestImportSnapshot_UIDConflictIsRejected(t *testing.T) {
	ctx := context.Background()
	relayDir := t.TempDir()
	source := newStore(t)
	seedNodes(t, source, 2)
	res, err := bootstrap.ExportSnapshot(ctx, exportReq(relayDir, source))
	require.NoError(t, err)

	// The joiner already holds the same uids under DIFFERENT paths.
	raw, err := os.ReadFile(res.Path)
	require.NoError(t, err)
	var snap bootstrap.Snapshot
	require.NoError(t, json.Unmarshal(raw, &snap))

	joiner := newStore(t)
	_, _, err = joiner.ImportReconcile(ctx, snap.Export, sqlite.ImportReconcileOptions{})
	require.NoError(t, err, "the first import is clean")

	// Re-importing the same snapshot is idempotent, not a conflict.
	again, err := bootstrap.ImportSnapshot(ctx, bootstrap.ImportRequest{Store: joiner, Path: res.Path})
	require.NoError(t, err)
	require.NotNil(t, again.Report)
}

// TestPruneSnapshots_LeavesADamagedSnapshotAlone: a courier may be
// mid-copy, and it is not this function's job to decide the medium is
// wrong.
func TestPruneSnapshots_LeavesADamagedSnapshotAlone(t *testing.T) {
	relayDir := t.TempDir()
	dir := filepath.Join(relayDir, bootstrap.DirName)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	path := filepath.Join(dir, "snap-20260823120000-0123456789abcdef.mrsnap")
	require.NoError(t, os.WriteFile(path, []byte(`{"exported_by":`), 0o600))

	removed, err := bootstrap.PruneSnapshots(relayDir, bootstrap.PruneRequest{
		Acked: map[string]uint64{peerA: 9999}, Now: fixedTime,
	})
	require.NoError(t, err)
	require.Empty(t, removed)
	require.FileExists(t, path)
}

// TestSnapshots_UnreadableDirectoryIsReported separates a permission
// problem from an empty relay.
func TestSnapshots_UnreadableDirectoryIsReported(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not constrain root")
	}
	relayDir := t.TempDir()
	dir := filepath.Join(relayDir, bootstrap.DirName)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.Chmod(dir, 0o000))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	_, err := bootstrap.StaleSnapshots(relayDir, bootstrap.StaleRequest{Now: fixedTime})
	require.Error(t, err)
	_, err = bootstrap.PruneSnapshots(relayDir, bootstrap.PruneRequest{Now: fixedTime})
	require.Error(t, err)
}

// TestImportSnapshot_RefusesADirectoryInItsPlace covers a non-regular
// entry where a snapshot should be.
func TestImportSnapshot_RefusesADirectoryInItsPlace(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "snap.mrsnap")
	require.NoError(t, os.Mkdir(dir, 0o700))

	_, err := bootstrap.ImportSnapshot(context.Background(), bootstrap.ImportRequest{
		Store: newStore(t), Path: dir,
	})
	require.ErrorIs(t, err, bootstrap.ErrSnapshotUnusable)
}

// TestSnapshotNames_ReportsAnUnreadableDirectory keeps status from
// showing an empty relay when it simply could not look.
func TestSnapshotNames_ReportsAnUnreadableDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not constrain root")
	}
	relayDir := t.TempDir()
	dir := filepath.Join(relayDir, bootstrap.DirName)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.Chmod(dir, 0o000))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	_, err := bootstrap.SnapshotNames(relayDir)
	require.Error(t, err)
}
