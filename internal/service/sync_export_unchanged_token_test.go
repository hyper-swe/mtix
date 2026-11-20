// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

// tokenCounters counts the whole-board work an export pass does.
type tokenCounters struct {
	exports int
	reads   int
}

// countPassWork installs counting seams on the fixture's service and returns
// the counters (MTIX-130.20.2).
func countPassWork(f *marshalFixture) *tokenCounters {
	c := &tokenCounters{}
	f.svc.exportBoardData = func(ctx context.Context) (*sqlite.ExportData, error) {
		c.exports++
		return f.st.Export(ctx, "", "")
	}
	f.svc.readPublished = func(path string) ([]byte, error) {
		c.reads++
		return os.ReadFile(path)
	}
	return c
}

// An export pass on a board nothing has changed since its last publication
// does no whole-board work: no store.Export, no tasks.json read (MTIX-130.20.2).
func TestAutoExport_UnchangedSincePublication_DoesNoWholeBoardWork(t *testing.T) {
	f := newMarshalFixture(t)
	ctx := context.Background()
	require.NoError(t, f.svc.AutoExport(ctx, f.dir))
	c := countPassWork(f)
	for tick := 0; tick < 5; tick++ {
		*f.now = f.now.Add(time.Minute)
		require.NoError(t, f.svc.AutoExport(ctx, f.dir))
	}
	require.Zero(t, c.exports, "an unchanged board must not be exported again")
	require.Zero(t, c.reads, "an unchanged board must not re-read tasks.json")
	status, err := f.svc.PendingExports(ctx, f.dir)
	require.NoError(t, err)
	require.Zero(t, status.Count, "the skipped pass must still retire its pending request")
}

// exportedWrite is one kind of write to exported content. setup runs and is
// published BEFORE the counters start, so the row's write alone must move the
// token: a row that also did its own setup writes would let a missing trigger
// hide behind another table's trigger (MTIX-130.20.2).
type exportedWrite struct {
	name  string
	setup func(t *testing.T, f *marshalFixture)
	write func(t *testing.T, f *marshalFixture)
}

func execWrite(t *testing.T, f *marshalFixture, query string, args ...any) {
	t.Helper()
	_, err := f.st.WriteDB().ExecContext(context.Background(), query, args...)
	require.NoError(t, err)
}

func addTokenNode(t *testing.T, f *marshalFixture, seq int) *model.Node {
	t.Helper()
	n := &model.Node{ID: "TEST-" + string(rune('0'+seq)), Project: "TEST", Seq: seq, Title: "n", Status: model.StatusOpen,
		Priority: model.PriorityMedium, Weight: 1, NodeType: model.NodeTypeIssue, CreatedAt: *f.now, UpdatedAt: *f.now}
	n.ContentHash = n.ComputeHash()
	require.NoError(t, f.st.CreateNode(context.Background(), n))
	return n
}

const (
	insertAgentSQL   = `INSERT INTO agents (agent_id, project, state, state_changed_at, last_heartbeat) VALUES ('a1','TEST','idle','2026-11-18T12:00:00Z','2026-11-18T12:00:00Z')`
	insertSessionSQL = `INSERT INTO sessions (id, agent_id, project, started_at, status) VALUES ('s1','a1','TEST','2026-11-18T12:00:00Z','active')`
)

func noSetup(*testing.T, *marshalFixture) {}

func withSecondNode(t *testing.T, f *marshalFixture) { addTokenNode(t, f, 2) }

func withAgent(t *testing.T, f *marshalFixture) { execWrite(t, f, insertAgentSQL) }

func withSession(t *testing.T, f *marshalFixture) {
	withAgent(t, f)
	execWrite(t, f, insertSessionSQL)
}

func exportedWriteKinds() []exportedWrite {
	ctx := context.Background()
	return []exportedWrite{
		{"node create", noSetup, func(t *testing.T, f *marshalFixture) { addTokenNode(t, f, 2) }},
		{"node title update", noSetup, func(t *testing.T, f *marshalFixture) { f.rename(t) }},
		{"node label update", noSetup, func(t *testing.T, f *marshalFixture) {
			execWrite(t, f, `UPDATE nodes SET labels = '["x"]' WHERE id = 'TEST-1'`)
		}},
		{"node annotation update", noSetup, func(t *testing.T, f *marshalFixture) {
			execWrite(t, f, `UPDATE nodes SET annotations = '[{"id":"a"}]' WHERE id = 'TEST-1'`)
		}},
		{"node activity update", noSetup, func(t *testing.T, f *marshalFixture) {
			execWrite(t, f, `UPDATE nodes SET activity = '[{"id":"a"}]' WHERE id = 'TEST-1'`)
		}},
		{"node delete", withSecondNode, func(t *testing.T, f *marshalFixture) {
			execWrite(t, f, `DELETE FROM nodes WHERE id = 'TEST-2'`)
		}},
		{"dependency add", withSecondNode, func(t *testing.T, f *marshalFixture) {
			require.NoError(t, f.st.AddDependency(ctx, &model.Dependency{FromID: "TEST-1", ToID: "TEST-2", DepType: model.DepTypeBlocks, CreatedAt: *f.now}))
		}},
		{"dependency delete", func(t *testing.T, f *marshalFixture) {
			withSecondNode(t, f)
			require.NoError(t, f.st.AddDependency(ctx, &model.Dependency{FromID: "TEST-1", ToID: "TEST-2", DepType: model.DepTypeBlocks, CreatedAt: *f.now}))
		}, func(t *testing.T, f *marshalFixture) {
			execWrite(t, f, `DELETE FROM dependencies WHERE from_id = 'TEST-1'`)
		}},
		{"agent insert", noSetup, func(t *testing.T, f *marshalFixture) { execWrite(t, f, insertAgentSQL) }},
		{"agent heartbeat", withAgent, func(t *testing.T, f *marshalFixture) {
			execWrite(t, f, `UPDATE agents SET last_heartbeat = '2026-11-18T13:00:00Z' WHERE agent_id = 'a1'`)
		}},
		{"agent delete", withAgent, func(t *testing.T, f *marshalFixture) {
			execWrite(t, f, `DELETE FROM agents WHERE agent_id = 'a1'`)
		}},
		{"session insert", withAgent, func(t *testing.T, f *marshalFixture) { execWrite(t, f, insertSessionSQL) }},
		{"session end", withSession, func(t *testing.T, f *marshalFixture) {
			execWrite(t, f, `UPDATE sessions SET status = 'ended', ended_at = '2026-11-18T13:00:00Z' WHERE id = 's1'`)
		}},
		{"session delete", withSession, func(t *testing.T, f *marshalFixture) {
			execWrite(t, f, `DELETE FROM sessions WHERE id = 's1'`)
		}},
		{"task history insert", func(t *testing.T, f *marshalFixture) {
			f.st.SetHistoryExportSource(func() bool { return true })
		}, func(t *testing.T, f *marshalFixture) {
			execWrite(t, f, `INSERT INTO task_history (entry_id, task_uid, ts, actor, origin, kind, field) VALUES ('h1','TEST-1','2026-11-18T12:00:00.000000000Z','a','local','change','title')`)
		}},
	}
}

// Every kind of write to exported content forces a full pass (MTIX-130.20.2).
func TestAutoExport_ExportedContentWrite_ForcesFullPass(t *testing.T) {
	for _, tc := range exportedWriteKinds() {
		t.Run(tc.name, func(t *testing.T) {
			f := newMarshalFixture(t)
			ctx := context.Background()
			require.NoError(t, f.svc.AutoExport(ctx, f.dir))
			tc.setup(t, f)
			require.NoError(t, f.svc.AutoExport(ctx, f.dir)) // publish the setup, so only the write is new
			c := countPassWork(f)
			require.NoError(t, f.svc.AutoExport(ctx, f.dir))
			require.Zero(t, c.exports, "the published setup must skip: the row's write is the only change")
			tc.write(t, f)
			require.NoError(t, f.svc.AutoExport(ctx, f.dir))
			require.Equal(t, 1, c.exports, "the write alone must force a full pass")
			// And the pass records the new state: the next tick skips again.
			require.NoError(t, f.svc.AutoExport(ctx, f.dir))
			require.Equal(t, 1, c.exports, "a recorded board must skip again")
		})
	}
}

// Writes to tables the export does not carry never force a pass.
func TestAutoExport_NonExportedWrite_DoesNotForceFullPass(t *testing.T) {
	f := newMarshalFixture(t)
	ctx := context.Background()
	require.NoError(t, f.svc.AutoExport(ctx, f.dir))
	c := countPassWork(f)
	execWrite(t, f, `INSERT OR REPLACE INTO meta (key, value) VALUES ('unrelated', 'x')`)
	execWrite(t, f, `INSERT OR REPLACE INTO sequences (key, value) VALUES ('unrelated', 7)`)
	require.NoError(t, f.svc.AutoExport(ctx, f.dir))
	require.Zero(t, c.exports)
}

// Anything that makes the last publication doubtful forces a full pass, which
// then records the board again (MTIX-130.20.2).
func TestAutoExport_DoubtfulPublication_FallsBackToFullPass(t *testing.T) {
	tasks := func(f *marshalFixture) string { return filepath.Join(f.dir, "tasks.json") }
	cases := []struct {
		name  string
		doubt func(t *testing.T, f *marshalFixture)
		// guarded: AutoExport's pulled-board guard (keepPulledBoard) acts before
		// the pass for this doubt, so only the skip decision itself is asserted.
		guarded bool
	}{
		{"record deleted", func(t *testing.T, f *marshalFixture) {
			execWrite(t, f, `DELETE FROM meta WHERE key = 'export.published_state'`)
		}, false},
		{"record malformed", func(t *testing.T, f *marshalFixture) {
			execWrite(t, f, `UPDATE meta SET value = '{broken' WHERE key = 'export.published_state'`)
		}, false},
		{"record unknown version", func(t *testing.T, f *marshalFixture) {
			execWrite(t, f, `UPDATE meta SET value = json_set(value, '$.v', 99) WHERE key = 'export.published_state'`)
		}, false},
		{"other build", func(t *testing.T, f *marshalFixture) { f.svc.binaryVersion = "other-build" }, false},
		{"tasks.json touched", func(t *testing.T, f *marshalFixture) {
			later := time.Now().Add(-time.Hour)
			require.NoError(t, os.Chtimes(tasks(f), later, later))
		}, false},
		{"tasks.json replaced by other bytes", func(t *testing.T, f *marshalFixture) {
			require.NoError(t, os.WriteFile(tasks(f), []byte("{}"), 0o644))
		}, true},
		{"tasks.json removed", func(t *testing.T, f *marshalFixture) { require.NoError(t, os.Remove(tasks(f))) }, false},
		{"file hash changed", func(t *testing.T, f *marshalFixture) {
			require.NoError(t, os.WriteFile(filepath.Join(f.dir, "data", "sync.sha256"), []byte("x"), 0o644))
		}, true},
		{"file hash removed", func(t *testing.T, f *marshalFixture) {
			require.NoError(t, os.Remove(filepath.Join(f.dir, "data", "sync.sha256")))
		}, true},
		{"db hash changed", func(t *testing.T, f *marshalFixture) {
			require.NoError(t, os.WriteFile(filepath.Join(f.dir, "data", "sync-db.sha256"), []byte("x"), 0o644))
		}, false},
		{"last export hash changed", func(t *testing.T, f *marshalFixture) {
			execWrite(t, f, `UPDATE meta SET value = 'x' WHERE key = 'last_export_hash'`)
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newMarshalFixture(t)
			ctx := context.Background()
			require.NoError(t, f.svc.AutoExport(ctx, f.dir))
			c := countPassWork(f)
			tc.doubt(t, f)
			token := f.svc.readExportToken(ctx)
			require.False(t, f.svc.unchangedSincePublication(ctx, f.dir, token), "doubt must never authorize a skip")
			if tc.guarded {
				return
			}
			require.NoError(t, f.svc.AutoExport(ctx, f.dir))
			require.Equal(t, 1, c.exports, "doubt must force exactly one full pass")
			require.NoError(t, f.svc.AutoExport(ctx, f.dir))
			require.Equal(t, 1, c.exports, "the full pass must record the board again")
			_, err := os.Stat(tasks(f))
			require.NoError(t, err, "a missing board file must be republished")
		})
	}
}

// A write that lands between the token read and the export must force the next
// pass: the token is read first, so the recorded one is never newer than the
// content (MTIX-130.20.2, the data-loss guard).
func TestAutoExport_WriteBetweenTokenReadAndExport_ForcesNextPass(t *testing.T) {
	f := newMarshalFixture(t)
	ctx := context.Background()
	require.NoError(t, f.svc.AutoExport(ctx, f.dir))
	c := countPassWork(f)
	inject := true
	inner := f.svc.exportBoardData
	f.svc.exportBoardData = func(ctx context.Context) (*sqlite.ExportData, error) {
		if inject {
			inject = false
			addTokenNode(t, f, 2) // lands after the token read, before the snapshot
		}
		return inner(ctx)
	}
	execWrite(t, f, `UPDATE nodes SET labels = '["y"]' WHERE id = 'TEST-1'`)
	require.NoError(t, f.svc.AutoExport(ctx, f.dir))
	require.Equal(t, 1, c.exports)
	require.NoError(t, f.svc.AutoExport(ctx, f.dir))
	require.Equal(t, 2, c.exports, "the in-between write must force another pass")
	require.NoError(t, f.svc.AutoExport(ctx, f.dir))
	require.Equal(t, 2, c.exports)
}

// The history.export switch changes export content without any row write.
func TestAutoExport_HistoryExportToggle_ForcesFullPass(t *testing.T) {
	f := newMarshalFixture(t)
	ctx := context.Background()
	require.NoError(t, f.svc.AutoExport(ctx, f.dir))
	c := countPassWork(f)
	f.st.SetHistoryExportSource(func() bool { return true })
	require.NoError(t, f.svc.AutoExport(ctx, f.dir))
	require.Equal(t, 1, c.exports)
}

// An explicit (non-automatic) export always writes, token or not.
func TestExportBoard_Explicit_IgnoresToken(t *testing.T) {
	f := newMarshalFixture(t)
	ctx := context.Background()
	require.NoError(t, f.svc.AutoExport(ctx, f.dir))
	c := countPassWork(f)
	require.NoError(t, f.svc.exportBoard(ctx, f.dir))
	require.Equal(t, 1, c.exports)
}

// Any doubt about tasks.json makes the pulled-board guard read and hash the
// file as before: only a record that proves the file untouched lets it skip
// the read (MTIX-130.20.2).
func TestAutoExport_GuardReadsBoardWhenRecordDoubtful(t *testing.T) {
	f := newMarshalFixture(t)
	ctx := context.Background()
	require.NoError(t, f.svc.AutoExport(ctx, f.dir))
	c := countPassWork(f)
	require.NoError(t, f.svc.AutoExport(ctx, f.dir))
	require.Zero(t, c.reads, "a proven-untouched board is not read by the guard either")
	later := time.Now().Add(-time.Hour)
	require.NoError(t, os.Chtimes(filepath.Join(f.dir, "tasks.json"), later, later))
	require.NoError(t, f.svc.AutoExport(ctx, f.dir))
	require.Positive(t, c.reads, "a changed modification time must make the guard read the file")
}

// A foreign tasks.json is still protected: the guard sees the changed file and
// the automatic export never overwrites it (MTIX-130.20.2).
func TestAutoExport_ForeignBoardIsKept_NotOverwritten(t *testing.T) {
	f := newMarshalFixture(t)
	ctx := context.Background()
	require.NoError(t, f.svc.AutoExport(ctx, f.dir))
	path := filepath.Join(f.dir, "tasks.json")
	f.svc.SetNoticeWriter(io.Discard)
	require.NoError(t, os.WriteFile(path, []byte(`{"foreign":true}`), 0o644))
	c := countPassWork(f)
	_ = f.svc.AutoExport(ctx, f.dir)
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, `{"foreign":true}`, string(got), "the foreign board must not be overwritten")
	require.Zero(t, c.exports, "a refused pass must not export")
	require.Positive(t, c.reads)
}

// An older database copied back over a newer one, with tasks.json left as the
// newer publication wrote it, is not trusted: the record it carries describes
// a file that no longer exists, so the pass is a full one (MTIX-130.20.2).
func TestAutoExport_RestoredOlderDatabase_ForcesFullPass(t *testing.T) {
	f := newMarshalFixture(t)
	ctx := context.Background()
	require.NoError(t, f.svc.AutoExport(ctx, f.dir))
	older := filepath.Join(t.TempDir(), "older.db")
	_, err := f.st.WriteDB().ExecContext(ctx, "VACUUM INTO ?", older)
	require.NoError(t, err)
	f.rename(t)
	require.NoError(t, f.svc.AutoExport(ctx, f.dir)) // tasks.json now holds the newer board
	newer, err := os.ReadFile(filepath.Join(f.dir, "tasks.json"))
	require.NoError(t, err)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	restored, err := sqlite.New(older, logger)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, restored.Close()) })
	svc := NewSyncService(restored, logger, func() time.Time { return *f.now })
	exports := 0
	svc.exportBoardData = func(ctx context.Context) (*sqlite.ExportData, error) {
		exports++
		return restored.Export(ctx, "", "")
	}
	require.NoError(t, svc.AutoExport(ctx, f.dir))
	require.Equal(t, 1, exports, "a restored database must not skip on a record about a newer file")
	after, err := os.ReadFile(filepath.Join(f.dir, "tasks.json"))
	require.NoError(t, err)
	require.NotEqual(t, newer, after, "the full pass publishes the restored board")
}
