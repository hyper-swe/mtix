// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Regression tests for MTIX-95.31.2 (FR-15.2i, FR-15.2k): after a git pull
// of a teammate's .mtix/tasks.json, the next command's auto-import replaced
// the local store with the file and deleted local data the file lacked
// (reproduced: annotations 2 -> 0). The auto-import now refuses such a
// replace, changes nothing and says what would be lost and how to proceed;
// a replace that loses nothing applies and prints a one-line notice.
// Written red-first.
package service_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// refusalHeader is the first line of an auto-import refusal; it must appear
// once per refused auto-import.
const refusalHeader = "mtix: auto-import of .mtix/tasks.json refused"

// guardFixture is a project whose store holds PROJ-1 (two annotations) and
// PROJ-2, exported to .mtix/tasks.json with every sync baseline in place,
// as after the last command before a git pull.
type guardFixture struct {
	svc     *service.SyncService
	store   *sqlite.Store
	mtixDir string
	notices *bytes.Buffer
	logs    *bytes.Buffer // the service's log output
	now     time.Time
}

// guardAnnotations are the two local annotations on PROJ-1.
func guardAnnotations() []model.Annotation {
	ts := time.Date(2026, 9, 24, 9, 0, 0, 0, time.UTC)
	return []model.Annotation{
		{ID: "01J9GITPULL000000000000001", Author: "reviewer", Text: "PASS: evidence attached", CreatedAt: ts, Resolved: true},
		{ID: "01J9GITPULL000000000000002", Author: "lead", Text: "close receipt", CreatedAt: ts.Add(time.Minute)},
	}
}

// newGuardFixture builds the project. The service's clock reads f.now.
func newGuardFixture(t *testing.T) *guardFixture {
	t.Helper()
	ctx := context.Background()
	f := newProjectFixture(t)
	created := time.Date(2026, 9, 24, 8, 0, 0, 0, time.UTC)
	for i, id := range []string{"PROJ-1", "PROJ-2"} {
		require.NoError(t, f.store.CreateNode(ctx, &model.Node{
			ID: id, Project: "PROJ", Depth: 0, Seq: i + 1, Title: "Task " + id,
			Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
			NodeType: model.NodeTypeEpic, ContentHash: "h-" + id, CreatedAt: created, UpdatedAt: created,
		}))
	}
	require.NoError(t, f.store.SetAnnotations(ctx, "PROJ-1", guardAnnotations()))
	require.NoError(t, f.svc.AutoExport(ctx, f.mtixDir))
	return f
}

// newProjectFixture builds a project with an empty store and no
// tasks.json, as a fresh clone before its first command.
func newProjectFixture(t *testing.T) *guardFixture {
	t.Helper()
	dir := t.TempDir()
	f := &guardFixture{
		mtixDir: filepath.Join(dir, ".mtix"),
		notices: &bytes.Buffer{},
		now:     time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC),
	}
	require.NoError(t, os.MkdirAll(filepath.Join(f.mtixDir, "data"), 0o755))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := sqlite.New(filepath.Join(f.mtixDir, "data"), logger)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	f.store = st
	f.logs = &bytes.Buffer{}
	svcLogger := slog.New(slog.NewTextHandler(f.logs, nil))
	f.svc = service.NewSyncService(st, svcLogger, func() time.Time { return f.now })
	f.svc.SetNoticeWriter(f.notices)
	return f
}

// nodeIndex returns the index of node id in data.Nodes.
func nodeIndex(t *testing.T, data *sqlite.ExportData, id string) int {
	t.Helper()
	for i := range data.Nodes {
		if data.Nodes[i].ID == id {
			return i
		}
	}
	require.Failf(t, "node not in export", "node %s is not in the export", id)
	return -1
}

// addTeammateNode appends a copy of node from, renamed to id, as a node a
// teammate created.
func addTeammateNode(t *testing.T, data *sqlite.ExportData, from, id string, seq int) {
	t.Helper()
	added := data.Nodes[nodeIndex(t, data, from)]
	added.ID, added.Seq, added.UID = id, seq, ""
	added.Title, added.ContentHash = "Teammate task "+id, "h-"+id
	added.Annotations = nil
	data.Nodes = append(data.Nodes, added)
}

// addBoardDependency appends the dependency from -> to of type typ to a
// board, as a teammate's mtix dep add would.
func addBoardDependency(t *testing.T, data *sqlite.ExportData, from, to, typ string) {
	t.Helper()
	extra := data.Dependencies[:0:0]
	raw := `[{"from_id":"` + from + `","to_id":"` + to + `","dep_type":"` + typ +
		`","created_at":"2026-09-24T09:30:00Z"}]`
	require.NoError(t, json.Unmarshal([]byte(raw), &extra))
	data.Dependencies = append(data.Dependencies, extra...)
}

// teammateBoard returns the tasks.json a teammate would commit: the local
// store's export, changed by edit and sealed with a valid checksum.
func (f *guardFixture) teammateBoard(t *testing.T, edit func(data *sqlite.ExportData)) []byte {
	t.Helper()
	data, err := f.store.Export(context.Background(), "", "")
	require.NoError(t, err)
	edit(data)
	require.NoError(t, sqlite.RecomputeExportChecksum(data))
	raw, err := json.MarshalIndent(data, "", "  ")
	require.NoError(t, err)
	return raw
}

// pull writes board to .mtix/tasks.json, as git pull does.
func (f *guardFixture) pull(t *testing.T, board []byte) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(f.mtixDir, "tasks.json"), board, 0o644))
}

// read returns the content of a file under .mtix/.
func (f *guardFixture) read(t *testing.T, rel string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.mtixDir, rel))
	require.NoError(t, err)
	return raw
}

// storeSnapshot returns the local store's export as canonical JSON, with
// the export time cleared, to compare the store before and after.
func (f *guardFixture) storeSnapshot(t *testing.T) string {
	t.Helper()
	data, err := f.store.Export(context.Background(), "", "")
	require.NoError(t, err)
	data.ExportedAt = ""
	raw, err := json.Marshal(data)
	require.NoError(t, err)
	return string(raw)
}

// preSyncBackups lists the pre-import backups in .mtix/data/backups.
func (f *guardFixture) preSyncBackups(t *testing.T) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(f.mtixDir, "data", "backups", "pre-sync-*.db"))
	require.NoError(t, err)
	names := make([]string, 0, len(matches))
	for _, m := range matches {
		names = append(names, filepath.Base(m))
	}
	return names
}

// annotationCount returns how many annotations node id holds.
func (f *guardFixture) annotationCount(t *testing.T, id string) int {
	t.Helper()
	node, err := f.store.GetNode(context.Background(), id)
	require.NoError(t, err)
	return len(node.Annotations)
}

// asOlderClientBoard turns a board into what a client older than 0.5.4
// re-exports: schema_version 1.0.0 and none of the node keys 2.0.0 added,
// annotations and activity included.
func asOlderClientBoard(t *testing.T, board []byte) []byte {
	t.Helper()
	var doc map[string]any
	dec := json.NewDecoder(bytes.NewReader(board))
	dec.UseNumber()
	require.NoError(t, dec.Decode(&doc))
	doc["schema_version"] = "1.0.0"
	nodes, ok := doc["nodes"].([]any)
	require.True(t, ok)
	for _, raw := range nodes {
		n, isObj := raw.(map[string]any)
		require.True(t, isObj)
		for _, key := range []string{
			"previous_status", "estimate_min", "actual_min", "code_refs", "commit_refs",
			"annotations", "invalidated_at", "invalidated_by", "invalidation_reason",
			"activity", "deleted_by", "metadata", "session_id",
		} {
			delete(n, key)
		}
	}
	stripped, err := json.Marshal(doc)
	require.NoError(t, err)
	data, err := sqlite.DecodeExportData(bytes.NewReader(stripped))
	require.NoError(t, err)
	require.NoError(t, sqlite.RecomputeExportChecksum(data))
	out, err := json.MarshalIndent(data, "", "  ")
	require.NoError(t, err)
	return out
}

// assertRefusedUnchanged checks a refused auto-import changed nothing: the
// store, tasks.json and its stored hash are as they were, and no backup was
// taken.
func (f *guardFixture) assertRefusedUnchanged(t *testing.T, err error, snapshot, board, hash string) {
	t.Helper()
	require.ErrorIs(t, err, service.ErrAutoImportRefused)
	assert.Equal(t, snapshot, f.storeSnapshot(t), "a refused auto-import changes nothing in the store")
	assert.Equal(t, board, string(f.read(t, "tasks.json")), "tasks.json is left as pulled")
	assert.Equal(t, hash, string(f.read(t, "data/sync.sha256")), "the stored hash is not updated")
	assert.Empty(t, f.preSyncBackups(t), "a refusal takes no backup")
}

// TestAutoImport_TeammateBoardWithoutLocalAnnotations_RefusesAndKeepsThem is
// the git-pull regression: a teammate's board lacks PROJ-1's two local
// annotations and adds a task. The auto-import must refuse, keep both
// annotations and change nothing, name the loss and the commands that
// proceed, and repeat the refusal, printed once, on every command. With and
// without the conflict baseline (sync-db.sha256).
func TestAutoImport_TeammateBoardWithoutLocalAnnotations_RefusesAndKeepsThem(t *testing.T) {
	for _, withBaseline := range []bool{true, false} {
		name := "without conflict baseline"
		if withBaseline {
			name = "with conflict baseline"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			f := newGuardFixture(t)
			board := f.teammateBoard(t, func(d *sqlite.ExportData) {
				d.Nodes[nodeIndex(t, d, "PROJ-1")].Annotations = nil
				addTeammateNode(t, d, "PROJ-2", "PROJ-3", 3)
			})
			if !withBaseline {
				require.NoError(t, os.Remove(filepath.Join(f.mtixDir, "data", "sync-db.sha256")))
			}
			hash := string(f.read(t, "data/sync.sha256"))
			before := f.storeSnapshot(t)
			f.pull(t, board)

			for command := 1; command <= 2; command++ {
				err := f.svc.AutoImport(ctx, f.mtixDir)
				f.assertRefusedUnchanged(t, err, before, string(board), hash)
				assert.Equal(t, 2, f.annotationCount(t, "PROJ-1"), "every local annotation is kept")
				_, getErr := f.store.GetNode(ctx, "PROJ-3")
				assert.ErrorIs(t, getErr, model.ErrNotFound, "nothing from the file is imported")
				assert.Equal(t, command, strings.Count(f.notices.String(), refusalHeader),
					"the refusal repeats on every command and is printed once per command")
			}

			msg := f.notices.String()
			assert.Contains(t, msg, "PROJ-1: 2 annotations")
			assert.Contains(t, msg, "01J9GITPULL000000000000001")
			assert.Contains(t, msg, "mtix import .mtix/tasks.json --mode merge")
			assert.Contains(t, msg, "mtix import .mtix/tasks.json --mode replace")
			assert.Contains(t, msg, "mtix sync --fix")
			assert.NotContains(t, msg, "PROJ-2:", "PROJ-2 loses nothing")
		})
	}
}

// TestAutoImport_OlderClientBoardV100_RefusesAndKeepsStore is the case the
// guard exists for: a teammate on a client older than 0.5.4 re-exported the
// board as schema 1.0.0, without annotations or activity, and changed a
// task. A 0.5.4 store must refuse it and keep everything.
func TestAutoImport_OlderClientBoardV100_RefusesAndKeepsStore(t *testing.T) {
	for _, withBaseline := range []bool{true, false} {
		name := "without conflict baseline"
		if withBaseline {
			name = "with conflict baseline"
		}
		t.Run(name, func(t *testing.T) {
			f := newGuardFixture(t)
			board := asOlderClientBoard(t, f.teammateBoard(t, func(d *sqlite.ExportData) {
				i := nodeIndex(t, d, "PROJ-2")
				d.Nodes[i].Title, d.Nodes[i].ContentHash = "Retitled on the older client", "h-older"
			}))
			if !withBaseline {
				require.NoError(t, os.Remove(filepath.Join(f.mtixDir, "data", "sync-db.sha256")))
			}
			hash := string(f.read(t, "data/sync.sha256"))
			before := f.storeSnapshot(t)
			f.pull(t, board)

			err := f.svc.AutoImport(context.Background(), f.mtixDir)
			f.assertRefusedUnchanged(t, err, before, string(board), hash)
			assert.Equal(t, 2, f.annotationCount(t, "PROJ-1"))
			msg := f.notices.String()
			assert.Contains(t, msg, "PROJ-1: 2 annotations")
			assert.Contains(t, msg, "activity entr")
			assert.Contains(t, msg, "PROJ-2: 1 activity entry")
		})
	}
}

// TestAutoImport_LossyReplace_RefusedNamingTheLoss verifies each kind of
// local data a teammate's board can lack is refused and named: a whole
// node, a field value, an activity entry and an annotation's resolution.
func TestAutoImport_LossyReplace_RefusedNamingTheLoss(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, f *guardFixture) // before the board is taken
		later func(t *testing.T, f *guardFixture) // a local write after it
		edit  func(t *testing.T, d *sqlite.ExportData)
		want  string
	}{
		{"board lacks a local node", nil, nil, func(t *testing.T, d *sqlite.ExportData) {
			i := nodeIndex(t, d, "PROJ-2")
			d.Nodes = append(d.Nodes[:i], d.Nodes[i+1:]...)
		}, "PROJ-2: the whole node"},
		{"stale board blanks a local claim", nil, func(t *testing.T, f *guardFixture) {
			require.NoError(t, f.store.ClaimNode(context.Background(), "PROJ-2", "agent-local"))
		}, func(*testing.T, *sqlite.ExportData) {}, "PROJ-2: 1 activity entry, fields agent_state, assignee"},
		{"board lacks a local dependency", func(t *testing.T, f *guardFixture) {
			require.NoError(t, f.store.AddDependency(context.Background(), &model.Dependency{
				FromID: "PROJ-1", ToID: "PROJ-2", DepType: model.DepTypeRelated,
			}))
			require.NoError(t, f.svc.AutoExport(context.Background(), f.mtixDir))
		}, nil, func(_ *testing.T, d *sqlite.ExportData) {
			d.Dependencies = nil
		}, "PROJ-1: 1 dependency (related PROJ-2)"},
		{"board lacks a local activity entry", nil, nil, func(t *testing.T, d *sqlite.ExportData) {
			d.Nodes[nodeIndex(t, d, "PROJ-2")].Activity = nil
		}, "PROJ-2: 1 activity entry"},
		{"board reopens a resolved annotation", nil, nil, func(t *testing.T, d *sqlite.ExportData) {
			d.Nodes[nodeIndex(t, d, "PROJ-1")].Annotations[0].Resolved = false
		}, "PROJ-1: 1 annotation resolution (01J9GITPULL000000000000001)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newGuardFixture(t)
			if tt.setup != nil {
				tt.setup(t, f)
			}
			board := f.teammateBoard(t, func(d *sqlite.ExportData) { tt.edit(t, d) })
			if tt.later != nil {
				tt.later(t, f)
				require.NoError(t, f.svc.AutoExport(context.Background(), f.mtixDir)) // a writing command
			}
			hash := string(f.read(t, "data/sync.sha256"))
			before := f.storeSnapshot(t)
			f.pull(t, board)

			err := f.svc.AutoImport(context.Background(), f.mtixDir)
			f.assertRefusedUnchanged(t, err, before, string(board), hash)
			assert.Contains(t, f.notices.String(), tt.want)
			assert.Equal(t, 1, strings.Count(f.notices.String(), refusalHeader))
		})
	}
}

// TestAutoImport_TeammateAdditionsAndUpdates_AppliesWithOneLineNotice
// verifies a board that only adds and changes (a new task, a retitled task,
// a new annotation) is not lossy: it is imported, and a single line on the
// notice writer says what changed and where the backup is.
func TestAutoImport_TeammateAdditionsAndUpdates_AppliesWithOneLineNotice(t *testing.T) {
	ctx := context.Background()
	f := newGuardFixture(t)
	board := f.teammateBoard(t, func(d *sqlite.ExportData) {
		i := nodeIndex(t, d, "PROJ-2")
		d.Nodes[i].Title, d.Nodes[i].ContentHash = "Retitled upstream", "h-upstream"
		j := nodeIndex(t, d, "PROJ-1")
		d.Nodes[j].Annotations = append(d.Nodes[j].Annotations, model.Annotation{
			ID: "01J9GITPULL000000000000003", Author: "teammate", Text: "LGTM",
			CreatedAt: time.Date(2026, 9, 24, 9, 30, 0, 0, time.UTC),
		})
		addTeammateNode(t, d, "PROJ-2", "PROJ-3", 3)
		addBoardDependency(t, d, "PROJ-3", "PROJ-1", "related")
	})
	f.pull(t, board)

	require.NoError(t, f.svc.AutoImport(ctx, f.mtixDir))

	after, err := f.store.Export(ctx, "", "")
	require.NoError(t, err)
	assert.Len(t, after.Dependencies, 1, "the teammate's dependency is imported")
	node, err := f.store.GetNode(ctx, "PROJ-2")
	require.NoError(t, err)
	assert.Equal(t, "Retitled upstream", node.Title)
	_, err = f.store.GetNode(ctx, "PROJ-3")
	require.NoError(t, err, "the teammate's task is imported")
	assert.Equal(t, 3, f.annotationCount(t, "PROJ-1"))
	assert.Equal(t, hashBytes(board), string(f.read(t, "data/sync.sha256")))

	backups := f.preSyncBackups(t)
	require.Len(t, backups, 1)
	want := "mtix: imported the changed .mtix/tasks.json: nodes 1 added, 2 updated, 0 removed; " +
		"dependencies 1 added, 0 removed (local database backed up to " +
		filepath.Join(".mtix", "data", "backups", backups[0]) + ")\n"
	assert.Equal(t, want, f.notices.String(), "exactly one notice line")
}

// TestAutoImport_ManyNodesLost_RefusalListsTenAndCountsTheRest verifies the
// refusal lists at most ten nodes, counts the rest, and still refuses.
func TestAutoImport_ManyNodesLost_RefusalListsTenAndCountsTheRest(t *testing.T) {
	ctx := context.Background()
	f := newGuardFixture(t)
	created := time.Date(2026, 9, 24, 8, 0, 0, 0, time.UTC)
	for seq := 3; seq <= 13; seq++ {
		id := "PROJ-" + strconv.Itoa(seq)
		require.NoError(t, f.store.CreateNode(ctx, &model.Node{
			ID: id, Project: "PROJ", Depth: 0, Seq: seq, Title: "Task " + id,
			Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
			NodeType: model.NodeTypeEpic, ContentHash: "h-" + id, CreatedAt: created, UpdatedAt: created,
		}))
	}
	require.NoError(t, f.svc.AutoExport(ctx, f.mtixDir))
	f.pull(t, f.teammateBoard(t, func(d *sqlite.ExportData) {
		d.Nodes = d.Nodes[:1] // PROJ-1 only: twelve local nodes are missing
	}))

	err := f.svc.AutoImport(ctx, f.mtixDir)
	require.ErrorIs(t, err, service.ErrAutoImportRefused)
	msg := f.notices.String()
	assert.Equal(t, 10, strings.Count(msg, ": the whole node"), msg)
	assert.Contains(t, msg, "  and 2 more nodes\n")
	assert.Contains(t, err.Error(), "; and 2 more nodes")
}

// TestSetNoticeWriter_Nil_DiscardsNotices verifies a nil writer discards
// notices instead of panicking on the next import.
func TestSetNoticeWriter_Nil_DiscardsNotices(t *testing.T) {
	ctx := context.Background()
	f := newGuardFixture(t)
	f.svc.SetNoticeWriter(nil)
	f.pull(t, f.teammateBoard(t, func(d *sqlite.ExportData) { addTeammateNode(t, d, "PROJ-2", "PROJ-3", 3) }))

	require.NoError(t, f.svc.AutoImport(ctx, f.mtixDir))
	_, err := f.store.GetNode(ctx, "PROJ-3")
	require.NoError(t, err)
	assert.Empty(t, f.notices.String(), "the old writer no longer receives notices")
}

// TestAutoImport_TwoPullsWithoutAWrite_BothApply verifies a successful
// auto-import refreshes the conflict baseline: a second pulled board, with
// no write in between, is imported rather than reported as a conflict
// (FR-15.2h, MTIX-95.31.2).
func TestAutoImport_TwoPullsWithoutAWrite_BothApply(t *testing.T) {
	ctx := context.Background()
	f := newGuardFixture(t)
	for seq := 3; seq <= 4; seq++ {
		id := "PROJ-" + strconv.Itoa(seq)
		f.pull(t, f.teammateBoard(t, func(d *sqlite.ExportData) { addTeammateNode(t, d, "PROJ-2", id, seq) }))
		require.NoError(t, f.svc.AutoImport(ctx, f.mtixDir))
		_, err := f.store.GetNode(ctx, id)
		require.NoError(t, err, "pull of %s is imported", id)
	}
	report, err := f.svc.Compare(ctx, f.mtixDir)
	require.NoError(t, err)
	assert.Nil(t, report.AutoImport.LastRefusal, "no conflict was recorded")
	assert.Equal(t, 2, strings.Count(f.notices.String(), "mtix: imported"))
}

// TestAutoExport_ConflictBaseline_IgnoresExportTime verifies the conflict
// baseline (sync-db.sha256) is the hash of the store's export without its
// export time, so exports of the same data in different seconds match.
func TestAutoExport_ConflictBaseline_IgnoresExportTime(t *testing.T) {
	f := newGuardFixture(t)
	data, err := f.store.Export(context.Background(), "", "")
	require.NoError(t, err)
	require.NotEmpty(t, data.ExportedAt)
	data.ExportedAt = ""
	raw, err := json.Marshal(data)
	require.NoError(t, err)
	assert.Equal(t, hashBytes(raw), string(f.read(t, "data/sync-db.sha256")))
}
