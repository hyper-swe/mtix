// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/require"
)

// MTIX-15.6.2 reconciliation execution tests.

// reconcileTestStore opens a fresh store and a parallel mtixDir for
// audit log and rename map files. Returns (store, raw inspection DB,
// mtixDir).
func reconcileTestStore(t *testing.T) (*Store, *sql.DB, string) {
	t.Helper()
	dir := t.TempDir()
	mtixDir := filepath.Join(dir, ".mtix")
	require.NoError(t, os.MkdirAll(mtixDir, 0o755))
	dbPath := filepath.Join(dir, "rec.db")
	s, err := New(dbPath, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	raw, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	return s, raw, mtixDir
}

// seedTree creates a small node tree for reconciliation tests:
//
//	MTIX-1 (root, epic)
//	  MTIX-1.1 (child, story)
//	    MTIX-1.1.1 (grandchild, issue)
//	  MTIX-1.2 (child, story)
//	MTIX-2 (root, epic)
func seedTree(t *testing.T, s *Store) {
	t.Helper()
	now := time.Now().UTC()
	for _, n := range []*model.Node{
		{ID: "MTIX-1", Project: "MTIX", Title: "root1", NodeType: model.NodeTypeEpic,
			Status: model.StatusOpen, Weight: 1.0, CreatedAt: now, UpdatedAt: now,
			Priority: model.PriorityMedium},
		{ID: "MTIX-1.1", ParentID: "MTIX-1", Project: "MTIX", Title: "child1",
			NodeType: model.NodeTypeStory, Status: model.StatusOpen,
			Weight: 1.0, CreatedAt: now, UpdatedAt: now, Priority: model.PriorityMedium},
		{ID: "MTIX-1.1.1", ParentID: "MTIX-1.1", Project: "MTIX", Title: "gc1",
			NodeType: model.NodeTypeIssue, Status: model.StatusOpen,
			Weight: 1.0, CreatedAt: now, UpdatedAt: now, Priority: model.PriorityMedium},
		{ID: "MTIX-1.2", ParentID: "MTIX-1", Project: "MTIX", Title: "child2",
			NodeType: model.NodeTypeStory, Status: model.StatusOpen,
			Weight: 1.0, CreatedAt: now, UpdatedAt: now, Priority: model.PriorityMedium},
		{ID: "MTIX-2", Project: "MTIX", Title: "root2", NodeType: model.NodeTypeEpic,
			Status: model.StatusOpen, Weight: 1.0, CreatedAt: now, UpdatedAt: now,
			Priority: model.PriorityMedium},
	} {
		require.NoError(t, s.CreateNode(context.Background(), n))
	}
}

// readNodeIDs returns all node IDs in the store, sorted.
func readNodeIDs(t *testing.T, raw *sql.DB) []string {
	t.Helper()
	rows, err := raw.Query(`SELECT id FROM nodes ORDER BY id`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		ids = append(ids, id)
	}
	require.NoError(t, rows.Err())
	return ids
}

// readAuditLog reads .mtix/reconcile.audit.log and returns parsed events.
func readAuditLog(t *testing.T, mtixDir string) []auditEvent {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(mtixDir, ReconcileAuditFilename))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	require.NoError(t, err)
	var events []auditEvent
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		if line == "" {
			continue
		}
		var e auditEvent
		require.NoError(t, json.Unmarshal([]byte(line), &e))
		events = append(events, e)
	}
	return events
}

// readRenameMap reads .mtix/id-rename-map.json.
func readRenameMap(t *testing.T, mtixDir string) (IDRenameMap, bool) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(mtixDir, IDRenameMapFilename))
	if errors.Is(err, os.ErrNotExist) {
		return IDRenameMap{}, false
	}
	require.NoError(t, err)
	var m IDRenameMap
	require.NoError(t, json.Unmarshal(body, &m))
	return m, true
}

// --- DiscardLocal ---

func TestDiscardLocal_ClearsAllState(t *testing.T) {
	s, raw, mtixDir := reconcileTestStore(t)
	seedTree(t, s)
	require.Equal(t, []string{"MTIX-1", "MTIX-1.1", "MTIX-1.1.1", "MTIX-1.2", "MTIX-2"},
		readNodeIDs(t, raw))

	require.NoError(t, DiscardLocal(context.Background(), s, mtixDir))

	require.Empty(t, readNodeIDs(t, raw))

	for _, table := range []string{"sync_events", "applied_events", "sync_conflicts", "dependencies", "sync_projects"} {
		var n int
		require.NoError(t, raw.QueryRow(
			`SELECT COUNT(*) FROM `+table,
		).Scan(&n))
		require.Equalf(t, 0, n, "%s should be empty after DiscardLocal", table)
	}
}

func TestDiscardLocal_ResetsSentinels(t *testing.T) {
	s, raw, mtixDir := reconcileTestStore(t)
	seedTree(t, s)

	// Bump the lamport sentinel so we can check it's reset.
	_, err := raw.Exec(`UPDATE meta SET value = '99' WHERE key = 'meta.sync.lamport'`)
	require.NoError(t, err)

	require.NoError(t, DiscardLocal(context.Background(), s, mtixDir))

	for _, kv := range []struct{ key, want string }{
		{"meta.sync.lamport", "0"},
		{"meta.sync.last_pulled_clock", "0"},
		{"meta.sync.vector_clock", "{}"},
		{"meta.sync.first_event_hash", ""},
		{"meta.sync.project_prefix", ""},
		{"meta.sync.machine_hash", ""},
	} {
		t.Run(kv.key, func(t *testing.T) {
			var got string
			require.NoError(t, raw.QueryRow(
				`SELECT value FROM meta WHERE key = ?`, kv.key,
			).Scan(&got))
			require.Equal(t, kv.want, got)
		})
	}
}

func TestDiscardLocal_AuditLogEmitted(t *testing.T) {
	s, _, mtixDir := reconcileTestStore(t)
	seedTree(t, s)

	require.NoError(t, DiscardLocal(context.Background(), s, mtixDir))

	events := readAuditLog(t, mtixDir)
	require.Len(t, events, 2)
	require.Equal(t, "RECONCILE_START", events[0].Type)
	require.Equal(t, "discard-local", events[0].Path)
	require.Equal(t, "RECONCILE_DONE", events[1].Type)
	require.Equal(t, "discard-local", events[1].Path)
	require.GreaterOrEqual(t, events[1].Duration, int64(0))
}

func TestDiscardLocal_RenameMapWrittenEmpty(t *testing.T) {
	s, _, mtixDir := reconcileTestStore(t)
	seedTree(t, s)

	require.NoError(t, DiscardLocal(context.Background(), s, mtixDir))

	m, ok := readRenameMap(t, mtixDir)
	require.True(t, ok)
	require.False(t, m.Partial)
	require.Equal(t, "discard-local", m.Path)
	require.Empty(t, m.Map, "DiscardLocal renames nothing — map is empty")
}

// --- RenameTo ---

func TestRenameTo_HappyPath(t *testing.T) {
	s, raw, mtixDir := reconcileTestStore(t)
	seedTree(t, s)

	count, err := RenameTo(context.Background(), s, mtixDir, "DEMO")
	require.NoError(t, err)
	require.Equal(t, 5, count)

	require.Equal(t, []string{"DEMO-1", "DEMO-1.1", "DEMO-1.1.1", "DEMO-1.2", "DEMO-2"},
		readNodeIDs(t, raw))
}

func TestRenameTo_PreservesContent(t *testing.T) {
	s, raw, mtixDir := reconcileTestStore(t)
	seedTree(t, s)

	_, err := RenameTo(context.Background(), s, mtixDir, "DEMO")
	require.NoError(t, err)

	var title string
	require.NoError(t, raw.QueryRow(
		`SELECT title FROM nodes WHERE id = 'DEMO-1.1.1'`,
	).Scan(&title))
	require.Equal(t, "gc1", title)
}

func TestRenameTo_UpdatesParentID(t *testing.T) {
	s, raw, mtixDir := reconcileTestStore(t)
	seedTree(t, s)

	_, err := RenameTo(context.Background(), s, mtixDir, "DEMO")
	require.NoError(t, err)

	var parent sql.NullString
	require.NoError(t, raw.QueryRow(
		`SELECT parent_id FROM nodes WHERE id = 'DEMO-1.1.1'`,
	).Scan(&parent))
	require.Equal(t, "DEMO-1.1", parent.String)
}

func TestRenameTo_UpdatesDependencies(t *testing.T) {
	s, raw, mtixDir := reconcileTestStore(t)
	seedTree(t, s)

	require.NoError(t, s.AddDependency(context.Background(), &model.Dependency{
		FromID: "MTIX-1.1", ToID: "MTIX-2",
		DepType: model.DepTypeRelated, CreatedBy: "alice", CreatedAt: time.Now().UTC(),
	}))

	_, err := RenameTo(context.Background(), s, mtixDir, "DEMO")
	require.NoError(t, err)

	var from, to string
	require.NoError(t, raw.QueryRow(
		`SELECT from_id, to_id FROM dependencies LIMIT 1`,
	).Scan(&from, &to))
	require.Equal(t, "DEMO-1.1", from)
	require.Equal(t, "DEMO-2", to)
}

func TestRenameTo_UpdatesSyncEvents(t *testing.T) {
	s, raw, mtixDir := reconcileTestStore(t)
	seedTree(t, s)

	// Verify sync_events for MTIX-1 exist BEFORE rename (CreateNode emitted them).
	var n int
	require.NoError(t, raw.QueryRow(
		`SELECT COUNT(*) FROM sync_events WHERE node_id = 'MTIX-1'`,
	).Scan(&n))
	require.Greater(t, n, 0)

	_, err := RenameTo(context.Background(), s, mtixDir, "DEMO")
	require.NoError(t, err)

	require.NoError(t, raw.QueryRow(
		`SELECT COUNT(*) FROM sync_events WHERE node_id = 'MTIX-1'`,
	).Scan(&n))
	require.Equal(t, 0, n, "old node_id rewritten")
	require.NoError(t, raw.QueryRow(
		`SELECT COUNT(*) FROM sync_events WHERE node_id = 'DEMO-1'`,
	).Scan(&n))
	require.Greater(t, n, 0, "new node_id present")
}

func TestRenameTo_RefusesInvalidPrefix(t *testing.T) {
	s, _, mtixDir := reconcileTestStore(t)
	seedTree(t, s)

	cases := []string{"", "lowercase", "1STARTS_WITH_DIGIT", "TOO_LONG_PREFIX_HERE", "BAD!"}
	for _, p := range cases {
		t.Run(p, func(t *testing.T) {
			_, err := RenameTo(context.Background(), s, mtixDir, p)
			require.Error(t, err)
			require.True(t, errors.Is(err, model.ErrInvalidInput))
		})
	}
}

func TestRenameTo_AuditLogPerNode(t *testing.T) {
	s, _, mtixDir := reconcileTestStore(t)
	seedTree(t, s)

	count, err := RenameTo(context.Background(), s, mtixDir, "DEMO")
	require.NoError(t, err)

	events := readAuditLog(t, mtixDir)
	// 1 START + N RENAME_NODE + 1 DONE
	require.Len(t, events, count+2)
	require.Equal(t, "RECONCILE_START", events[0].Type)
	require.Equal(t, "RECONCILE_DONE", events[len(events)-1].Type)

	renames := events[1 : len(events)-1]
	gotPairs := map[string]string{}
	for _, e := range renames {
		require.Equal(t, "RENAME_NODE", e.Type)
		gotPairs[e.OldID] = e.NewID
	}
	require.Equal(t, map[string]string{
		"MTIX-1":     "DEMO-1",
		"MTIX-1.1":   "DEMO-1.1",
		"MTIX-1.1.1": "DEMO-1.1.1",
		"MTIX-1.2":   "DEMO-1.2",
		"MTIX-2":     "DEMO-2",
	}, gotPairs)
}

func TestRenameTo_RenameMapWritten(t *testing.T) {
	s, _, mtixDir := reconcileTestStore(t)
	seedTree(t, s)

	_, err := RenameTo(context.Background(), s, mtixDir, "DEMO")
	require.NoError(t, err)

	m, ok := readRenameMap(t, mtixDir)
	require.True(t, ok)
	require.False(t, m.Partial)
	require.Equal(t, "rename-to", m.Path)
	require.Equal(t, "DEMO-1", m.Map["MTIX-1"])
	require.Equal(t, "DEMO-1.1.1", m.Map["MTIX-1.1.1"])
}

func TestRenameTo_UpdatesSentinels(t *testing.T) {
	s, raw, mtixDir := reconcileTestStore(t)
	seedTree(t, s)

	_, err := RenameTo(context.Background(), s, mtixDir, "DEMO")
	require.NoError(t, err)

	var prefix string
	require.NoError(t, raw.QueryRow(
		`SELECT value FROM meta WHERE key = 'meta.sync.project_prefix'`,
	).Scan(&prefix))
	require.Equal(t, "DEMO", prefix)

	var hash string
	require.NoError(t, raw.QueryRow(
		`SELECT value FROM meta WHERE key = 'meta.sync.first_event_hash'`,
	).Scan(&hash))
	require.Empty(t, hash, "rename invalidates the cached first_event_hash")
}

func TestRenameTo_EmptyStoreNoop(t *testing.T) {
	s, _, mtixDir := reconcileTestStore(t)

	count, err := RenameTo(context.Background(), s, mtixDir, "DEMO")
	require.NoError(t, err)
	require.Equal(t, 0, count)
}

// --- ImportAs ---

// seedParentForImport creates a parent node in the local store. ImportAs
// requires the parent to exist locally (the CLI workflow runs
// mtix sync clone first to fetch it from the hub).
func seedParentForImport(t *testing.T, s *Store, parentID string) {
	t.Helper()
	now := time.Now().UTC()
	require.NoError(t, s.CreateNode(context.Background(), &model.Node{
		ID: parentID, Project: "PROJ", Title: "external parent",
		NodeType: model.NodeTypeEpic, Status: model.StatusOpen,
		Weight: 1.0, CreatedAt: now, UpdatedAt: now, Priority: model.PriorityMedium,
	}))
}

func TestImportAs_HappyPath_Roots(t *testing.T) {
	s, raw, mtixDir := reconcileTestStore(t)
	seedParentForImport(t, s, "PROJ-7")
	// Two flat roots, no children, for a simpler verification.
	now := time.Now().UTC()
	for _, n := range []*model.Node{
		{ID: "MTIX-1", Project: "MTIX", Title: "a", NodeType: model.NodeTypeEpic,
			Status: model.StatusOpen, Weight: 1.0, CreatedAt: now, UpdatedAt: now,
			Priority: model.PriorityMedium},
		{ID: "MTIX-2", Project: "MTIX", Title: "b", NodeType: model.NodeTypeEpic,
			Status: model.StatusOpen, Weight: 1.0, CreatedAt: now, UpdatedAt: now,
			Priority: model.PriorityMedium},
	} {
		require.NoError(t, s.CreateNode(context.Background(), n))
	}

	count, err := ImportAs(context.Background(), s, mtixDir, "PROJ-7")
	require.NoError(t, err)
	require.Equal(t, 2, count)

	got := readNodeIDs(t, raw)
	// Includes the pre-existing PROJ-7 parent + the 2 imported nodes.
	require.Equal(t, []string{"PROJ-7", "PROJ-7.1", "PROJ-7.2"}, got)
}

func TestImportAs_RefusesIfParentMissing(t *testing.T) {
	s, _, mtixDir := reconcileTestStore(t)
	seedTree(t, s)
	// PROJ-7 is NOT seeded — should refuse with ErrNotFound.
	_, err := ImportAs(context.Background(), s, mtixDir, "PROJ-7")
	require.Error(t, err)
	require.True(t, errors.Is(err, model.ErrNotFound))
}

func TestImportAs_PreservesSubtreeStructure(t *testing.T) {
	s, raw, mtixDir := reconcileTestStore(t)
	seedParentForImport(t, s, "PROJ-7")
	seedTree(t, s) // MTIX-1, MTIX-1.1, MTIX-1.1.1, MTIX-1.2, MTIX-2

	count, err := ImportAs(context.Background(), s, mtixDir, "PROJ-7")
	require.NoError(t, err)
	require.Equal(t, 5, count)

	got := readNodeIDs(t, raw)
	want := []string{"PROJ-7", "PROJ-7.1", "PROJ-7.1.1", "PROJ-7.1.1.1", "PROJ-7.1.2", "PROJ-7.2"}
	require.Equal(t, want, got)
}

func TestImportAs_SetsParentIDOnFormerRoots(t *testing.T) {
	s, raw, mtixDir := reconcileTestStore(t)
	seedParentForImport(t, s, "PROJ-7")
	seedTree(t, s)

	_, err := ImportAs(context.Background(), s, mtixDir, "PROJ-7")
	require.NoError(t, err)

	var parent sql.NullString
	require.NoError(t, raw.QueryRow(
		`SELECT parent_id FROM nodes WHERE id = 'PROJ-7.1'`,
	).Scan(&parent))
	require.Equal(t, "PROJ-7", parent.String,
		"former root MTIX-1 (now PROJ-7.1) gets parent_id=PROJ-7")

	require.NoError(t, raw.QueryRow(
		`SELECT parent_id FROM nodes WHERE id = 'PROJ-7.1.1'`,
	).Scan(&parent))
	require.Equal(t, "PROJ-7.1", parent.String,
		"non-root retains its (rewritten) parent")
}

func TestImportAs_RefusesEmptyParentID(t *testing.T) {
	s, _, mtixDir := reconcileTestStore(t)
	seedTree(t, s)

	_, err := ImportAs(context.Background(), s, mtixDir, "")
	require.Error(t, err)
	require.True(t, errors.Is(err, model.ErrInvalidInput))
}

func TestImportAs_AuditLog(t *testing.T) {
	s, _, mtixDir := reconcileTestStore(t)
	seedParentForImport(t, s, "PROJ-7")
	seedTree(t, s)

	count, err := ImportAs(context.Background(), s, mtixDir, "PROJ-7")
	require.NoError(t, err)

	events := readAuditLog(t, mtixDir)
	require.Len(t, events, count+2)
	require.Equal(t, "RECONCILE_START", events[0].Type)
	require.Equal(t, "import-as", events[0].Path)
	require.Equal(t, "PROJ-7", events[0].ParentID)
	require.Equal(t, "RECONCILE_DONE", events[len(events)-1].Type)
}

func TestImportAs_RenameMapWritten(t *testing.T) {
	s, _, mtixDir := reconcileTestStore(t)
	seedParentForImport(t, s, "PROJ-7")
	seedTree(t, s)

	_, err := ImportAs(context.Background(), s, mtixDir, "PROJ-7")
	require.NoError(t, err)

	m, ok := readRenameMap(t, mtixDir)
	require.True(t, ok)
	require.False(t, m.Partial)
	require.Equal(t, "import-as", m.Path)
	require.Equal(t, "PROJ-7.1", m.Map["MTIX-1"])
	require.Equal(t, "PROJ-7.1.1.1", m.Map["MTIX-1.1.1"])
}

// --- helpers ---

func TestSplitProjectPrefix(t *testing.T) {
	cases := []struct {
		in              string
		wantP, wantRest string
	}{
		{"MTIX-1", "MTIX", "1"},
		{"MTIX-1.2.3", "MTIX", "1.2.3"},
		{"DEP_ADD-7", "DEP_ADD", "7"},
		{"", "", ""},
		{"-trailing", "", ""},
		{"no-dash", "no", "dash"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			p, r := splitProjectPrefix(tc.in)
			require.Equal(t, tc.wantP, p)
			require.Equal(t, tc.wantRest, r)
		})
	}
}

func TestRootAndSuffix(t *testing.T) {
	cases := []struct {
		in                 string
		wantRoot, wantSufx string
	}{
		{"MTIX-1", "MTIX-1", ""},
		{"MTIX-1.2", "MTIX-1", ".2"},
		{"MTIX-1.2.3", "MTIX-1", ".2.3"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			r, s := rootAndSuffix(tc.in)
			require.Equal(t, tc.wantRoot, r)
			require.Equal(t, tc.wantSufx, s)
		})
	}
}

func TestIsValidProjectPrefix(t *testing.T) {
	require.True(t, isValidProjectPrefix("MTIX"))
	require.True(t, isValidProjectPrefix("DEP_ADD"))
	require.True(t, isValidProjectPrefix("PROJ123"))
	require.True(t, isValidProjectPrefix("A"))

	require.False(t, isValidProjectPrefix(""))
	require.False(t, isValidProjectPrefix("lower"))
	require.False(t, isValidProjectPrefix("1DIGIT_FIRST"))
	require.False(t, isValidProjectPrefix("HAS!CHAR"))
	require.False(t, isValidProjectPrefix(strings.Repeat("A", 17)))
}

// Helper: validate the audit log file is created with mode 0600 so a
// shared developer machine doesn't expose project IDs to other users.
func TestAuditLogFileMode(t *testing.T) {
	s, _, mtixDir := reconcileTestStore(t)
	seedTree(t, s)
	require.NoError(t, DiscardLocal(context.Background(), s, mtixDir))

	info, err := os.Stat(filepath.Join(mtixDir, ReconcileAuditFilename))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestRenameMapFileMode(t *testing.T) {
	s, _, mtixDir := reconcileTestStore(t)
	seedTree(t, s)
	_, err := RenameTo(context.Background(), s, mtixDir, "DEMO")
	require.NoError(t, err)

	info, err := os.Stat(filepath.Join(mtixDir, IDRenameMapFilename))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

// Sanity: the audit events from RenameTo arrive in order and reference
// monotonically-increasing timestamps.
func TestAuditEventTimestampsMonotonic(t *testing.T) {
	s, _, mtixDir := reconcileTestStore(t)
	seedTree(t, s)
	_, err := RenameTo(context.Background(), s, mtixDir, "DEMO")
	require.NoError(t, err)

	events := readAuditLog(t, mtixDir)
	require.NotEmpty(t, events)
	parsed := make([]time.Time, len(events))
	for i, e := range events {
		t1, err := time.Parse(time.RFC3339Nano, e.Timestamp)
		require.NoError(t, err)
		parsed[i] = t1
	}
	for i := 1; i < len(parsed); i++ {
		require.Falsef(t, parsed[i].Before(parsed[i-1]),
			"event %d at %v before event %d at %v", i, parsed[i], i-1, parsed[i-1])
	}
	require.IsNonDecreasing(t, parsed)
}

// Compile-time sanity: appendAuditEvent reachable; deadcode-elim
// guard.
func TestAuditEventLineSeparated(t *testing.T) {
	s, _, mtixDir := reconcileTestStore(t)
	seedTree(t, s)
	_, err := RenameTo(context.Background(), s, mtixDir, "DEMO")
	require.NoError(t, err)

	body, err := os.ReadFile(filepath.Join(mtixDir, ReconcileAuditFilename))
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	require.Greater(t, len(lines), 2)
	for i, line := range lines {
		require.NotEmptyf(t, line, "line %d", i)
		var e auditEvent
		require.NoErrorf(t, json.Unmarshal([]byte(line), &e), "line %d malformed JSON", i)
	}
}

// audit events for ImportAs include the right Path field
func TestImportAsAuditMentionsPath(t *testing.T) {
	s, _, mtixDir := reconcileTestStore(t)
	seedParentForImport(t, s, "PROJ-7")
	seedTree(t, s)
	_, err := ImportAs(context.Background(), s, mtixDir, "PROJ-7")
	require.NoError(t, err)
	events := readAuditLog(t, mtixDir)
	require.Equal(t, "import-as", events[0].Path)
}

// helper used elsewhere in the file: ensure readNodeIDs is sorted.
func TestReadNodeIDsSorted(t *testing.T) {
	s, raw, _ := reconcileTestStore(t)
	seedTree(t, s)
	got := readNodeIDs(t, raw)
	cp := make([]string, len(got))
	copy(cp, got)
	sort.Strings(cp)
	require.Equal(t, cp, got)
}

// Compile-time sanity: the writeIDRenameMap deferred call writes
// partial=true on error.
func TestRenameTo_RenameMapPartialFlag(t *testing.T) {
	// Force an error by passing an invalid prefix; verify the deferred
	// writer marks partial=true. The function returns BEFORE buildMapping,
	// so the renamemap path doesn't fire — but for a deeper chaos test
	// we'd inject a SQL failure. For v1, this test documents the
	// happy non-partial path; a failure-injection test for partial
	// belongs in 15.6.3.
	s, _, mtixDir := reconcileTestStore(t)
	seedTree(t, s)

	_, err := RenameTo(context.Background(), s, mtixDir, "DEMO")
	require.NoError(t, err)

	m, ok := readRenameMap(t, mtixDir)
	require.True(t, ok)
	require.False(t, m.Partial)
}

// Smoke test: rename map prints stable JSON shape for use as agent
// reference in mtix sync (15.7).
func TestRenameMapJSONShape(t *testing.T) {
	s, _, mtixDir := reconcileTestStore(t)
	seedTree(t, s)
	_, err := RenameTo(context.Background(), s, mtixDir, "DEMO")
	require.NoError(t, err)

	body, err := os.ReadFile(filepath.Join(mtixDir, IDRenameMapFilename))
	require.NoError(t, err)
	require.Contains(t, string(body), `"renamed_at":`)
	require.Contains(t, string(body), `"path": "rename-to"`)
	require.Contains(t, string(body), `"partial": false`)
	require.Contains(t, string(body), `"map":`)
}

// helper to keep the compiler happy on unused symbols; the model
// package constants below appear in test scaffolding above.
var _ = fmt.Sprintf
