// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// The conflict baseline mtix 0.5.3 wrote (MTIX-95.31.11, FR-15.2h), rebuilt
// the way 0.5.3 computed it, independently of the code under test: the
// export types, the node query and the checksum below are copied from
// v0.5.3-beta (internal/store/sqlite/export.go), and v053BaselineHash is its
// computeDBHash (internal/service/sync_service.go). 0.5.3 exported the store
// in the 1.0.0 form, cleared exported_at, and wrote the SHA-256 of the JSON
// encoding of the whole export to .mtix/data/sync-db.sha256.
package service_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// v053ExportData is ExportData of mtix 0.5.3, field for field.
type v053ExportData struct {
	Version       int                 `json:"version"`
	SchemaVersion string              `json:"schema_version"`
	ExportedAt    string              `json:"exported_at"`
	MtixVersion   string              `json:"mtix_version"`
	Project       string              `json:"project"`
	Nodes         []v053ExportNode    `json:"nodes"`
	Dependencies  []v053ExportDep     `json:"dependencies"`
	Agents        []v053ExportAgent   `json:"agents"`
	Sessions      []v053ExportSession `json:"sessions"`
	NodeCount     int                 `json:"node_count"`
	Checksum      string              `json:"checksum"`
}

// v053ExportNode is exportNode of mtix 0.5.3 (schema 1.0.0), field for field.
type v053ExportNode struct {
	ID          string  `json:"id"`
	ParentID    string  `json:"parent_id"`
	Depth       int     `json:"depth"`
	Seq         int     `json:"seq"`
	Project     string  `json:"project"`
	Title       string  `json:"title"`
	Description string  `json:"description"`
	Prompt      string  `json:"prompt"`
	Acceptance  string  `json:"acceptance"`
	NodeType    string  `json:"node_type"`
	IssueType   string  `json:"issue_type"`
	Priority    int     `json:"priority"`
	Labels      string  `json:"labels"`
	Status      string  `json:"status"`
	Progress    float64 `json:"progress"`
	Assignee    string  `json:"assignee"`
	Creator     string  `json:"creator"`
	AgentState  string  `json:"agent_state"`
	Weight      float64 `json:"weight"`
	ContentHash string  `json:"content_hash"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
	ClosedAt    string  `json:"closed_at,omitempty"`
	DeferUntil  string  `json:"defer_until,omitempty"`
	DeletedAt   string  `json:"deleted_at,omitempty"`
	UID         string  `json:"uid,omitempty"`
}

// v053ExportDep is exportDep of mtix 0.5.3.
type v053ExportDep struct {
	FromID    string `json:"from_id"`
	ToID      string `json:"to_id"`
	DepType   string `json:"dep_type"`
	CreatedAt string `json:"created_at"`
}

// v053ExportAgent is exportAgent of mtix 0.5.3.
type v053ExportAgent struct {
	AgentID       string `json:"agent_id"`
	Project       string `json:"project"`
	State         string `json:"state"`
	CurrentNodeID string `json:"current_node_id,omitempty"`
	LastHeartbeat string `json:"last_heartbeat,omitempty"`
}

// v053ExportSession is exportSession of mtix 0.5.3.
type v053ExportSession struct {
	ID        string `json:"id"`
	AgentID   string `json:"agent_id"`
	Project   string `json:"project"`
	StartedAt string `json:"started_at"`
	EndedAt   string `json:"ended_at,omitempty"`
	Status    string `json:"status"`
	Summary   string `json:"summary,omitempty"`
}

// v053NodeSelectSQL is exportNodeSelectSQL of mtix 0.5.3: the 1.0.0 columns.
const v053NodeSelectSQL = `SELECT id, COALESCE(parent_id,''), depth, seq, project,
		        title, COALESCE(description,''), COALESCE(prompt,''),
		        COALESCE(acceptance,''), COALESCE(node_type,'auto'),
		        COALESCE(issue_type,''), priority, COALESCE(labels,'[]'),
		        status, progress, COALESCE(assignee,''), COALESCE(creator,''),
		        COALESCE(agent_state,''), weight, COALESCE(content_hash,''),
		        created_at, updated_at, COALESCE(closed_at,''),
		        COALESCE(defer_until,''), COALESCE(deleted_at,''),
		        COALESCE(uid,'')
		 FROM nodes ORDER BY id`

// v053BaselineHash returns the conflict baseline mtix 0.5.3 wrote for st:
// computeDBHash of v0.5.3-beta, over the store read with 0.5.3's queries.
func v053BaselineHash(t *testing.T, st *sqlite.Store) string {
	t.Helper()
	data := v053Export(t, st.ReadDB())
	data.ExportedAt = "" // 0.5.3 zeroed the export time before hashing
	raw, err := json.Marshal(data)
	require.NoError(t, err)
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

// v053Export is Store.Export of mtix 0.5.3 with project and mtix version
// empty, as computeDBHash called it.
func v053Export(t *testing.T, db *sql.DB) *v053ExportData {
	t.Helper()
	nodes := v053Nodes(t, db)
	deps := v053Deps(t, db)
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	sort.Slice(deps, func(i, j int) bool {
		if deps[i].FromID != deps[j].FromID {
			return deps[i].FromID < deps[j].FromID
		}
		return deps[i].ToID < deps[j].ToID
	})
	// computeExportChecksum of 0.5.3: the SHA-256 of the first JSON encoding
	// of the sorted nodes and dependencies.
	canonical, err := json.Marshal(struct {
		Nodes []v053ExportNode `json:"nodes"`
		Deps  []v053ExportDep  `json:"deps"`
	}{Nodes: nodes, Deps: deps})
	require.NoError(t, err)
	return &v053ExportData{
		Version: 1, SchemaVersion: "1.0.0", ExportedAt: "2026-09-03T08:00:00Z",
		Nodes: nodes, Dependencies: deps, Agents: v053Agents(t, db), Sessions: v053Sessions(t, db),
		NodeCount: len(nodes), Checksum: fmt.Sprintf("%x", sha256.Sum256(canonical)),
	}
}

// v053Nodes reads the nodes as mtix 0.5.3 exported them, node_type
// normalized to the depth's type.
func v053Nodes(t *testing.T, db *sql.DB) []v053ExportNode {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), v053NodeSelectSQL)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	var nodes []v053ExportNode
	for rows.Next() {
		var n v053ExportNode
		require.NoError(t, rows.Scan(
			&n.ID, &n.ParentID, &n.Depth, &n.Seq, &n.Project,
			&n.Title, &n.Description, &n.Prompt, &n.Acceptance, &n.NodeType,
			&n.IssueType, &n.Priority, &n.Labels, &n.Status, &n.Progress,
			&n.Assignee, &n.Creator, &n.AgentState, &n.Weight, &n.ContentHash,
			&n.CreatedAt, &n.UpdatedAt, &n.ClosedAt, &n.DeferUntil, &n.DeletedAt,
			&n.UID,
		))
		n.NodeType = string(model.NodeTypeForDepth(n.Depth))
		nodes = append(nodes, n)
	}
	require.NoError(t, rows.Err())
	return nodes
}

// v053Deps reads the dependencies as mtix 0.5.3 exported them.
func v053Deps(t *testing.T, db *sql.DB) []v053ExportDep {
	t.Helper()
	// Every dependency, in the export's order.
	rows, err := db.QueryContext(context.Background(),
		`SELECT from_id, to_id, dep_type, created_at FROM dependencies ORDER BY from_id, to_id`)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	var deps []v053ExportDep
	for rows.Next() {
		var d v053ExportDep
		require.NoError(t, rows.Scan(&d.FromID, &d.ToID, &d.DepType, &d.CreatedAt))
		deps = append(deps, d)
	}
	require.NoError(t, rows.Err())
	return deps
}

// v053Agents reads the agents as mtix 0.5.3 exported them.
func v053Agents(t *testing.T, db *sql.DB) []v053ExportAgent {
	t.Helper()
	// Every agent, in the export's order.
	rows, err := db.QueryContext(context.Background(),
		`SELECT agent_id, project, COALESCE(state,'idle'),
		        COALESCE(current_node_id,''), COALESCE(last_heartbeat,'')
		 FROM agents ORDER BY agent_id`)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	var agents []v053ExportAgent
	for rows.Next() {
		var a v053ExportAgent
		require.NoError(t, rows.Scan(&a.AgentID, &a.Project, &a.State, &a.CurrentNodeID, &a.LastHeartbeat))
		agents = append(agents, a)
	}
	require.NoError(t, rows.Err())
	return agents
}

// v053Sessions reads the sessions as mtix 0.5.3 exported them.
func v053Sessions(t *testing.T, db *sql.DB) []v053ExportSession {
	t.Helper()
	// Every session, in the export's order.
	rows, err := db.QueryContext(context.Background(),
		`SELECT id, agent_id, project, started_at, COALESCE(ended_at,''),
		        COALESCE(status,'active'), COALESCE(summary,'')
		 FROM sessions ORDER BY id`)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	var sessions []v053ExportSession
	for rows.Next() {
		var s v053ExportSession
		require.NoError(t, rows.Scan(&s.ID, &s.AgentID, &s.Project, &s.StartedAt, &s.EndedAt, &s.Status, &s.Summary))
		sessions = append(sessions, s)
	}
	require.NoError(t, rows.Err())
	return sessions
}

// upgradeSetup varies the store a user upgrades from 0.5.3
// (newUpgradeFixture).
type upgradeSetup struct {
	empty       bool // the store holds no tasks
	invalidUTF8 bool // PROJ-2's description holds bytes that are not valid UTF-8
	uidless     bool // PROJ-2 has no uid, as a task an older mtix imported from a board without uids
}

// The fixed uids of the fixture's tasks (UUIDv7, as a create mints).
const (
	upgradeUID1 = "01995a1e-8c00-7a11-8b22-000000000001"
	upgradeUID2 = "01995a1e-8c00-7a11-8b22-000000000002"
)

// The conflict baselines 0.5.3 wrote for the fixture (v053BaselineHash),
// pinned: the fixture as newUpgradeFixture builds it, and with PROJ-2's
// description holding invalid UTF-8.
const (
	v053FixtureBaseline            = "abd66aaa6c1155d243e8b5a7650ac7cf3205b32657aaa9db4903f0b1252c608d"
	v053FixtureBaselineInvalidUTF8 = "2ee1dfb6840271b69686910799626e94cbe37c06d2f8b882c0b44aeb182e7280"
)

// newUpgradeFixture builds the store a user had on 0.5.3, every value
// fixed: PROJ-1 (labels, assignee, two annotations) blocks PROJ-2, then
// exports it, as the last command before the upgrade did. The annotations
// are a 2.0.0 field: the 1.0.0 export 0.5.3 hashed lacked them.
func newUpgradeFixture(t *testing.T, setup upgradeSetup) *guardFixture {
	t.Helper()
	ctx := context.Background()
	f := newProjectFixture(t)
	if !setup.empty {
		created := time.Date(2026, 9, 3, 8, 0, 0, 0, time.UTC)
		for i, uid := range []string{upgradeUID1, upgradeUID2} {
			id := "PROJ-" + fmt.Sprint(i+1)
			require.NoError(t, f.store.CreateNode(ctx, &model.Node{
				ID: id, Project: "PROJ", Depth: 0, Seq: i + 1, UID: uid, Title: "Task " + id,
				Description: "Upgrade fixture " + id, Labels: []string{"upgrade"}, Assignee: "dev",
				Creator: "cli", Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
				NodeType: model.NodeTypeEpic, ContentHash: "h-" + id, CreatedAt: created, UpdatedAt: created,
			}))
		}
		require.NoError(t, f.store.SetAnnotations(ctx, "PROJ-1", guardAnnotations()))
		f.exec(t, `INSERT INTO dependencies (from_id, to_id, dep_type, created_at) VALUES (?, ?, ?, ?)`,
			"PROJ-1", "PROJ-2", string(model.DepTypeBlocks), "2026-09-03T08:30:00Z")
	}
	if setup.invalidUTF8 {
		f.exec(t, `UPDATE nodes SET description = ? WHERE id = 'PROJ-2'`, "cut \xe2\x80 and stray \xff byte")
	}
	if setup.uidless {
		f.exec(t, `UPDATE nodes SET uid = NULL WHERE id = 'PROJ-2'`)
	}
	require.NoError(t, f.svc.AutoExport(ctx, f.mtixDir))
	return f
}

// exec runs a write on the fixture's store, bypassing the service.
func (f *guardFixture) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	_, err := f.store.WriteDB().ExecContext(context.Background(), query, args...)
	require.NoError(t, err)
}

// reopen closes the store and opens it again, as the next command does: the
// open gives every task without a uid a backfill uid (MTIX-95.31.9).
func (f *guardFixture) reopen(t *testing.T) {
	t.Helper()
	require.NoError(t, f.store.Close())
	st, err := sqlite.New(filepath.Join(f.mtixDir, "data"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	f.store = st
	f.svc = service.NewSyncService(st, slog.New(slog.NewTextHandler(f.logs, nil)), func() time.Time { return f.now })
	f.svc.SetNoticeWriter(f.notices)
}

// TestV053BaselineHash_UpgradeFixture_MatchesPinnedHash pins the baselines
// 0.5.3 wrote for the fixture, so a change to the fixture or to the
// reconstruction of 0.5.3's hash cannot pass unnoticed.
func TestV053BaselineHash_UpgradeFixture_MatchesPinnedHash(t *testing.T) {
	tests := []struct {
		name  string
		setup upgradeSetup
		want  string
	}{
		{"valid UTF-8", upgradeSetup{}, v053FixtureBaseline},
		{"description holding invalid UTF-8", upgradeSetup{invalidUTF8: true}, v053FixtureBaselineInvalidUTF8},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newUpgradeFixture(t, tt.setup)
			assert.Equal(t, tt.want, v053BaselineHash(t, f.store))
		})
	}
}
