// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func evAt(clock int64, id string) *model.SyncEvent {
	return &model.SyncEvent{EventID: id, LamportClock: clock}
}

// TestAppliedBound_Holds_FollowsTheCursorKeysetOrderAndTheJournal pins which hub
// events a store counts as applied: those at or below its pull cursor in the
// pull's own (lamport_clock, event_id) order, and those in its own journal. An
// event above the cursor that the journal lacks is not applied; a Lamport-only
// cursor covers only the clocks below it.
func TestAppliedBound_Holds_FollowsTheCursorKeysetOrderAndTheJournal(t *testing.T) {
	cursor := appliedBound{cursor: transport.PullCursor{Lamport: 5, EventID: "m"}, own: map[string]bool{"origin": true}}
	lamportOnly := appliedBound{cursor: transport.PullCursor{Lamport: 5}, own: map[string]bool{}}
	tests := []struct {
		name  string
		bound appliedBound
		event *model.SyncEvent
		want  bool
	}{
		{"below the cursor clock", cursor, evAt(4, "z"), true},
		{"same clock, id below", cursor, evAt(5, "a"), true},
		{"the cursor event itself", cursor, evAt(5, "m"), true},
		{"same clock, id above: a late event", cursor, evAt(5, "n"), false},
		{"above the cursor clock", cursor, evAt(6, "a"), false},
		{"an event the store originated, above the cursor", cursor, evAt(9, "origin"), true},
		{"empty cursor holds nothing", appliedBound{own: map[string]bool{}}, evAt(1, "a"), false},
		{"empty cursor, own event", appliedBound{own: map[string]bool{"a": true}}, evAt(1, "a"), true},
		{"lamport-only cursor: clock below", lamportOnly, evAt(4, "z"), true},
		{"lamport-only cursor: the same clock is not held", lamportOnly, evAt(5, "a"), false},
		{"lamport-only cursor: clock above", lamportOnly, evAt(6, "a"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.bound.holds(tt.event))
		})
	}
}

// TestBoundedHub_PullEvents_ServesOnlyHeldEventsAcrossEmptyPages: a page whose
// events are all outside the bound is not the end of the log; the wrapper reads
// on, so a held event behind a run of unheld ones is still served, and the
// result is the same at any page size.
func TestBoundedHub_PullEvents_ServesOnlyHeldEventsAcrossEmptyPages(t *testing.T) {
	var events []*model.SyncEvent
	for i := int64(1); i <= 12; i++ {
		events = append(events, evAt(i, fmt.Sprintf("e%02d", i)))
	}
	bound := appliedBound{cursor: transport.PullCursor{Lamport: 2, EventID: "e02"}, own: map[string]bool{"e11": true}}
	for _, limit := range []int{1, 2, 3, 5, 100} {
		t.Run(fmt.Sprintf("limit %d", limit), func(t *testing.T) {
			hub := boundedHub{cursorPuller: hubOf(events), bound: bound}
			var got []string
			after := transport.PullCursor{}
			for {
				page, more, err := hub.PullEvents(context.Background(), after, limit)
				require.NoError(t, err)
				for _, e := range page {
					got = append(got, e.EventID)
				}
				if !more || len(page) == 0 {
					break
				}
				after = transport.CursorAt(page[len(page)-1])
			}
			assert.Equal(t, []string{"e01", "e02", "e11"}, got)
		})
	}
}

func TestBoundedHub_PullEvents_HubErrorIsReturned(t *testing.T) {
	failing := hubOf(nil)
	failing.pullErr = fmt.Errorf("hub down")
	_, _, err := boundedHub{cursorPuller: failing}.PullEvents(context.Background(), transport.PullCursor{}, 10)
	require.ErrorContains(t, err, "hub down")
}

func hubTitleEdit(t *testing.T, base []*model.SyncEvent, id, title string) []*model.SyncEvent {
	t.Helper()
	return hubEventsAfter(t, base, func(ctx context.Context, st *sqlite.Store) {
		require.NoError(t, st.UpdateNode(ctx, id, &store.NodeUpdate{Title: &title}))
	})
}

// TestEnrolWorktree_RealPath_UnjournaledLocalChange_HubEditedSameField_Refuses is
// the F17 scenario: an import merge changed a journalled node's title with no
// journal row, tasks.json equals the store, and a teammate then retitled the
// same node on the hub. Pulling the hub over a copy of the store would make the
// two exports match and hide the loss. The store is compared with a clone of
// only the events it applied, so the local title is seen, the enrolment refuses
// with a report, and nothing moved.
func TestEnrolWorktree_RealPath_UnjournaledLocalChange_HubEditedSameField_Refuses(t *testing.T) {
	ctx := context.Background()
	root, writer, hubEvents := writerWorktreeOnHub(t)
	_, err := writer.WriteDB().ExecContext(ctx, `UPDATE nodes SET title = 'imported title' WHERE id = 'TEST-1'`)
	require.NoError(t, err)
	writeBoardFromStore(t, root, writer)
	require.NoError(t, writer.Close())
	hub := hubOf(append(append([]*model.SyncEvent(nil), hubEvents...), hubTitleEdit(t, hubEvents, "TEST-1", "teammate title")...))
	svc, recorded := enrolOverHub(fakeDial(hub))
	board, storeBefore := fileBytes(t, boardFilePath(root)), treeBytes(t, worktreeDataDir(root))

	_, err = svc.EnrolWorktree(ctx, enrolRequest(root))

	var refusal *service.WorktreeBoardError
	require.ErrorAs(t, err, &refusal)
	assert.Equal(t, service.BoardHubDiffers, refusal.Reason)
	assert.Equal(t, []string{"title"}, refusal.Diff["TEST-1"])
	assert.NotContains(t, string(fileBytes(t, refusal.Report)), "imported title", "ids and field names only")
	assert.NotContains(t, err.Error(), "fix: run mtix sync pull", "a pull would overwrite the unjournaled title")
	assert.Contains(t, err.Error(), "accept-hub "+refusal.Digest())
	assertNothingChangedExceptReport(t, root, storeBefore, board, recorded)
}

// TestEnrolWorktree_RealPath_BehindBoardWithHubEditAndDelete_Enrols: the store
// applied only older events; the hub holds a teammate's edit of one node and a
// deletion of another that this store never saw. Every local state derives from
// hub events, so the enrolment goes on whatever the hub touched.
func TestEnrolWorktree_RealPath_BehindBoardWithHubEditAndDelete_Enrols(t *testing.T) {
	ctx := context.Background()
	root, writer, hubEvents := worktreeWith(t, func(ctx context.Context, st *sqlite.Store) { pushedWork(ctx, t, st) })
	require.NoError(t, writer.Close())
	ahead := hubEventsAfter(t, hubEvents, func(ctx context.Context, st *sqlite.Store) {
		title := "teammate retitle"
		require.NoError(t, st.UpdateNode(ctx, "TEST-1", &store.NodeUpdate{Title: &title}))
		require.NoError(t, st.DeleteNode(ctx, "TEST-2", false, "teammate"))
	})
	svc, recorded := enrolOverHub(fakeDial(hubOf(append(append([]*model.SyncEvent(nil), hubEvents...), ahead...))))

	_, err := svc.EnrolWorktree(ctx, enrolRequest(root))

	require.NoError(t, err)
	assert.True(t, *recorded)
	clone, err := sqlite.New(worktreeDataDir(root), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = clone.Close() })
	n, err := clone.GetNode(ctx, "TEST-1")
	require.NoError(t, err)
	assert.Equal(t, "teammate retitle", n.Title)
	_, err = clone.GetNode(ctx, "TEST-2")
	require.ErrorIs(t, err, model.ErrNotFound)
}

// lateEventStore gives a worktree whose pull cursor is above the clock of every
// event in late, with late on the hub: the store never applied them.
func lateEventStore(t *testing.T, late func(base []*model.SyncEvent) []*model.SyncEvent) (root string, hub pulledHub) {
	t.Helper()
	ctx := context.Background()
	root, writer, hubEvents := writerWorktreeOnHub(t)
	lateEvents := late(hubEvents)
	for _, e := range lateEvents {
		e.LamportClock = 1 // below the cursor the store will hold
	}
	_, err := writer.WriteDB().ExecContext(ctx,
		`INSERT OR REPLACE INTO meta (key, value) VALUES ('meta.sync.last_pulled_clock', '1000'), ('meta.sync.last_pulled_event_id', 'zzzz')`)
	require.NoError(t, err)
	writeBoardFromStore(t, root, writer)
	require.NoError(t, writer.Close())
	return root, hubOf(append(append([]*model.SyncEvent(nil), hubEvents...), lateEvents...))
}

// TestEnrolWorktree_RealPath_LateHubNodeBelowTheCursor_RefusesWithThePullFix: a
// hub event for a node stamped below the store's pull cursor, which the store
// never applied, is a row the bounded clone holds and the store lacks: a false
// refusal, the safe direction. The store holds nothing the clone lacks, so the
// pull then sync --fix is printed (ruling r5 point 1), and nothing else.
func TestEnrolWorktree_RealPath_LateHubNodeBelowTheCursor_RefusesWithThePullFix(t *testing.T) {
	ctx := context.Background()
	root, hub := lateEventStore(t, func(base []*model.SyncEvent) []*model.SyncEvent {
		return hubEventsAfter(t, base, func(ctx context.Context, st *sqlite.Store) {
			require.NoError(t, st.CreateNode(ctx, mkPGNode("TEST-9", "", 0, 9, "late node")))
		})
	})
	svc, recorded := enrolOverHub(fakeDial(hub))

	_, err := svc.EnrolWorktree(ctx, enrolRequest(root))

	var refusal *service.WorktreeBoardError
	require.ErrorAs(t, err, &refusal)
	assert.True(t, refusal.HubRowsOnly())
	assert.Contains(t, err.Error(), "run mtix sync pull, then mtix sync --fix")
	assert.NotContains(t, err.Error(), "holds work the hub does not")
	assert.NotContains(t, err.Error(), "mtix sync push")
	assert.NotContains(t, err.Error(), "accept-hub")
	assert.False(t, *recorded)
}

// TestEnrolWorktree_RealPath_LateHubEditBelowTheCursor_IsTreatedAsLocal: a late
// hub edit of a field the store holds differently cannot be told from a local
// unjournaled change, so it is refused with the local steps and no pull.
func TestEnrolWorktree_RealPath_LateHubEditBelowTheCursor_IsTreatedAsLocal(t *testing.T) {
	ctx := context.Background()
	root, hub := lateEventStore(t, func(base []*model.SyncEvent) []*model.SyncEvent {
		return hubTitleEdit(t, base, "TEST-1", "late title")
	})
	svc, recorded := enrolOverHub(fakeDial(hub))

	_, err := svc.EnrolWorktree(ctx, enrolRequest(root))

	var refusal *service.WorktreeBoardError
	require.ErrorAs(t, err, &refusal)
	assert.False(t, refusal.HubRowsOnly())
	assert.NotContains(t, err.Error(), "fix: run mtix sync pull")
	assert.Contains(t, err.Error(), "(1) keep a copy: mtix backup")
	assert.False(t, *recorded)
}

// TestEnrolWorktree_RealPath_AddressedComment_FullyPushed_Enrols: current and
// ordinary old addressed comment events carry to, which the current clone
// applies. Old backfilled and bulk events omit it and still need the addressee
// exclusion. The comment's author, text and place in the order still compare.
func TestEnrolWorktree_RealPath_AddressedComment_FullyPushed_Enrols(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy=%t", legacy), func(t *testing.T) {
			ctx := context.Background()
			root, writer, hubEvents := worktreeWith(t, func(ctx context.Context, st *sqlite.Store) {
				now := mustTime(t, "2026-06-02T00:00:00Z")
				require.NoError(t, st.CreateNode(ctx, mkPGNode("TEST-2", "", 0, 2, "commented")))
				require.NoError(t, st.AppendAnnotation(ctx, "TEST-2", model.Annotation{ID: "c1", Author: "lane", Text: "plain", CreatedAt: now}))
				require.NoError(t, st.AppendAnnotation(ctx, "TEST-2", model.Annotation{ID: "c2", Author: "lane", Text: "for you", CreatedAt: now.Add(time.Second), Addressee: "lane-b"}))
			})
			if legacy {
				hubEvents = preReplicatedFieldsEvents(t, hubEvents)
			}
			require.NoError(t, writer.Close())
			svc, recorded := enrolOverHub(fakeDial(hubOf(hubEvents)))

			_, err := svc.EnrolWorktree(ctx, enrolRequest(root))

			var refusal *service.WorktreeBoardError
			if errors.As(err, &refusal) {
				t.Logf("DIFF %v", refusal.Diff)
			}
			require.NoError(t, err)
			assert.True(t, *recorded)
			require.NoError(t, withBoardStore(slog.Default(), root, worktreeDataDir(root), func(st *sqlite.Store) error {
				node, readErr := st.GetNode(ctx, "TEST-2")
				if readErr != nil {
					return readErr
				}
				require.Len(t, node.Annotations, 2)
				assert.Equal(t, "lane-b", node.Annotations[1].Addressee)
				return nil
			}))
		})
	}
}

// TestEnrolWorktree_RealPath_ResolvedComment_PushedEnrols_OtherwiseRefuses:
// current pushed resolutions reach a clone. An unjournaled flag change refuses
// on that flag alone; an old resolve event appends a bulk-placeholder row and
// also refuses on annotations.
func TestEnrolWorktree_RealPath_ResolvedComment_PushedEnrols_OtherwiseRefuses(t *testing.T) {
	for _, mode := range []string{"pushed", "unpushed local state", "pre-.15 events"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			root, writer, hubEvents := worktreeWith(t, func(ctx context.Context, st *sqlite.Store) {
				require.NoError(t, st.CreateNode(ctx, mkPGNode("TEST-2", "", 0, 2, "commented")))
				require.NoError(t, st.AppendAnnotation(ctx, "TEST-2", model.Annotation{ID: "c1", Author: "lane", Text: "x", CreatedAt: mustTime(t, "2026-06-02T00:00:00Z")}))
				if mode != "unpushed local state" {
					require.NoError(t, st.ResolveAnnotation(ctx, "TEST-2", "c1", true))
				}
			})
			if mode == "unpushed local state" {
				// An import can change the row without a corresponding hub event.
				_, err := writer.WriteDB().ExecContext(ctx, `UPDATE nodes SET annotations = json_set(annotations, '$[0].resolved', json('true')) WHERE id = 'TEST-2'`)
				require.NoError(t, err)
				writeBoardFromStore(t, root, writer)
			}
			if mode == "pre-.15 events" {
				hubEvents = preReplicatedFieldsEvents(t, hubEvents)
				// Old resolution writes a bulk placeholder, not the original text.
				payload, err := model.EncodePayload(&model.CommentPayload{Body: "annotations bulk update: 1 -> 1 entries"})
				require.NoError(t, err)
				resolution := hubEvents[len(hubEvents)-1]
				require.Equal(t, model.OpComment, resolution.OpType)
				resolution.Payload = payload
			}
			require.NoError(t, writer.Close())
			svc, recorded := enrolOverHub(fakeDial(hubOf(hubEvents)))

			_, err := svc.EnrolWorktree(ctx, enrolRequest(root))

			if mode == "pushed" {
				require.NoError(t, err)
				assert.True(t, *recorded)
				return
			}
			var refusal *service.WorktreeBoardError
			require.ErrorAs(t, err, &refusal)
			assert.Contains(t, refusal.Diff["TEST-2"], "annotations")
			assert.False(t, *recorded)
		})
	}
}

// TestEnrolWorktree_RealPath_AgentOnlyInTheBoardFile_NoExclusionExcuses: tasks.json
// carries an agent row the store export lacks. The file rule has no exclusions,
// so it refuses with the file fix, though the hub rule leaves agents out.
func TestEnrolWorktree_RealPath_AgentOnlyInTheBoardFile_NoExclusionExcuses(t *testing.T) {
	ctx := context.Background()
	root, writer, hubEvents := worktreeWith(t, func(ctx context.Context, st *sqlite.Store) { pushedWork(ctx, t, st) })
	require.NoError(t, writer.Close())
	path := boardFilePath(root)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(fileBytes(t, path), &doc))
	agents, _ := doc["agents"].([]any)
	doc["agents"] = append(agents, map[string]any{"agent_id": "only-in-file"})
	edited, err := json.Marshal(doc)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, edited, 0o600))
	svc, recorded := enrolOverHub(fakeDial(hubOf(hubEvents)))
	board, storeBefore := fileBytes(t, path), treeBytes(t, worktreeDataDir(root))

	_, err = svc.EnrolWorktree(ctx, enrolRequest(root))

	var refusal *service.WorktreeBoardError
	require.ErrorAs(t, err, &refusal)
	assert.Equal(t, service.BoardFileDiffers, refusal.Reason)
	assertNothingChangedExceptReport(t, root, storeBefore, board, recorded)
}

// TestReadAppliedBound_StoreWithoutACursor_HoldsOnlyItsJournal: a store with no
// saved pull cursor has applied nothing but what its journal holds. The bound is
// the empty cursor and the journal ids, never an error and never "everything".
func TestReadAppliedBound_StoreWithoutACursor_HoldsOnlyItsJournal(t *testing.T) {
	ctx := context.Background()
	_, writer, hubEvents := writerWorktreeOnHub(t)
	_, err := writer.WriteDB().ExecContext(ctx, `DELETE FROM meta WHERE key IN ('meta.sync.last_pulled_clock', 'meta.sync.last_pulled_event_id')`)
	require.NoError(t, err)

	bound, err := readAppliedBound(ctx, writer)

	require.NoError(t, err)
	assert.Equal(t, transport.PullCursor{}, bound.cursor)
	require.NotEmpty(t, hubEvents)
	assert.True(t, bound.holds(hubEvents[0]), "an event in the journal is held")
	assert.False(t, bound.holds(evAt(1, "not-in-the-journal")), "nothing else is")
}

// TestReadAppliedBound_ClosedStore_IsAnError: a store that cannot be read has no
// bound; the enrolment fails instead of assuming one.
func TestReadAppliedBound_ClosedStore_IsAnError(t *testing.T) {
	st, err := sqlite.New(filepath.Join(t.TempDir(), "d"), slog.Default())
	require.NoError(t, err)
	require.NoError(t, st.Close())

	_, err = readAppliedBound(context.Background(), st)

	require.ErrorContains(t, err, "read the pull cursor")
}
