// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Property fixtures retain real opens and verify empty-image equivalence and ownership.
package sqlite

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/require"
)

// Only completed immutable backup bytes survive the builder's owned source.
type emptyStoreImage string

func buildEmptyStoreImage(t *testing.T) emptyStoreImage {
	t.Helper()
	dir := t.TempDir()
	s, err := New(filepath.Join(dir, "source.db"), slog.Default())
	require.NoError(t, err)
	closed := false
	t.Cleanup(func() {
		if !closed {
			require.NoError(t, s.Close())
		}
	})
	path := filepath.Join(dir, "empty.db")
	result, err := s.Backup(t.Context(), path)
	require.NoError(t, err)
	require.True(t, result.Verified)
	require.Positive(t, result.Size)
	require.NoError(t, s.Close())
	closed = true
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, result.Size, int64(len(raw)))
	return emptyStoreImage(raw)
}

func (image emptyStoreImage) open(t *testing.T, filename string) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), filename)
	require.NoError(t, os.WriteFile(path, []byte(image), 0o600))
	return openImageFixture(t, path)
}

func openImageFixture(t *testing.T, path string) *Store {
	t.Helper()
	s, err := New(path, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	return s
}

func imageFixtureQueries() map[string]string {
	return map[string]string{
		"schema":               `SELECT type,name,tbl_name,sql FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' ORDER BY type,name`,
		"meta":                 `SELECT key,value FROM meta ORDER BY key`,
		"nodes":                `SELECT * FROM nodes ORDER BY id`,
		"dependencies":         `SELECT * FROM dependencies ORDER BY from_id,to_id,dep_type`,
		"cascade_deletes":      `SELECT * FROM cascade_deletes ORDER BY node_id`,
		"sync_events":          `SELECT * FROM sync_events ORDER BY event_id`,
		"applied_events":       `SELECT * FROM applied_events ORDER BY event_id`,
		"sync_sweep_pending":   `SELECT * FROM sync_sweep_pending ORDER BY event_id`,
		"agent_inbox_cursor":   `SELECT * FROM agent_inbox_cursor ORDER BY agent_id`,
		"agent_inbox_ack":      `SELECT * FROM agent_inbox_ack ORDER BY agent_id,event_seq`,
		"hook_dispatch_cursor": `SELECT * FROM hook_dispatch_cursor ORDER BY rowid`,
		"hook_dispatch_ledger": `SELECT * FROM hook_dispatch_ledger ORDER BY rowid`,
		"relay_push_cursor":    `SELECT * FROM relay_push_cursor ORDER BY rowid`,
		"relay_ingest_cursor":  `SELECT * FROM relay_ingest_cursor ORDER BY rowid`,
		"hook_synced_cursor":   `SELECT * FROM hook_synced_cursor ORDER BY rowid`,
		"inbox_deliveries":     `SELECT * FROM inbox_deliveries ORDER BY rowid`,
		"hook_log":             `SELECT * FROM hook_log ORDER BY rowid`,
		"hook_event_origin":    `SELECT * FROM hook_event_origin ORDER BY event_id`,
		"sync_projects":        `SELECT * FROM sync_projects ORDER BY rowid`,
		"sync_conflicts":       `SELECT * FROM sync_conflicts ORDER BY rowid`,
		"agents":               `SELECT * FROM agents ORDER BY agent_id`,
		"sessions":             `SELECT * FROM sessions ORDER BY id`,
		"sequences":            `SELECT * FROM sequences ORDER BY rowid`,
		"sync_quarantine":      `SELECT * FROM sync_quarantine ORDER BY event_id`,
		"nodes_fts":            `SELECT * FROM nodes_fts ORDER BY rowid`,
		"nodes_fts_data":       `SELECT * FROM nodes_fts_data ORDER BY id`,
		"nodes_fts_idx":        `SELECT * FROM nodes_fts_idx ORDER BY segid,term`,
		"nodes_fts_docsize":    `SELECT * FROM nodes_fts_docsize ORDER BY id`,
		"nodes_fts_config":     `SELECT * FROM nodes_fts_config ORDER BY k`,
	}
}

func imageFixtureState(t *testing.T, s *Store) map[string][][]any {
	t.Helper()
	state := map[string][][]any{}
	for name, query := range imageFixtureQueries() {
		state[name] = payloadRows(t, s.readDB, query)
	}
	return state
}

func imageFixturePragmas(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	values := map[string]string{}
	for name, query := range map[string]string{
		"journal_mode": "PRAGMA journal_mode", "synchronous": "PRAGMA synchronous",
		"foreign_keys": "PRAGMA foreign_keys", "wal_autocheckpoint": "PRAGMA wal_autocheckpoint",
		"busy_timeout": "PRAGMA busy_timeout", "quick_check": "PRAGMA quick_check",
	} {
		var value string
		require.NoError(t, db.QueryRowContext(t.Context(), query).Scan(&value))
		values[name] = value
	}
	return values
}

func requireEmptyImageFixture(t *testing.T, s *Store) {
	t.Helper()
	state := imageFixtureState(t, s)
	for name, rows := range state {
		if name != "schema" && name != "meta" && name != "nodes_fts_data" && name != "nodes_fts_config" {
			require.Empty(t, rows, name)
		}
	}
	require.Equal(t, "4", imageFixtureMeta(t, s, "schema_version"))
	require.Equal(t, "0", imageFixtureMeta(t, s, "meta.sync.lamport"))
	require.Equal(t, "{}", imageFixtureMeta(t, s, "meta.sync.vector_clock"))
	require.Empty(t, imageFixtureMeta(t, s, "meta.sync.machine_hash"))
	require.Empty(t, imageFixtureMeta(t, s, "meta.sync.author_id"))
	require.Equal(t, [][]any{{"version", int64(4)}}, state["nodes_fts_config"])
	result, err := s.Verify(t.Context())
	require.NoError(t, err)
	require.True(t, result.AllPassed, "%v", result.Errors)
}

func imageFixtureMeta(t *testing.T, s *Store, key string) string {
	t.Helper()
	var value string
	require.NoError(t, s.readDB.QueryRowContext(t.Context(), `SELECT value FROM meta WHERE key = ?`, key).Scan(&value))
	return value
}

func TestEmptyStoreImage_MatchesFreshSchemaAndDefaults(t *testing.T) {
	image := buildEmptyStoreImage(t)
	fresh := openImageFixture(t, filepath.Join(t.TempDir(), "fresh.db"))
	requireEmptyImageFixture(t, fresh)
	wantState := imageFixtureState(t, fresh)
	wantPragmas := map[string]string{"journal_mode": "wal", "synchronous": "2", "foreign_keys": "1",
		"wal_autocheckpoint": "1000", "busy_timeout": "5000", "quick_check": "ok"}
	for _, filename := range []string{"a.db", "b.db"} {
		s := image.open(t, filename)
		requireEmptyImageFixture(t, s)
		require.Equal(t, wantState, imageFixtureState(t, s))
		require.Equal(t, wantPragmas, imageFixturePragmas(t, s.writeDB))
		require.Equal(t, wantPragmas, imageFixturePragmas(t, s.readDB))
	}
}

func mutateImageFixture(t *testing.T, s *Store, title, author string, at time.Time) *model.Node {
	t.Helper()
	seq, err := s.NextSequence(t.Context(), "IMG:")
	require.NoError(t, err)
	require.Equal(t, 1, seq)
	n := makeTestNode("IMG-1", "", "IMG", title, 0, seq, at)
	n.Creator = author
	require.NoError(t, s.CreateNode(t.Context(), n))
	require.NoError(t, s.ClaimNode(t.Context(), n.ID, author))
	stored, err := s.GetNode(t.Context(), n.ID)
	require.NoError(t, err)
	require.Equal(t, title, stored.Title)
	require.Equal(t, author, stored.Assignee)
	require.Equal(t, at, stored.UpdatedAt)
	events, err := s.ReadPendingEvents(t.Context(), 10)
	require.NoError(t, err)
	require.Len(t, events, 2)
	require.Equal(t, model.OpCreateNode, events[0].OpType)
	require.Equal(t, model.OpClaim, events[1].OpType)
	for i, event := range events {
		require.Equal(t, author, event.AuthorID)
		require.Equal(t, int64(i+1), event.LamportClock)
		require.Equal(t, int64(i+1), event.VectorClock[author])
		require.Equal(t, stored.UID, event.UID)
	}
	return stored
}

func requireImageFilesOwned(t *testing.T, a, b *Store) {
	t.Helper()
	require.NotSame(t, a, b)
	require.NotSame(t, a.writeDB, b.writeDB)
	require.NotSame(t, a.readDB, b.readDB)
	require.NotEqual(t, filepath.Dir(a.dbPath), filepath.Dir(b.dbPath))
	for _, suffix := range []string{"", "-wal", "-shm"} {
		require.NotEqual(t, a.dbPath+suffix, b.dbPath+suffix)
		for _, s := range []*Store{a, b} {
			_, err := os.Stat(s.dbPath + suffix)
			require.NoError(t, err)
		}
	}
}

func requireImageReopen(t *testing.T, original *Store) {
	t.Helper()
	before := imageFixtureState(t, original)
	reopened := openImageFixture(t, original.dbPath)
	require.Equal(t, before, imageFixtureState(t, reopened))
	require.Empty(t, reopened.onCommit)
	require.Nil(t, reopened.encodePayloadFn)
	require.NotEqual(t, original.clock(), reopened.clock())
	result, err := reopened.Verify(context.Background())
	require.NoError(t, err)
	require.True(t, result.AllPassed, "%v", result.Errors)
}

func TestEmptyStoreImage_CopiesOwnDurableStateAndCallbacks(t *testing.T) {
	image := buildEmptyStoreImage(t)
	a, b := image.open(t, "a.db"), image.open(t, "b.db")
	emptyB := imageFixtureState(t, b)
	atA := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	atB := atA.Add(24 * time.Hour)
	a.SetClock(func() time.Time { return atA })
	b.SetClock(func() time.Time { return atB })
	callsA, callsB := 0, 0
	a.AddOnCommit(func() { callsA++ })
	b.AddOnCommit(func() { callsB++ })
	nodeA := mutateImageFixture(t, a, "copy A", "image-a", atA)
	require.Equal(t, 2, callsA)
	require.Zero(t, callsB)
	require.Equal(t, emptyB, imageFixtureState(t, b))
	require.NotEmpty(t, imageFixtureMeta(t, a, "meta.sync.machine_hash"))
	require.Empty(t, imageFixtureMeta(t, b, "meta.sync.machine_hash"))
	stateA := imageFixtureState(t, a)
	nodeB := mutateImageFixture(t, b, "copy B", "image-b", atB)
	require.NotEqual(t, nodeA.UID, nodeB.UID)
	require.Equal(t, 2, callsA)
	require.Equal(t, 2, callsB)
	require.Equal(t, stateA, imageFixtureState(t, a))
	require.Equal(t, atA, a.clock())
	require.Equal(t, atB, b.clock())
	requireImageFilesOwned(t, a, b)
	requireImageReopen(t, a)
	requireImageReopen(t, b)
	stateB := imageFixtureState(t, b)
	require.NoError(t, a.UnclaimNode(t.Context(), nodeA.ID, "fixture release", "image-a"))
	require.Equal(t, 3, callsA)
	require.Equal(t, 2, callsB)
	require.Equal(t, stateB, imageFixtureState(t, b))
}
