// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

func generation(t *testing.T, s *sqlite.Store) string {
	t.Helper()
	token, err := s.ExportChangeToken(context.Background())
	require.NoError(t, err)
	return token
}

func bumpExec(t *testing.T, s *sqlite.Store, q string, args ...any) {
	t.Helper()
	_, err := s.WriteDB().ExecContext(context.Background(), q, args...)
	require.NoError(t, err)
}

// Every INSERT, UPDATE and DELETE on every exported table moves the change
// token; writes to bookkeeping tables do not (MTIX-130.20.2).
func TestExportChangeToken_BumpsOnEveryExportedTableWrite(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 11, 19, 1, 0, 0, 0, time.UTC)
	require.NoError(t, s.CreateNode(context.Background(), makeNode("T-1", now)))
	require.NoError(t, s.CreateNode(context.Background(), makeNode("T-2", now)))
	steps := []struct{ name, sql string }{
		{"nodes update", `UPDATE nodes SET title = 'x' WHERE id = 'T-1'`},
		{"dependencies insert", `INSERT INTO dependencies (from_id, to_id, dep_type, created_at) VALUES ('T-1','T-2','blocks','2026-11-19T01:00:00Z')`},
		{"dependencies update", `UPDATE dependencies SET created_at = '2026-11-19T02:00:00Z' WHERE from_id = 'T-1'`},
		{"dependencies delete", `DELETE FROM dependencies WHERE from_id = 'T-1'`},
		{"agents insert", `INSERT INTO agents (agent_id, project, state, state_changed_at, last_heartbeat) VALUES ('a','T','idle','x','x')`},
		{"agents update", `UPDATE agents SET last_heartbeat = 'y' WHERE agent_id = 'a'`},
		{"sessions insert", `INSERT INTO sessions (id, agent_id, project, started_at, status) VALUES ('s','a','T','x','active')`},
		{"sessions update", `UPDATE sessions SET status = 'ended' WHERE id = 's'`},
		{"sessions delete", `DELETE FROM sessions WHERE id = 's'`},
		{"agents delete", `DELETE FROM agents WHERE agent_id = 'a'`},
		{"history insert", `INSERT INTO task_history (entry_id, task_uid, ts, actor, origin, kind, field) VALUES ('h','T-1','t','a','local','change','title')`},
		{"history update", `UPDATE task_history SET actor = 'b' WHERE entry_id = 'h'`},
		{"history delete", `DELETE FROM task_history WHERE entry_id = 'h'`},
		{"nodes delete", `DELETE FROM nodes WHERE id = 'T-2'`},
	}
	for _, st := range steps {
		before := generation(t, s)
		bumpExec(t, s, st.sql)
		require.NotEqual(t, before, generation(t, s), st.name)
	}
	before := generation(t, s)
	bumpExec(t, s, `INSERT OR REPLACE INTO meta (key, value) VALUES ('unrelated', 'x')`)
	bumpExec(t, s, `INSERT OR REPLACE INTO sequences (key, value) VALUES ('unrelated', 3)`)
	require.Equal(t, before, generation(t, s), "bookkeeping tables are not exported content")
}

// A rolled-back write never moves the token.
func TestExportChangeToken_RolledBackWrite_DoesNotBump(t *testing.T) {
	s := newTestStore(t)
	require.NoError(t, s.CreateNode(context.Background(), makeNode("T-1", time.Now())))
	before := generation(t, s)
	tx, err := s.WriteDB().BeginTx(context.Background(), nil)
	require.NoError(t, err)
	_, err = tx.ExecContext(context.Background(), `UPDATE nodes SET title = 'gone' WHERE id = 'T-1'`)
	require.NoError(t, err)
	require.NoError(t, tx.Rollback())
	require.Equal(t, before, generation(t, s))
}

func TestExportChangeToken_HistorySwitchIsPartOfToken(t *testing.T) {
	s := newTestStore(t)
	off := generation(t, s)
	s.SetHistoryExportSource(func() bool { return true })
	require.NotEqual(t, off, generation(t, s))
}

func TestPublishedExport_RoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_, ok, err := s.ReadPublishedExport(ctx)
	require.NoError(t, err)
	require.False(t, ok)
	rec := sqlite.PublishedExport{Token: "1|h0|r1", Build: "b", FileHash: "f", DBHash: "d", Size: 7, MtimeNS: 9}
	require.NoError(t, s.WritePublishedExport(ctx, rec))
	got, ok, err := s.ReadPublishedExport(ctx)
	require.NoError(t, err)
	require.True(t, ok)
	rec.Version = sqlite.PublishedExportVersion
	require.Equal(t, rec, got)
}

// Every import entry point invalidates the last-publication record: replace,
// merge and the uid-reconciling import (MTIX-130.20.2).
func TestPublishedExport_EveryImportKindInvalidates(t *testing.T) {
	kinds := []struct {
		name string
		run  func(t *testing.T, s *sqlite.Store, data *sqlite.ExportData)
	}{
		{"replace", func(t *testing.T, s *sqlite.Store, data *sqlite.ExportData) {
			_, err := s.Import(context.Background(), data, sqlite.ImportModeReplace, true)
			require.NoError(t, err)
		}},
		{"merge", func(t *testing.T, s *sqlite.Store, data *sqlite.ExportData) {
			_, err := s.Import(context.Background(), data, sqlite.ImportModeMerge, false)
			require.NoError(t, err)
		}},
		{"reconcile merge", func(t *testing.T, s *sqlite.Store, data *sqlite.ExportData) {
			_, _, err := s.ImportReconcile(context.Background(), data, sqlite.ImportReconcileOptions{Mode: sqlite.ImportModeMerge})
			require.NoError(t, err)
		}},
		{"reconcile replace", func(t *testing.T, s *sqlite.Store, data *sqlite.ExportData) {
			_, _, err := s.ImportReconcile(context.Background(), data, sqlite.ImportReconcileOptions{Mode: sqlite.ImportModeReplace, Force: true})
			require.NoError(t, err)
		}},
	}
	for _, k := range kinds {
		t.Run(k.name, func(t *testing.T) {
			s := newTestStore(t)
			ctx := context.Background()
			require.NoError(t, s.CreateNode(ctx, makeNode("T-1", time.Now())))
			data, err := s.Export(ctx, "T", "")
			require.NoError(t, err)
			require.NoError(t, s.WritePublishedExport(ctx, sqlite.PublishedExport{Token: "t", Build: "b", FileHash: "f", DBHash: "d"}))
			k.run(t, s, data)
			_, ok, err := s.ReadPublishedExport(ctx)
			require.NoError(t, err)
			require.False(t, ok, "an import must invalidate the last-publication record")
		})
	}
}

func makeNode(id string, now time.Time) *model.Node {
	n := &model.Node{ID: id, Project: "T", Seq: int(id[len(id)-1] - '0'), Title: "n", Status: model.StatusOpen,
		Priority: model.PriorityMedium, Weight: 1, NodeType: model.NodeTypeIssue, CreatedAt: now, UpdatedAt: now}
	n.ContentHash = n.ComputeHash()
	return n
}
