// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// The retired holds of tasks the sync log had no creation for
// (MTIX-112.17.16.37, reviews r1b and r1c, lead rulings). A task held as
// unlogged and deleted here BEFORE its creation reached the log (the
// doctor's fix, or mtix gc) keeps its delete, and while it stays deleted
// every other held event of it, local for good: a teammate's live task with
// the same uid is never tombstoned by this replica's cleanup. A delete made
// after the creation is logged is an ordinary delete and is sent; a task
// undeleted here releases its edits and keeps only the predating delete. An
// event whose own task has no node and no logged creation (mtix gc purged
// an unlogged task) is retired too.

// holdWithReason records the events ids as push holds with reason.
func holdWithReason(t *testing.T, s *sqlite.Store, reason string, ids ...string) {
	t.Helper()
	holds := make([]sqlite.QuarantinedEvent, len(ids))
	for i, id := range ids {
		holds[i] = sqlite.QuarantinedEvent{EventID: id, RawEvent: "{}", Reason: reason}
	}
	require.NoError(t, s.HoldPushEvents(context.Background(), holds, "test"))
}

// holdAsUnlogged records the events ids as push holds of an unlogged task,
// with the reason push gives them.
func holdAsUnlogged(t *testing.T, s *sqlite.Store, ids ...string) {
	t.Helper()
	holdWithReason(t, s, "no logged creation: task MTIX-1 (uid u) has no creation in the sync log", ids...)
}

// pullCreation moves the creation of node id to the end of the local log,
// as a pull that brings it after every local event does.
func pullCreation(t *testing.T, raw *sql.DB, id string) {
	t.Helper()
	res, err := raw.Exec(`UPDATE sync_events SET rowid = (SELECT MAX(rowid) + 1 FROM sync_events)
		WHERE node_id = ? AND op_type = 'create_node'`, id)
	require.NoError(t, err)
	n, err := res.RowsAffected()
	require.NoError(t, err)
	require.Equal(t, int64(1), n)
}

// retiredCase is one state of the retired-hold rule over unloggedFixture.
type retiredCase struct {
	name string
	// setup changes the fixture and returns the event ids to read with the
	// subject each must report as retired ("" when none).
	setup func(t *testing.T, s *sqlite.Store, raw *sql.DB) map[string]string
}

// retiredCases are the states of the rule (lead rulings r1d): a hold is
// retired only when the task's delete predates its logged creation; a task
// live here releases its held edits, and only its predating delete stays.
// MTIX-1's creation is moved after the local events by pullCreation, as a
// pull of a teammate's creation of the same task does.
func retiredCases() []retiredCase {
	ctx := context.Background()
	title := "retitled"
	edit := func(t *testing.T, s *sqlite.Store, raw *sql.DB, id string) string {
		require.NoError(t, s.UpdateNode(ctx, id, &store.NodeUpdate{Title: &title}))
		return lastEventID(t, raw, id, model.OpUpdateField)
	}
	del := func(t *testing.T, s *sqlite.Store, raw *sql.DB, id string, cascade bool) string {
		require.NoError(t, s.DeleteNode(ctx, id, cascade, "alice"))
		return lastEventID(t, raw, id, model.OpDelete)
	}
	undelete := func(t *testing.T, raw *sql.DB) {
		_, err := raw.Exec(`UPDATE nodes SET deleted_at = NULL WHERE id = 'MTIX-1'`)
		require.NoError(t, err)
	}
	link := func(t *testing.T, s *sqlite.Store, raw *sql.DB, from, to string) string {
		require.NoError(t, s.AddDependency(ctx, &model.Dependency{FromID: from, ToID: to,
			DepType: model.DepTypeRelated, CreatedBy: "alice", CreatedAt: time.Now().UTC()}))
		return lastEventID(t, raw, from, model.OpLinkDep)
	}
	return []retiredCase{
		{"held, deleted, then the creation is pulled: edit and delete stay held", func(t *testing.T, s *sqlite.Store, raw *sql.DB) map[string]string {
			e := edit(t, s, raw, "MTIX-1")
			holdAsUnlogged(t, s, e)
			d := del(t, s, raw, "MTIX-1", true)
			pullCreation(t, raw, "MTIX-1")
			return map[string]string{e: "MTIX-1", d: "MTIX-1"}
		}},
		{"held, creation pulled, task live: released", func(t *testing.T, s *sqlite.Store, raw *sql.DB) map[string]string {
			e := edit(t, s, raw, "MTIX-1")
			holdAsUnlogged(t, s, e)
			pullCreation(t, raw, "MTIX-1")
			return map[string]string{e: ""}
		}},
		{"held, creation pulled, then deleted: an ordinary delete, edit and delete are sent", func(t *testing.T, s *sqlite.Store, raw *sql.DB) map[string]string {
			e := edit(t, s, raw, "MTIX-1")
			holdAsUnlogged(t, s, e)
			pullCreation(t, raw, "MTIX-1")
			return map[string]string{e: "", del(t, s, raw, "MTIX-1", true): ""}
		}},
		{"deleted, undeleted, creation pulled: edit released, only the delete retired", func(t *testing.T, s *sqlite.Store, raw *sql.DB) map[string]string {
			e := edit(t, s, raw, "MTIX-1")
			d := del(t, s, raw, "MTIX-1", true)
			holdAsUnlogged(t, s, e, d)
			undelete(t, raw)
			pullCreation(t, raw, "MTIX-1")
			return map[string]string{e: "", d: "MTIX-1"}
		}},
		{"live, delete held for another reason, creation pulled: edit released", func(t *testing.T, s *sqlite.Store, raw *sql.DB) map[string]string {
			e := edit(t, s, raw, "MTIX-1")
			d := del(t, s, raw, "MTIX-1", true)
			holdAsUnlogged(t, s, e)
			holdWithReason(t, s, "refused: x", d)
			undelete(t, raw)
			pullCreation(t, raw, "MTIX-1")
			return map[string]string{e: "", d: "MTIX-1"}
		}},
		// Lead ruling r2 2: the subject is every task a hold names, whichever
		// held event put it there.
		{"only a held link from it, deleted, creation pulled: link and delete stay held", func(t *testing.T, s *sqlite.Store, raw *sql.DB) map[string]string {
			l := link(t, s, raw, "MTIX-1", "MTIX-3")
			holdAsUnlogged(t, s, l)
			d := del(t, s, raw, "MTIX-1", true)
			pullCreation(t, raw, "MTIX-1")
			return map[string]string{l: "MTIX-1", d: "MTIX-1"}
		}},
		{"only a held link to it, deleted, creation pulled: link and delete stay held", func(t *testing.T, s *sqlite.Store, raw *sql.DB) map[string]string {
			l := link(t, s, raw, "MTIX-3", "MTIX-1")
			holdAsUnlogged(t, s, l)
			d := del(t, s, raw, "MTIX-1", true)
			pullCreation(t, raw, "MTIX-1")
			return map[string]string{l: "MTIX-1", d: "MTIX-1"}
		}},
		{"only a held child creation under it, cascade-deleted, creation pulled: the creation and both deletes stay held", func(t *testing.T, s *sqlite.Store, raw *sql.DB) map[string]string {
			mustCreateNode(t, s, "MTIX-1.1", "MTIX-1")
			c := lastEventID(t, raw, "MTIX-1.1", model.OpCreateNode)
			holdAsUnlogged(t, s, c)
			d := del(t, s, raw, "MTIX-1", true)
			child := lastEventID(t, raw, "MTIX-1.1", model.OpDelete)
			pullCreation(t, raw, "MTIX-1")
			return map[string]string{c: "MTIX-1", d: "MTIX-1", child: "MTIX-1"}
		}},
		{"only a held edit of its child, cascade-deleted, creation pulled: the edit and both deletes stay held", func(t *testing.T, s *sqlite.Store, raw *sql.DB) map[string]string {
			mustCreateNode(t, s, "MTIX-1.1", "MTIX-1")
			e := edit(t, s, raw, "MTIX-1.1")
			holdAsUnlogged(t, s, e)
			d := del(t, s, raw, "MTIX-1", true)
			child := lastEventID(t, raw, "MTIX-1.1", model.OpDelete)
			pullCreation(t, raw, "MTIX-1")
			return map[string]string{e: "MTIX-1", d: "MTIX-1", child: "MTIX-1"}
		}},
		{"only a held child creation, child live, task undeleted, creation pulled: the creation is released, the delete stays", func(t *testing.T, s *sqlite.Store, raw *sql.DB) map[string]string {
			mustCreateNode(t, s, "MTIX-1.1", "MTIX-1")
			c := lastEventID(t, raw, "MTIX-1.1", model.OpCreateNode)
			holdAsUnlogged(t, s, c)
			d := del(t, s, raw, "MTIX-1", true)
			_, err := raw.Exec(`UPDATE nodes SET deleted_at = NULL WHERE id IN ('MTIX-1', 'MTIX-1.1')`)
			require.NoError(t, err)
			pullCreation(t, raw, "MTIX-1")
			return map[string]string{c: "", d: "MTIX-1"}
		}},
		// X1: the rule reads the task's LATEST delete.
		{"deleted, undeleted, creation pulled, deleted again: the edit and the later delete are sent", func(t *testing.T, s *sqlite.Store, raw *sql.DB) map[string]string {
			e := edit(t, s, raw, "MTIX-1")
			d := del(t, s, raw, "MTIX-1", true)
			holdAsUnlogged(t, s, e, d)
			undelete(t, raw)
			pullCreation(t, raw, "MTIX-1")
			return map[string]string{e: "", d: "MTIX-1", del(t, s, raw, "MTIX-1", true): ""}
		}},
		// X4: a held link to a task live here is released once its creation is
		// logged; only an own delete can predate the creation.
		{"held link to it, task live, creation pulled: the link is released", func(t *testing.T, s *sqlite.Store, raw *sql.DB) map[string]string {
			l := link(t, s, raw, "MTIX-3", "MTIX-1")
			holdAsUnlogged(t, s, l)
			pullCreation(t, raw, "MTIX-1")
			return map[string]string{l: ""}
		}},
		// X2, X3, X8: each creation lookup of a held task, by uid, by event
		// id, and the earliest of them.
		{"held, deleted, creation (legacy event id, logged by uid only) pulled: retired", func(t *testing.T, s *sqlite.Store, raw *sql.DB) map[string]string {
			mustCreateNode(t, s, "MTIX-5", "")
			_, err := raw.Exec(`UPDATE sync_events SET event_id = 'legacy-create-5'
				WHERE node_id = 'MTIX-5' AND op_type = 'create_node'`)
			require.NoError(t, err)
			e := edit(t, s, raw, "MTIX-5")
			holdAsUnlogged(t, s, e)
			d := del(t, s, raw, "MTIX-5", true)
			pullCreation(t, raw, "MTIX-5")
			return map[string]string{e: "MTIX-5", d: "MTIX-5"}
		}},
		{"held, deleted, creation (logged by event id only, no uid) pulled: retired", func(t *testing.T, s *sqlite.Store, raw *sql.DB) map[string]string {
			e := edit(t, s, raw, "MTIX-4")
			holdAsUnlogged(t, s, e)
			d := del(t, s, raw, "MTIX-4", true)
			pullCreation(t, raw, "MTIX-4")
			return map[string]string{e: "MTIX-4", d: "MTIX-4"}
		}},
		{"held, deleted after its earliest creation, a second creation pulled later: an ordinary delete", func(t *testing.T, s *sqlite.Store, raw *sql.DB) map[string]string {
			e := edit(t, s, raw, "MTIX-4")
			holdAsUnlogged(t, s, e)
			d := del(t, s, raw, "MTIX-4", true)
			_, err := raw.Exec(`INSERT INTO sync_events (event_id, project_prefix, node_id, op_type, payload,
				wall_clock_ts, lamport_clock, vector_clock, author_id, author_machine_hash, sync_status, created_at, uid)
				SELECT 'pulled-create-4', project_prefix, node_id, op_type, payload, wall_clock_ts, lamport_clock,
				       vector_clock, author_id, author_machine_hash, 'applied', created_at,
				       (SELECT uid FROM nodes WHERE id = 'MTIX-4')
				FROM sync_events WHERE node_id = 'MTIX-4' AND op_type = 'create_node'`)
			require.NoError(t, err)
			return map[string]string{e: "", d: ""}
		}},
		{"held link to it, deleted, creation pulled: the link stays held", func(t *testing.T, s *sqlite.Store, raw *sql.DB) map[string]string {
			e := edit(t, s, raw, "MTIX-1")
			l := link(t, s, raw, "MTIX-3", "MTIX-1")
			holdAsUnlogged(t, s, e, l)
			del(t, s, raw, "MTIX-1", false)
			pullCreation(t, raw, "MTIX-1")
			return map[string]string{l: "MTIX-1"}
		}},
		{"edit held for a refusal, deleted, creation pulled: not retired", func(t *testing.T, s *sqlite.Store, raw *sql.DB) map[string]string {
			e := edit(t, s, raw, "MTIX-1")
			holdWithReason(t, s, "refused: x", e)
			d := del(t, s, raw, "MTIX-1", true)
			pullCreation(t, raw, "MTIX-1")
			return map[string]string{e: "", d: ""}
		}},
		{"a pulled quarantine row of its uid is not a hold: not retired", func(t *testing.T, s *sqlite.Store, raw *sql.DB) map[string]string {
			e := edit(t, s, raw, "MTIX-1")
			_, err := raw.Exec(`INSERT INTO sync_quarantine (event_id, source, raw_event, reason, first_seen,
				last_attempt, attempts, cli_version) VALUES (?, 'pull', '{}', 'no logged creation: x',
				'2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 1, 'test')`, e)
			require.NoError(t, err)
			d := del(t, s, raw, "MTIX-1", true)
			pullCreation(t, raw, "MTIX-1")
			return map[string]string{e: "", d: ""}
		}},
		{"purged, no logged creation: retired", func(t *testing.T, s *sqlite.Store, raw *sql.DB) map[string]string {
			e := edit(t, s, raw, "MTIX-2")
			d := del(t, s, raw, "MTIX-2", true)
			_, err := raw.Exec(`DELETE FROM nodes WHERE id IN ('MTIX-2.1', 'MTIX-2')`)
			require.NoError(t, err)
			return map[string]string{e: "MTIX-2", d: "MTIX-2"}
		}},
		{"purged, creation logged: sent as before", func(t *testing.T, s *sqlite.Store, raw *sql.DB) map[string]string {
			e := edit(t, s, raw, "MTIX-1")
			_, err := raw.Exec(`DELETE FROM nodes WHERE id = 'MTIX-1'`)
			require.NoError(t, err)
			return map[string]string{e: ""}
		}},
		{"purged, creation logged by uid only (legacy event id): sent", func(t *testing.T, s *sqlite.Store, raw *sql.DB) map[string]string {
			mustCreateNode(t, s, "MTIX-5", "")
			_, err := raw.Exec(`UPDATE sync_events SET event_id = 'legacy-create-5'
				WHERE node_id = 'MTIX-5' AND op_type = 'create_node'`)
			require.NoError(t, err)
			e := edit(t, s, raw, "MTIX-5")
			_, err = raw.Exec(`DELETE FROM nodes WHERE id = 'MTIX-5'`)
			require.NoError(t, err)
			return map[string]string{e: ""}
		}},
		{"purged, creation logged by event id only (no uid): sent", func(t *testing.T, s *sqlite.Store, raw *sql.DB) map[string]string {
			e := edit(t, s, raw, "MTIX-4")
			_, err := raw.Exec(`DELETE FROM nodes WHERE id = 'MTIX-4'`)
			require.NoError(t, err)
			return map[string]string{e: ""}
		}},
	}
}

func TestUnloggedSubjects_RetiredOnlyWhenTheDeletePredatesTheLoggedCreation(t *testing.T) {
	for _, tt := range retiredCases() {
		t.Run(tt.name, func(t *testing.T) {
			s, raw := unloggedFixture(t)
			want := tt.setup(t, s, raw)
			ids := make([]string, 0, len(want))
			for id := range want {
				ids = append(ids, id)
			}
			got, err := s.UnloggedSubjects(context.Background(), ids)
			require.NoError(t, err)
			for id, node := range want {
				subj, ok := got[id]
				if node == "" {
					require.False(t, ok, "%s: %+v", id, subj)
					continue
				}
				require.True(t, ok, "%s is held for good", id)
				require.Equal(t, node, subj.NodeID)
				require.NotEmpty(t, subj.UID)
				require.True(t, subj.Retired)
			}
		})
	}
}

// TestUnloggedSubjects_LiveUnloggedTaskWinsOverItsHeldDelete: before the
// creation is logged, an undeleted unlogged task is named again and not
// retired, even though its delete is held (mtix undelete fails doctor
// again; story 4).
func TestUnloggedSubjects_LiveUnloggedTaskWinsOverItsHeldDelete(t *testing.T) {
	ctx := context.Background()
	s, raw := unloggedFixture(t)
	title := "retitled"
	require.NoError(t, s.UpdateNode(ctx, "MTIX-2", &store.NodeUpdate{Title: &title}))
	e := lastEventID(t, raw, "MTIX-2", model.OpUpdateField)
	require.NoError(t, s.DeleteNode(ctx, "MTIX-2", false, "alice"))
	holdAsUnlogged(t, s, e, lastEventID(t, raw, "MTIX-2", model.OpDelete))
	_, err := raw.Exec(`UPDATE nodes SET deleted_at = NULL WHERE id = 'MTIX-2'`)
	require.NoError(t, err)
	got, err := s.UnloggedSubjects(ctx, []string{e})
	require.NoError(t, err)
	require.Equal(t, "MTIX-2", got[e].NodeID)
	require.False(t, got[e].Retired)
}

// TestUnloggedSubjects_UnloggedLineWinsOverARetiredTarget: a link from
// MTIX-2 (unlogged, live) to MTIX-1 (deleted here before its creation was
// pulled, so retired) is named by MTIX-2 and not retired: doctor still
// names MTIX-2 with its fix.
func TestUnloggedSubjects_UnloggedLineWinsOverARetiredTarget(t *testing.T) {
	ctx := context.Background()
	s, raw := unloggedFixture(t)
	require.NoError(t, s.AddDependency(ctx, &model.Dependency{FromID: "MTIX-2", ToID: "MTIX-1",
		DepType: model.DepTypeRelated, CreatedBy: "alice", CreatedAt: time.Now().UTC()}))
	link := lastEventID(t, raw, "MTIX-2", model.OpLinkDep)
	title := "retitled"
	require.NoError(t, s.UpdateNode(ctx, "MTIX-1", &store.NodeUpdate{Title: &title}))
	holdAsUnlogged(t, s, lastEventID(t, raw, "MTIX-1", model.OpUpdateField))
	require.NoError(t, s.DeleteNode(ctx, "MTIX-1", false, "alice"))
	pullCreation(t, raw, "MTIX-1")
	got, err := s.UnloggedSubjects(ctx, []string{link})
	require.NoError(t, err)
	require.Equal(t, "MTIX-2", got[link].NodeID)
	require.False(t, got[link].Retired)
}
