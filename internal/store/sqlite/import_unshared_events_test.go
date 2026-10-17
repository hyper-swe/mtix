// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// MTIX-107.31.3: on a store that never synced, every event is still pending
// and nothing was shared, so a replace that drops a task discards that
// task's pending events with it. Left behind, they would reach the hub with
// the first push after a hub is configured: the hub and teammates would hold
// a task this board lacks.

// pendingFor counts the pending events that name node id.
func pendingFor(t *testing.T, raw *sql.DB, id string) int {
	t.Helper()
	return countRows(t, raw, `SELECT COUNT(*) FROM sync_events WHERE node_id = ? AND sync_status = 'pending'`, id)
}

// ownEventsOf counts the pending events of node id other than links: those
// stay with a kept task when the task a link points at is dropped.
func ownEventsOf(t *testing.T, raw *sql.DB, id string) int {
	t.Helper()
	return countRows(t, raw, `SELECT COUNT(*) FROM sync_events WHERE node_id = ? AND sync_status = 'pending'
		AND op_type NOT IN ('link_dep', 'unlink_dep')`, id)
}

// linkEventsTo counts the pending link events that point at node id.
func linkEventsTo(t *testing.T, raw *sql.DB, id string) int {
	t.Helper()
	return countRows(t, raw, `SELECT COUNT(*) FROM sync_events WHERE sync_status = 'pending'
		AND op_type IN ('link_dep', 'unlink_dep') AND json_extract(payload, '$.depends_on_node_id') = ?`, id)
}

func TestImport_ReplaceOnNeverSyncedStore_DiscardsPendingEventsOfDroppedTasks(t *testing.T) {
	tests := []struct {
		name string
		// edit changes the file's copy of MTIX-3.
		drop func(d *sqlite.ExportData) *sqlite.ExportData
		// kept is whether MTIX-3's pending events must survive.
		kept bool
	}{
		{"file lacks the task", func(d *sqlite.ExportData) *sqlite.ExportData { return withoutExportNode(d, "MTIX-3") }, false},
		{"file keeps the number, drops the uid", func(d *sqlite.ExportData) *sqlite.ExportData {
			setExportUID(d, "MTIX-3", "")
			return d
		}, false},
		{"file keeps the number, another title and uid", func(d *sqlite.ExportData) *sqlite.ExportData {
			setExportUID(d, "MTIX-3", swappedUID)
			setExportTitle(d, "MTIX-3", "Another task entirely")
			return d
		}, false},
		{"file renumbers the task, same uid", func(d *sqlite.ExportData) *sqlite.ExportData {
			renumberExportNode(d, "MTIX-3", "MTIX-9", 9)
			return d
		}, true},
		{"file keeps the number, takes another uid, same title", func(d *sqlite.ExportData) *sqlite.ExportData {
			setExportUID(d, "MTIX-3", swappedUID)
			return d
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s, raw := mutationTestStore(t)
			for _, id := range []string{"MTIX-1", "MTIX-2", "MTIX-3"} {
				mustCreateNode(t, s, id, "")
			}
			require.NoError(t, s.AddDependency(ctx, &model.Dependency{FromID: "MTIX-1", ToID: "MTIX-3",
				DepType: model.DepTypeBlocks, CreatedAt: time.Now(), CreatedBy: "test"}))
			require.NotZero(t, linkEventsTo(t, raw, "MTIX-3"), "a pending link event points at MTIX-3")
			keep1, keep2 := ownEventsOf(t, raw, "MTIX-1"), ownEventsOf(t, raw, "MTIX-2")
			held3 := pendingFor(t, raw, "MTIX-3")
			require.NotZero(t, held3)

			board, err := s.Export(ctx, "", "")
			require.NoError(t, err)
			board = tt.drop(board)
			require.NoError(t, sqlite.RecomputeExportChecksum(board))
			_, err = s.Import(ctx, board, sqlite.ImportModeReplace, false)
			require.NoError(t, err, "a store that never synced takes the file")

			assert.Equal(t, keep1, ownEventsOf(t, raw, "MTIX-1"), "a kept task keeps its events")
			assert.Equal(t, keep2, ownEventsOf(t, raw, "MTIX-2"), "a kept task keeps its events")
			if tt.kept {
				assert.NotZero(t, countRows(t, raw, `SELECT COUNT(*) FROM sync_events WHERE sync_status = 'pending'
					AND op_type = 'create_node' AND uid IN (SELECT uid FROM nodes WHERE id IN ('MTIX-3', 'MTIX-9'))`),
					"a retained task keeps its creation")
				return
			}
			assert.Zero(t, pendingFor(t, raw, "MTIX-3"), "no pending event names the dropped task")
			assert.Zero(t, linkEventsTo(t, raw, "MTIX-3"), "no pending link points at the dropped task")
			missing, err := s.MissingLoggedNodes(ctx)
			require.NoError(t, err)
			assert.Empty(t, missing)

			// The first push after a hub is configured reads the pending events.
			pending, err := s.ReadPendingEvents(ctx, 1000)
			require.NoError(t, err)
			for _, e := range pending {
				assert.NotEqual(t, "MTIX-3", e.NodeID, "the push carries no event of a task the board lacks")
			}
		})
	}
}

// A push hold keeps a copy of a pending event in sync_quarantine; the copy of
// a discarded event goes with it.
func TestImport_ReplaceOnNeverSyncedStore_DiscardsHeldCopiesOfDiscardedEvents(t *testing.T) {
	ctx := context.Background()
	s, raw := mutationTestStore(t)
	mustCreateNode(t, s, "MTIX-1", "")
	mustCreateNode(t, s, "MTIX-3", "")
	var eventID string
	require.NoError(t, raw.QueryRow(`SELECT event_id FROM sync_events
		WHERE node_id = 'MTIX-3' AND op_type = 'create_node'`).Scan(&eventID))
	require.NoError(t, s.HoldPushEvents(ctx, []sqlite.QuarantinedEvent{{EventID: eventID, RawEvent: "{}", Reason: "held"}}, "test"))
	require.Equal(t, 1, countRows(t, raw, `SELECT COUNT(*) FROM sync_quarantine WHERE event_id = ?`, eventID))

	_, err := s.Import(ctx, exportWithout(t, s, "MTIX-3"), sqlite.ImportModeReplace, false)
	require.NoError(t, err)
	assert.Zero(t, countRows(t, raw, `SELECT COUNT(*) FROM sync_quarantine WHERE event_id = ?`, eventID))
	assert.Zero(t, pendingFor(t, raw, "MTIX-3"))
}

// A store that published to a file relay, or ingested from one, shared its
// events even though they stay pending in the journal and no hub mark is
// set: it counts as synced and a replace that drops a task is refused.
func TestImport_ReplaceOnRelaySyncedStore_Refused(t *testing.T) {
	tests := []struct {
		name string
		mark string
	}{
		{"published events to a relay", `INSERT INTO relay_push_cursor (id, cursor, next_rs) VALUES (1, 3, 4)`},
		{"ingested events from a relay", `INSERT INTO relay_ingest_cursor (peer_id, segment_no, rs, pub_epoch) VALUES ('peer-a', 1, 5, 1)`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s, raw := mutationTestStore(t)
			mustCreateNode(t, s, "MTIX-1", "")
			mustCreateNode(t, s, "MTIX-3", "")
			_, err := raw.Exec(tt.mark)
			require.NoError(t, err)
			synced, err := s.HasSyncedWithHub(ctx)
			require.NoError(t, err)
			require.True(t, synced, "a relay peer counts as synced")

			_, err = s.Import(ctx, exportWithout(t, s, "MTIX-3"), sqlite.ImportModeReplace, false)
			require.ErrorIs(t, err, sqlite.ErrReplaceOnSyncedStore)
			assert.NotZero(t, pendingFor(t, raw, "MTIX-3"), "a refused replace changes nothing")
			assert.True(t, nodeExists(t, raw, "MTIX-3"))
		})
	}
}

// withoutExportNode returns d without node id.
func withoutExportNode(d *sqlite.ExportData, id string) *sqlite.ExportData {
	kept := d.Nodes[:0]
	for _, n := range d.Nodes {
		if n.ID != id {
			kept = append(kept, n)
		}
	}
	d.Nodes, d.NodeCount = kept, len(kept)
	deps := d.Dependencies[:0]
	for _, dep := range d.Dependencies {
		if dep.FromID != id && dep.ToID != id {
			deps = append(deps, dep)
		}
	}
	d.Dependencies = deps
	return d
}

func setExportUID(d *sqlite.ExportData, id, uid string) {
	for i := range d.Nodes {
		if d.Nodes[i].ID == id {
			d.Nodes[i].UID = uid
		}
	}
}

func setExportTitle(d *sqlite.ExportData, id, title string) {
	for i := range d.Nodes {
		if d.Nodes[i].ID == id {
			d.Nodes[i].Title = title
		}
	}
}

func renumberExportNode(d *sqlite.ExportData, id, to string, seq int) {
	for i := range d.Nodes {
		if d.Nodes[i].ID == id {
			d.Nodes[i].ID, d.Nodes[i].Seq = to, seq
		}
	}
	for i := range d.Dependencies {
		if d.Dependencies[i].FromID == id {
			d.Dependencies[i].FromID = to
		}
		if d.Dependencies[i].ToID == id {
			d.Dependencies[i].ToID = to
		}
	}
}
