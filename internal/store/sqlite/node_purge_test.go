// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// A gc purge of a task the sync log has no creation for (MTIX-112.17.16.37,
// lead ruling r2 3) counts as a delete made before any later logged
// creation: PurgeDeletedNode marks the push holds of unlogged tasks whose
// event has the task on its lines as purged, and those holds stay retired
// even when a pull brings the task back here live.

// holdReason returns the reason of the push hold of eventID.
func holdReason(t *testing.T, raw *sql.DB, eventID string) string {
	t.Helper()
	var r string
	require.NoError(t, raw.QueryRow(`SELECT reason FROM sync_quarantine WHERE event_id = ? AND source = 'push'`,
		eventID).Scan(&r))
	return r
}

func TestPurgeDeletedNode_MarksTheHoldsOnItsLinesOnlyWhenUnlogged(t *testing.T) {
	ctx := context.Background()
	title := "retitled"
	edit := func(t *testing.T, s *sqlite.Store, raw *sql.DB, id string) string {
		require.NoError(t, s.UpdateNode(ctx, id, &store.NodeUpdate{Title: &title}))
		return lastEventID(t, raw, id, model.OpUpdateField)
	}
	link := func(t *testing.T, s *sqlite.Store, raw *sql.DB, from, to string) string {
		require.NoError(t, s.AddDependency(ctx, &model.Dependency{FromID: from, ToID: to,
			DepType: model.DepTypeRelated, CreatedBy: "alice", CreatedAt: time.Now().UTC()}))
		return lastEventID(t, raw, from, model.OpLinkDep)
	}
	tests := []struct {
		name string
		// setup makes the holds and returns, per held event, whether it
		// must be marked purged, then the node to purge and whether it is
		// deleted first.
		setup  func(t *testing.T, s *sqlite.Store, raw *sql.DB) map[string]bool
		purge  string
		delete bool
	}{
		{"unlogged task: its edit, its child's edit and a link to it are marked; other holds are not", func(t *testing.T, s *sqlite.Store, raw *sql.DB) map[string]bool {
			own, child, l := edit(t, s, raw, "MTIX-2"), edit(t, s, raw, "MTIX-2.1"), link(t, s, raw, "MTIX-1", "MTIX-2")
			other := edit(t, s, raw, "MTIX-1")
			holdAsUnlogged(t, s, own, child, l, other)
			refused := edit(t, s, raw, "MTIX-3")
			holdWithReason(t, s, "refused: x", refused)
			return map[string]bool{own: true, child: true, l: true, other: false, refused: false}
		}, "MTIX-2", true},
		{"logged task: nothing is marked", func(t *testing.T, s *sqlite.Store, raw *sql.DB) map[string]bool {
			e := edit(t, s, raw, "MTIX-1")
			holdAsUnlogged(t, s, e)
			return map[string]bool{e: false}
		}, "MTIX-1", true},
		{"live task: nothing is marked or purged", func(t *testing.T, s *sqlite.Store, raw *sql.DB) map[string]bool {
			e := edit(t, s, raw, "MTIX-2")
			holdAsUnlogged(t, s, e)
			return map[string]bool{e: false}
		}, "MTIX-2", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, raw := unloggedFixture(t)
			want := tt.setup(t, s, raw)
			before := map[string]string{}
			for id := range want {
				before[id] = holdReason(t, raw, id)
			}
			if tt.delete {
				require.NoError(t, s.DeleteNode(ctx, tt.purge, true, "alice"))
			}
			require.NoError(t, s.PurgeDeletedNode(ctx, tt.purge))
			var n int
			require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM nodes WHERE id = ?`, tt.purge).Scan(&n))
			require.Equal(t, map[bool]int{true: 0, false: 1}[tt.delete], n, "only a deleted node is purged")
			for id, marked := range want {
				r := holdReason(t, raw, id)
				if !marked {
					require.Equal(t, before[id], r, "%s keeps its reason", id)
					continue
				}
				require.True(t, strings.HasPrefix(r, sqlite.UnloggedPurgedPrefix), r)
				require.Contains(t, r, "task "+tt.purge+" (uid \"")
				require.Contains(t, r, "was purged from this board before the sync log held its creation, "+
					"so this change is never sent; see mtix sync doctor")
			}
		})
	}
}

// TestUnloggedSubjects_PurgedHold_StaysRetiredWhenThePullBringsTheTaskBack:
// MTIX-2 (no logged creation) has a held edit, is deleted and purged; a pull
// then logs a teammate's creation of it and brings it back live. The edit
// and the held delete stay retired, as purged; without the purge the edit
// would be released.
func TestUnloggedSubjects_PurgedHold_StaysRetiredWhenThePullBringsTheTaskBack(t *testing.T) {
	ctx := context.Background()
	s, raw := unloggedFixture(t)
	title := "retitled"
	require.NoError(t, s.UpdateNode(ctx, "MTIX-2", &store.NodeUpdate{Title: &title}))
	e := lastEventID(t, raw, "MTIX-2", model.OpUpdateField)
	holdAsUnlogged(t, s, e)
	require.NoError(t, s.DeleteNode(ctx, "MTIX-2", true, "alice"))
	d := lastEventID(t, raw, "MTIX-2", model.OpDelete)
	holdAsUnlogged(t, s, d)
	_, err := raw.Exec(`CREATE TABLE kept_node AS SELECT * FROM nodes WHERE id = 'MTIX-2'`)
	require.NoError(t, err)
	require.NoError(t, s.PurgeDeletedNode(ctx, "MTIX-2"))
	_, err = raw.Exec(`INSERT INTO nodes SELECT * FROM kept_node`)
	require.NoError(t, err)
	_, err = raw.Exec(`UPDATE nodes SET deleted_at = NULL WHERE id = 'MTIX-2'`)
	require.NoError(t, err)
	_, err = raw.Exec(`INSERT INTO sync_events (event_id, project_prefix, node_id, op_type, payload, wall_clock_ts,
		lamport_clock, vector_clock, author_id, author_machine_hash, sync_status, created_at, uid)
		VALUES ('pulled-create-2', 'MTIX', 'MTIX-2', 'create_node', '{}', 0, 999, '{}', 'bob', 'm', 'applied',
		        '2026-01-01T00:00:00Z', (SELECT uid FROM nodes WHERE id = 'MTIX-2'))`)
	require.NoError(t, err)

	got, err := s.UnloggedSubjects(ctx, []string{e, d})
	require.NoError(t, err)
	for _, id := range []string{e, d} {
		require.True(t, got[id].Retired, "%+v", got[id])
		require.True(t, got[id].Purged, "the purged form wins (the delete also predates the creation): %+v", got[id])
		require.Equal(t, "MTIX-2", got[id].NodeID)
	}
}
