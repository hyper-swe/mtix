// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests for MTIX-112.17.16.16 round 2: the import merge journals what it
// stored, never what the file held (principle A). Red-first against the
// round 1 code, which journaled the file's values.
package sqlite_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/sync/clock"
)

func storedPriority(t *testing.T, s *sqlite.Store, id string) (priority int, issueType string) {
	t.Helper()
	require.NoError(t, s.WriteDB().QueryRow(
		`SELECT priority, COALESCE(issue_type, '') FROM nodes WHERE id = ?`, id).Scan(&priority, &issueType))
	return priority, issueType
}

// TestImportMerge_EqualHash_PriorityAndIssueTypeDiffer_JournalsNothing: the
// content hash does not cover priority or issue type, so a file with the
// same hash leaves the local values in place; the merge must journal nothing
// for them, now or on a repeat.
func TestImportMerge_EqualHash_PriorityAndIssueTypeDiffer_JournalsNothing(t *testing.T) {
	s := syncedStoreWithTask(t)
	data := exportOf(t, s)
	data.Nodes[0].Priority = 1
	data.Nodes[0].IssueType = string(model.IssueTypeBug)
	require.NoError(t, sqlite.RecomputeExportChecksum(data))
	pBefore, itBefore := storedPriority(t, s, "REC-1")
	pending := countJournal(t, s, `sync_status = 'pending'`)

	mergeFile(t, s, data)
	mergeFile(t, s, data)

	pAfter, itAfter := storedPriority(t, s, "REC-1")
	require.Equal(t, pBefore, pAfter, "the local priority stands")
	require.Equal(t, itBefore, itAfter, "the local issue type stands")
	require.Equal(t, pending, countJournal(t, s, `sync_status = 'pending'`),
		"a value the merge did not write is not journaled, and a repeat emits nothing")
}

// TestImportMerge_CreatedTaskWithUID_CreateEventIDIsTheUID: a created task's
// create event carries the task's uid as its event id, the way every create
// path does, so a hub number collision renumbers it instead of wedging push.
func TestImportMerge_CreatedTaskWithUID_CreateEventIDIsTheUID(t *testing.T) {
	s := syncedStoreWithTask(t)
	other := newTestStore(t)
	uid := clock.MustNewEventID()
	createPlainTask(t, other, "REC-1", 1, "", sameIDTime)
	createPlainTask(t, other, "REC-2", 2, uid, sameIDTime)
	data := exportOf(t, other)

	mergeFile(t, s, data)

	require.Equal(t, 1, countJournal(t, s, `op_type = 'create_node' AND node_id = 'REC-2' AND event_id = ? AND uid = ?`, uid, uid),
		"the create event id is the node uid")
	_, err := s.RenumberForHubRejection(context.Background(), uid)
	require.NoError(t, err, "the hub's renumber outcome finds the merged task by its uid")
}

// TestImportMerge_BlocksDependency_BlocksLocallyAndJournalsTheLinkOnly: a
// blocks dependency the merge inserts blocks its dependent here, as the
// link_dep event does on a peer, so the replicas agree on the status.
func TestImportMerge_BlocksDependency_BlocksLocallyAndJournalsTheLinkOnly(t *testing.T) {
	s := syncedStoreWithTask(t)
	createPlainTask(t, s, "REC-2", 2, "", sameIDTime)
	markJournalPushed(t, s)
	data := exportOf(t, s)
	data.Dependencies = append(data.Dependencies, struct {
		FromID    string `json:"from_id"`
		ToID      string `json:"to_id"`
		DepType   string `json:"dep_type"`
		CreatedAt string `json:"created_at"`
	}{"REC-2", "REC-1", "blocks", sameIDTime.Format(time.RFC3339)})
	require.NoError(t, sqlite.RecomputeExportChecksum(data))

	mergeFile(t, s, data)

	var status string
	require.NoError(t, s.WriteDB().QueryRow(`SELECT status FROM nodes WHERE id = 'REC-1'`).Scan(&status))
	require.Equal(t, "blocked", status, "the merge auto-blocks the dependent")
	require.Equal(t, 1, countJournal(t, s, `op_type = 'link_dep' AND sync_status = 'pending'`))
	require.Zero(t, countJournal(t, s, `op_type = 'transition_status' AND sync_status = 'pending'`),
		"peers derive the block from the link, so no transition is journaled")
}

func retitledFile(t *testing.T, s *sqlite.Store, title string) *sqlite.ExportData {
	t.Helper()
	return retitled(t, s, "REC-1", title)
}

// TestImportMerge_AssumeSynced_JournalsOnAStoreWithoutAMark: transient hub
// evidence (an exported DSN) makes the merge journal although the store holds
// no sync mark yet.
func TestImportMerge_AssumeSynced_JournalsOnAStoreWithoutAMark(t *testing.T) {
	s := newTestStore(t)
	createPlainTask(t, s, "REC-1", 1, "", sameIDTime)
	before := countJournal(t, s, `sync_status = 'pending'`)

	_, _, err := s.ImportReconcile(context.Background(), retitledFile(t, s, "Imported"),
		sqlite.ImportReconcileOptions{Mode: sqlite.ImportModeMerge, AssumeSynced: true})

	require.NoError(t, err)
	require.Equal(t, before+1, countJournal(t, s, `sync_status = 'pending'`), "the title change is journaled")
}

// TestImportMerge_NoJournal_WritesNoEventOnASyncedStore: a merge of a copy of
// the shared board (a relay bootstrap snapshot) journals nothing.
func TestImportMerge_NoJournal_WritesNoEventOnASyncedStore(t *testing.T) {
	s := syncedStoreWithTask(t)

	_, _, err := s.ImportReconcile(context.Background(), retitledFile(t, s, "Imported"),
		sqlite.ImportReconcileOptions{Mode: sqlite.ImportModeMerge, NoJournal: true})

	require.NoError(t, err)
	require.Equal(t, "Imported", titleOf(t, s, "REC-1"))
	require.Zero(t, countJournal(t, s, `sync_status = 'pending'`))
}

// TestImportMerge_Comment_CarriesTheAnnotationID: a merged comment's event id
// is the annotation id from the file, so the hub that holds the comment keeps
// one copy, and a peer that holds the annotation already does not add another.
func TestImportMerge_Comment_CarriesTheAnnotationID(t *testing.T) {
	importer, peer := peerPair(t, 1)
	markJournalPushed(t, importer)
	annID := clock.MustNewEventID()
	data := exportOf(t, importer)
	data.Nodes[0].Annotations = []model.Annotation{{ID: annID, Author: "bob", Text: "note", CreatedAt: sameIDTime}}
	require.NoError(t, sqlite.RecomputeExportChecksum(data))
	_, err := peer.WriteDB().Exec(`UPDATE nodes SET annotations = ? WHERE id = 'REC-1'`,
		`[{"id":"`+annID+`","author":"bob","text":"note","created_at":"2026-09-24T08:00:00Z","resolved":false}]`)
	require.NoError(t, err)

	mergeFile(t, importer, data)
	replicate(t, importer, peer)

	require.Equal(t, 1, countJournal(t, importer, `op_type = 'comment' AND event_id = ?`, annID))
	var raw string
	require.NoError(t, peer.WriteDB().QueryRow(`SELECT annotations FROM nodes WHERE id = 'REC-1'`).Scan(&raw))
	require.Equal(t, 1, strings.Count(raw, annID), "the peer holds one copy of the comment")
}

func recomputeChecksum(d *sqlite.ExportData) error { return sqlite.RecomputeExportChecksum(d) }

func mergeErr(s *sqlite.Store, d *sqlite.ExportData) error {
	_, _, err := s.ImportReconcile(context.Background(), d, sqlite.ImportReconcileOptions{Mode: sqlite.ImportModeMerge})
	return err
}
