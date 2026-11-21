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

// The reads of the push hold of tasks the sync log has no creation for
// (MTIX-112.17.16.37): which task an event depends on that the log holds no
// create_node for, matched by uid, by the creation's own event id for a
// creation queued before events carried uids, or by number for a task
// without a uid; and whether that hold is retired.

// lastEventID returns the id of the latest event of op for node id.
func lastEventID(t *testing.T, raw *sql.DB, id string, op model.OpType) string {
	t.Helper()
	var eventID string
	require.NoError(t, raw.QueryRow(`SELECT event_id FROM sync_events WHERE node_id = ? AND op_type = ?
		ORDER BY lamport_clock DESC LIMIT 1`, id, string(op)).Scan(&eventID))
	return eventID
}

// unloggedFixture is a store with MTIX-1 (logged), MTIX-2 (its creation
// removed from the log) and its child MTIX-2.1 (logged), MTIX-3 (a task
// and creation with no uid, matched by number) and MTIX-4 (a creation with
// no uid whose event id is the task's uid).
func unloggedFixture(t *testing.T) (*sqlite.Store, *sql.DB) {
	t.Helper()
	s, raw := mutationTestStore(t)
	for _, id := range []string{"MTIX-1", "MTIX-2", "MTIX-3", "MTIX-4"} {
		mustCreateNode(t, s, id, "")
	}
	_, err := raw.Exec(`DELETE FROM sync_events WHERE op_type = 'create_node' AND node_id = 'MTIX-2'`)
	require.NoError(t, err)
	mustCreateNode(t, s, "MTIX-2.1", "MTIX-2")
	_, err = raw.Exec(`UPDATE sync_events SET uid = NULL WHERE node_id = 'MTIX-3'`)
	require.NoError(t, err)
	_, err = raw.Exec(`UPDATE nodes SET uid = NULL WHERE id = 'MTIX-3'`)
	require.NoError(t, err)
	_, err = raw.Exec(`UPDATE nodes SET uid = (SELECT event_id FROM sync_events
		WHERE node_id = 'MTIX-4' AND op_type = 'create_node') WHERE id = 'MTIX-4'`)
	require.NoError(t, err)
	_, err = raw.Exec(`UPDATE sync_events SET uid = NULL WHERE node_id = 'MTIX-4'`)
	require.NoError(t, err)
	return s, raw
}

func TestUnloggedSubjects_MatchesTheTaskTheLogDoesNotCreate(t *testing.T) {
	ctx := context.Background()
	s, raw := unloggedFixture(t)
	title := "retitled"
	for _, id := range []string{"MTIX-1", "MTIX-2", "MTIX-2.1", "MTIX-3", "MTIX-4"} {
		require.NoError(t, s.UpdateNode(ctx, id, &store.NodeUpdate{Title: &title}))
	}
	link := func(from, to string) {
		require.NoError(t, s.AddDependency(ctx, &model.Dependency{FromID: from, ToID: to,
			DepType: model.DepTypeRelated, CreatedBy: "alice", CreatedAt: time.Now().UTC()}))
	}
	link("MTIX-1", "MTIX-2")
	linkToUnlogged := lastEventID(t, raw, "MTIX-1", model.OpLinkDep)
	link("MTIX-1", "MTIX-3")
	linkToLogged := lastEventID(t, raw, "MTIX-1", model.OpLinkDep)
	_, err := raw.Exec(`UPDATE sync_events SET uid = NULL WHERE node_id = 'MTIX-3'`)
	require.NoError(t, err)

	tests := []struct {
		name, eventID, want string
	}{
		{"edit of a logged task", lastEventID(t, raw, "MTIX-1", model.OpUpdateField), ""},
		{"edit of the unlogged task", lastEventID(t, raw, "MTIX-2", model.OpUpdateField), "MTIX-2"},
		{"creation under it", lastEventID(t, raw, "MTIX-2.1", model.OpCreateNode), "MTIX-2"},
		{"edit below it", lastEventID(t, raw, "MTIX-2.1", model.OpUpdateField), "MTIX-2"},
		{"link to it", linkToUnlogged, "MTIX-2"},
		{"link between logged tasks", linkToLogged, ""},
		{"task and creation without a uid, matched by number", lastEventID(t, raw, "MTIX-3", model.OpUpdateField), ""},
		{"creation without a uid, its event id the task's uid", lastEventID(t, raw, "MTIX-4", model.OpUpdateField), ""},
	}
	ids := make([]string, len(tests))
	for i, tt := range tests {
		ids[i] = tt.eventID
	}
	got, err := s.UnloggedSubjects(ctx, ids)
	require.NoError(t, err)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			subj, ok := got[tt.eventID]
			if tt.want == "" {
				require.False(t, ok, "%+v", subj)
				return
			}
			require.True(t, ok)
			require.Equal(t, tt.want, subj.NodeID)
			require.Empty(t, subj.ParentID, "MTIX-2 is a root")
			require.NotEmpty(t, subj.UID)
			require.False(t, subj.Retired)
		})
	}
}

func TestUnloggedSubjects_RetiredOnlyWhenTheLineUpToTheTaskIsDeleted(t *testing.T) {
	ctx := context.Background()
	s, raw := unloggedFixture(t)
	title := "retitled"
	require.NoError(t, s.UpdateNode(ctx, "MTIX-2", &store.NodeUpdate{Title: &title}))
	require.NoError(t, s.UpdateNode(ctx, "MTIX-2.1", &store.NodeUpdate{Title: &title}))
	own := lastEventID(t, raw, "MTIX-2", model.OpUpdateField)
	child := lastEventID(t, raw, "MTIX-2.1", model.OpUpdateField)

	_, err := raw.Exec(`UPDATE nodes SET deleted_at = '2026-01-01T00:00:00Z' WHERE id = 'MTIX-2'`)
	require.NoError(t, err)
	got, err := s.UnloggedSubjects(ctx, []string{own, child})
	require.NoError(t, err)
	require.True(t, got[own].Retired, "the task itself is deleted")
	require.False(t, got[child].Retired, "its child is still live here")

	require.NoError(t, s.DeleteNode(ctx, "MTIX-2.1", false, "alice"))
	got, err = s.UnloggedSubjects(ctx, []string{child})
	require.NoError(t, err)
	require.True(t, got[child].Retired)
}

func TestHeldPushEventsExcept_LeavesOutThePrefix(t *testing.T) {
	ctx := context.Background()
	s, raw := unloggedFixture(t)
	held := []sqlite.QuarantinedEvent{
		{EventID: lastEventID(t, raw, "MTIX-1", model.OpCreateNode), RawEvent: "{}", Reason: "no logged creation: x", NodeID: "MTIX-1", OpType: "create_node"},
		{EventID: lastEventID(t, raw, "MTIX-3", model.OpCreateNode), RawEvent: "{}", Reason: "too large: y", NodeID: "MTIX-3", OpType: "create_node"},
	}
	require.NoError(t, s.HoldPushEvents(ctx, held, "test"))
	n, err := s.CountHeldPushEventsExcept(ctx, "no logged creation: ")
	require.NoError(t, err)
	require.Equal(t, 1, n)
	all, err := s.CountHeldPushEventsExcept(ctx, "")
	require.NoError(t, err)
	require.Equal(t, 2, all)
	list, err := s.HeldPushEventsExcept(ctx, "no logged creation: ", -1)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, held[1].EventID, list[0].EventID)
	ids, err := s.HeldPushEventIDsWithReason(ctx, "no logged creation: ")
	require.NoError(t, err)
	require.Equal(t, []string{held[0].EventID}, ids)
}

// unlog removes the creation of the task id from the sync log of raw.
func unlog(t *testing.T, raw *sql.DB, id string) {
	t.Helper()
	res, err := raw.Exec(`DELETE FROM sync_events WHERE op_type = 'create_node' AND node_id = ?`, id)
	require.NoError(t, err)
	n, err := res.RowsAffected()
	require.NoError(t, err)
	require.Equal(t, int64(1), n, "the creation of %s was in the log", id)
}

// TestUnloggedSubjects_TopmostUnloggedTaskOnTheLines (review r1 F4): with
// MTIX-2 (unlogged) above MTIX-2.1 (logged) above MTIX-2.1.1 (unlogged), and
// a root MTIX-5 (unlogged), every event of MTIX-2.1.1 names MTIX-2, the
// topmost unlogged task on all its lines: an edit (one line), and a link
// from it to MTIX-5 or from MTIX-5 to it (two lines, MTIX-5's the shorter).
func TestUnloggedSubjects_TopmostUnloggedTaskOnTheLines(t *testing.T) {
	ctx := context.Background()
	s, raw := unloggedFixture(t)
	mustCreateNode(t, s, "MTIX-2.1.1", "MTIX-2.1")
	mustCreateNode(t, s, "MTIX-5", "")
	unlog(t, raw, "MTIX-2.1.1")
	unlog(t, raw, "MTIX-5")
	title := "retitled"
	require.NoError(t, s.UpdateNode(ctx, "MTIX-2.1.1", &store.NodeUpdate{Title: &title}))
	link := func(from, to string) string {
		require.NoError(t, s.AddDependency(ctx, &model.Dependency{FromID: from, ToID: to,
			DepType: model.DepTypeRelated, CreatedBy: "alice", CreatedAt: time.Now().UTC()}))
		return lastEventID(t, raw, from, model.OpLinkDep)
	}
	tests := []struct{ name, eventID string }{
		{"edit of the lower unlogged task", lastEventID(t, raw, "MTIX-2.1.1", model.OpUpdateField)},
		{"link from it to a shorter unlogged line", link("MTIX-2.1.1", "MTIX-5")},
		{"link to it from a shorter unlogged line", link("MTIX-5", "MTIX-2.1.1")},
	}
	ids := []string{tests[0].eventID, tests[1].eventID, tests[2].eventID}
	got, err := s.UnloggedSubjects(ctx, ids)
	require.NoError(t, err)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			subj, ok := got[tt.eventID]
			require.True(t, ok)
			require.Equal(t, "MTIX-2", subj.NodeID, "the topmost unlogged task")
			require.Equal(t, uidOfNode(t, raw, "MTIX-2"), subj.UID)
			require.Empty(t, subj.ParentID)
		})
	}
}

// uidOfNode returns the uid of the task id in raw.
func uidOfNode(t *testing.T, raw *sql.DB, id string) string {
	t.Helper()
	var uid string
	require.NoError(t, raw.QueryRow(`SELECT uid FROM nodes WHERE id = ?`, id).Scan(&uid))
	return uid
}

// TestUnloggedSubjects_UnlinkOfALinkToTheUnloggedTask (review r1 F5): the
// unlink of a link from logged MTIX-1 to unlogged MTIX-2 depends on MTIX-2.
func TestUnloggedSubjects_UnlinkOfALinkToTheUnloggedTask(t *testing.T) {
	ctx := context.Background()
	s, raw := unloggedFixture(t)
	require.NoError(t, s.AddDependency(ctx, &model.Dependency{FromID: "MTIX-1", ToID: "MTIX-2",
		DepType: model.DepTypeBlocks, CreatedBy: "alice", CreatedAt: time.Now().UTC()}))
	require.NoError(t, s.RemoveDependency(ctx, "MTIX-1", "MTIX-2", model.DepTypeBlocks))
	unlink := lastEventID(t, raw, "MTIX-1", model.OpUnlinkDep)
	got, err := s.UnloggedSubjects(ctx, []string{unlink})
	require.NoError(t, err)
	require.Equal(t, "MTIX-2", got[unlink].NodeID)
}

// TestUnloggedSubjects_TaskWithoutUIDAndNoCreation (review r1 F7): a task
// without a uid whose creation (also without one) is not in the log is
// matched by number and reported unlogged, with an empty uid.
func TestUnloggedSubjects_TaskWithoutUIDAndNoCreation(t *testing.T) {
	ctx := context.Background()
	s, raw := unloggedFixture(t)
	mustCreateNode(t, s, "MTIX-5", "")
	title := "retitled"
	require.NoError(t, s.UpdateNode(ctx, "MTIX-5", &store.NodeUpdate{Title: &title}))
	for _, q := range []string{
		`UPDATE nodes SET uid = NULL WHERE id = 'MTIX-5'`,
		`UPDATE sync_events SET uid = NULL WHERE node_id = 'MTIX-5'`,
	} {
		_, err := raw.Exec(q)
		require.NoError(t, err)
	}
	unlog(t, raw, "MTIX-5")
	edit := lastEventID(t, raw, "MTIX-5", model.OpUpdateField)
	logged := lastEventID(t, raw, "MTIX-3", model.OpCreateNode)
	got, err := s.UnloggedSubjects(ctx, []string{edit, logged})
	require.NoError(t, err)
	subj, ok := got[edit]
	require.True(t, ok, "the uid-less task without a logged creation is unlogged")
	require.Equal(t, "MTIX-5", subj.NodeID)
	require.Empty(t, subj.UID)
	require.NotContains(t, got, logged, "MTIX-3, matched by number, is logged")
}

// TestSubjectsGone_TaskOrLinkTargetPurged (review r1 F1): an event whose
// task (by uid, or by number without one), or whose link target, has no
// node any more is gone; one whose task is on the board, even soft-deleted,
// is not, and an id with no sync_events row is left out.
func TestSubjectsGone_TaskOrLinkTargetPurged(t *testing.T) {
	ctx := context.Background()
	s, raw := unloggedFixture(t)
	title := "retitled"
	for _, id := range []string{"MTIX-1", "MTIX-2.1", "MTIX-3"} {
		require.NoError(t, s.UpdateNode(ctx, id, &store.NodeUpdate{Title: &title}))
	}
	require.NoError(t, s.AddDependency(ctx, &model.Dependency{FromID: "MTIX-1", ToID: "MTIX-4",
		DepType: model.DepTypeRelated, CreatedBy: "alice", CreatedAt: time.Now().UTC()}))
	ids := map[string]string{
		"deleted, on the board": lastEventID(t, raw, "MTIX-1", model.OpUpdateField),
		"purged":                lastEventID(t, raw, "MTIX-2.1", model.OpUpdateField),
		"purged, no uid":        lastEventID(t, raw, "MTIX-3", model.OpUpdateField),
		"purged target":         lastEventID(t, raw, "MTIX-1", model.OpLinkDep),
		"no sync_events row":    "no-such-event",
	}
	_, err := raw.Exec(`UPDATE sync_events SET uid = NULL WHERE node_id = 'MTIX-3'`)
	require.NoError(t, err)
	require.NoError(t, s.DeleteNode(ctx, "MTIX-1", false, "alice"))
	for _, q := range []string{
		`DELETE FROM dependencies`,
		`DELETE FROM nodes WHERE id IN ('MTIX-2.1', 'MTIX-3', 'MTIX-4')`,
	} {
		_, err := raw.Exec(q)
		require.NoError(t, err)
	}
	list := make([]string, 0, len(ids))
	for _, id := range ids {
		list = append(list, id)
	}
	gone, err := s.SubjectsGone(ctx, list)
	require.NoError(t, err)
	want := map[string]bool{"purged": true, "purged, no uid": true, "purged target": true}
	for name, id := range ids {
		require.Equal(t, want[name], gone[id], name)
	}
	require.Len(t, gone, len(want))
}
