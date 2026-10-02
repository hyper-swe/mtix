// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/require"
)

// bufferedStore opens a store whose injected logger writes to the buffer.
func bufferedStore(t *testing.T) (*Store, *sql.DB, *bytes.Buffer) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "prune.db")
	buf := &bytes.Buffer{}
	s, err := New(dbPath, slog.New(slog.NewTextHandler(buf, nil)))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	raw, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	return s, raw, buf
}

// captureVCDefaultLog routes slog.Default to a buffer for the test.
func captureVCDefaultLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

func setStoredVC(t *testing.T, raw *sql.DB, vc model.VectorClock) {
	t.Helper()
	b, err := vc.MarshalJSON()
	require.NoError(t, err)
	_, err = raw.Exec(`UPDATE meta SET value = ? WHERE key = 'meta.sync.vector_clock'`, string(b))
	require.NoError(t, err)
}

func counters(n int, v int64) model.VectorClock {
	vc := model.VectorClock{}
	for i := 0; i < n; i++ {
		vc[fmt.Sprintf("p%03d", i)] = v
	}
	return vc
}

func createNodes(t *testing.T, st *Store, base, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		require.NoError(t, st.CreateNode(context.Background(), &model.Node{
			ID: fmt.Sprintf("PROJ-%d", base+i), Project: "PROJ", Depth: 0, Seq: base + i,
			Title: "t", Status: model.StatusOpen, Priority: model.PriorityMedium,
			NodeType: model.NodeTypeAuto,
		}))
	}
}

func noticeRows(t *testing.T, raw *sql.DB) (first string, pending int) {
	t.Helper()
	require.NoError(t, raw.QueryRow(
		`SELECT COALESCE(MAX(value), '') FROM meta WHERE key = ?`, vcPrunedKey).Scan(&first))
	require.NoError(t, raw.QueryRow(
		`SELECT COUNT(*) FROM meta WHERE key = ?`, vcPrunePendingKey).Scan(&pending))
	return first, pending
}

// TestPrune_RealStoreEntryPoints_WarnsExactlyOnce: through the real Store
// paths only (the Store's own buffer logger, no slog.SetDefault), 150 applied
// authors then several writes, across two opens of the same file, give one
// Warn, one marker row stamped by the Store's clock, and no pending mark.
func TestPrune_RealStoreEntryPoints_WarnsExactlyOnce(t *testing.T) {
	s, raw, buf := bufferedStore(t)
	fixed := time.Date(2026, 10, 2, 8, 30, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return fixed })
	observeAuthorsViaApply(t, s, 150)
	createNodes(t, s, 1, 3)
	first, pending := noticeRows(t, raw)
	require.Equal(t, "2026-10-02T08:30:00Z", first, "stamped by the injected clock")
	require.Zero(t, pending)

	dbPath := s.dbPath
	require.NoError(t, s.Close())
	buf2 := &bytes.Buffer{}
	s2, err := New(dbPath, slog.New(slog.NewTextHandler(buf2, nil)))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s2.Close() })
	createNodes(t, s2, 10, 3)
	observeAuthorsViaApply(t, s2, 5)

	require.Equal(t, 1, bytes.Count(buf.Bytes(), []byte("vector clock pruned")), buf.String())
	require.NotContains(t, buf2.String(), "vector clock pruned")
	again, pending := noticeRows(t, raw)
	require.Equal(t, first, again)
	require.Zero(t, pending)
}

// TestPrune_WriteWithoutPrune_NeverMarksOrWarns: a clock within the cap
// leaves no pending mark, no marker row and no Warn.
func TestPrune_WriteWithoutPrune_NeverMarksOrWarns(t *testing.T) {
	s, raw, buf := bufferedStore(t)
	observeAuthorsViaApply(t, s, 50)
	createNodes(t, s, 1, 3)
	first, pending := noticeRows(t, raw)
	require.Empty(t, first)
	require.Zero(t, pending)
	require.NotContains(t, buf.String(), "vector clock pruned")
}

// TestPrune_PullOnlyPrune_RecordsAndWarns: a prune reached by the apply path
// alone (a pull with no local write) records the marker and warns.
func TestPrune_PullOnlyPrune_RecordsAndWarns(t *testing.T) {
	s, raw, buf := bufferedStore(t)
	observeAuthorsViaApply(t, s, 150)
	first, pending := noticeRows(t, raw)
	require.NotEmpty(t, first)
	require.Zero(t, pending)
	require.Equal(t, 1, bytes.Count(buf.Bytes(), []byte("vector clock pruned")), buf.String())
}

// TestLoadEmittingAuthors_RowMissing_SeedsDefaultAuthor: a replica without
// the emitting-authors row is seeded with its default author.
func TestLoadEmittingAuthors_RowMissing_SeedsDefaultAuthor(t *testing.T) {
	t.Setenv(AuthorIDEnv, "agent-env")
	s, _, _ := bufferedStore(t)
	require.NoError(t, s.WithTx(context.Background(), func(tx *sql.Tx) error {
		got, stored, err := loadEmittingAuthors(context.Background(), tx)
		require.NoError(t, err)
		require.Equal(t, []string{"agent-env"}, got)
		require.Empty(t, stored)
		return nil
	}))
}

// TestMerge_IncomingAuthorWithSmallestCounter_KeptExactly: the author of the
// applied event is kept with its real counter, the clock stays at the cap.
func TestMerge_IncomingAuthorWithSmallestCounter_KeptExactly(t *testing.T) {
	s, raw, _ := bufferedStore(t)
	setStoredVC(t, raw, counters(100, 50))
	e := makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "zz-new", 1, map[string]any{"title": "t"})
	require.NoError(t, applyOnce(t, s, e))
	vc := storedVC(t, raw)
	require.Len(t, vc, model.MaxVectorClockEntries)
	require.Equal(t, int64(1), vc["zz-new"])
}

// TestMerge_DefaultAuthorKeptWhenAbsentFromEmittingList: an upgraded replica
// whose emitting list lacks the default author still keeps that author.
func TestMerge_DefaultAuthorKeptWhenAbsentFromEmittingList(t *testing.T) {
	t.Setenv(AuthorIDEnv, "")
	s, raw, _ := bufferedStore(t)
	vc := counters(120, 50)
	vc[authorIDFallback] = 1
	setStoredVC(t, raw, vc)
	_, err := raw.Exec(`INSERT INTO meta (key, value) VALUES (?, '["someone-else"]')
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, emittingAuthorsKey)
	require.NoError(t, err)
	e := makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "other", 99, map[string]any{"title": "t"})
	require.NoError(t, applyOnce(t, s, e))
	got := storedVC(t, raw)
	require.Equal(t, int64(1), got[authorIDFallback], "default author kept despite the smallest counter")
	require.LessOrEqual(t, len(got), model.MaxVectorClockEntries)
}

func TestEmit_MalformedEmittingList_LoggedAndRebuilt(t *testing.T) {
	t.Setenv(AuthorIDEnv, "")
	buf := captureVCDefaultLog(t)
	s, raw, _ := bufferedStore(t)
	_, err := raw.Exec(`INSERT INTO meta (key, value) VALUES (?, 'not json')
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, emittingAuthorsKey)
	require.NoError(t, err)
	emitOne(t, s, raw, "agent-x")
	require.Contains(t, buf.String(), "emitting-authors list is malformed")
	var v string
	require.NoError(t, raw.QueryRow(`SELECT value FROM meta WHERE key = ?`, emittingAuthorsKey).Scan(&v))
	require.JSONEq(t, `["agent-x"]`, v)
}

// TestEmit_EmittingListSeededAndWrittenOnlyOnChange: the list is created by
// the first emit (authors absent from the clock are not recorded), and a
// repeat emit rewrites nothing.
func TestEmit_EmittingListSeededAndWrittenOnlyOnChange(t *testing.T) {
	t.Setenv(AuthorIDEnv, "")
	s, raw, _ := bufferedStore(t)
	_, err := raw.Exec(`CREATE TABLE emit_list_writes (n INTEGER);
		CREATE TRIGGER emit_list_upd AFTER UPDATE ON meta WHEN NEW.key = '` + emittingAuthorsKey + `'
		BEGIN INSERT INTO emit_list_writes VALUES (1); END;`)
	require.NoError(t, err)
	emitOne(t, s, raw, "agent-x")
	emitOne(t, s, raw, "agent-x")
	emitOne(t, s, raw, "agent-x")
	var v string
	require.NoError(t, raw.QueryRow(`SELECT value FROM meta WHERE key = ?`, emittingAuthorsKey).Scan(&v))
	require.JSONEq(t, `["agent-x"]`, v)
	var writes int
	require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM emit_list_writes`).Scan(&writes))
	require.Zero(t, writes, "after the first write the unchanged list is not rewritten")
}
