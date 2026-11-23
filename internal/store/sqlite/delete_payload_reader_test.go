// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func deleteReplayFixture(t *testing.T, cascade bool) ([]*model.SyncEvent, model.SyncEvent) {
	t.Helper()
	origin, db := mutationTestStore(t)
	sqlite.SeedDecisionDeleteTestMachineHash(t, db)
	mustCreateNode(t, origin, "MTIX-1", "")
	mustCreateNode(t, origin, "MTIX-1.1", "MTIX-1")
	creates, err := origin.ReadPendingEvents(t.Context(), 10)
	require.NoError(t, err)
	require.Len(t, creates, 2)
	require.NoError(t, origin.DeleteNode(t.Context(), "MTIX-1", cascade, "alice"))
	last := creates[len(creates)-1]
	deletes, err := origin.ReadPendingEventsAfter(t.Context(), last.LamportClock, last.EventID, 10)
	require.NoError(t, err)
	require.NotEmpty(t, deletes)
	for i, e := range deletes {
		var fields map[string]any
		require.NoError(t, json.Unmarshal(e.Payload, &fields))
		assert.Equal(t, e.NodeID, fields["path"])
		assert.Equal(t, cascade && i == 0, fields["cascade"])
	}
	return creates, *deletes[0]
}

func TestDeletePayload_BaseReader(t *testing.T) {
	for _, cascade := range []bool{false, true} {
		t.Run(fmt.Sprint(cascade), func(t *testing.T) {
			creates, del := deleteReplayFixture(t, cascade)
			for _, payload := range []json.RawMessage{del.Payload, json.RawMessage("{}")} {
				replica, _ := mutationTestStore(t)
				pullEvents(t, replica, creates)
				del.Payload = payload
				pullEvents(t, replica, []*model.SyncEvent{&del})
				_, err := replica.GetNode(t.Context(), "MTIX-1")
				require.ErrorIs(t, err, model.ErrNotFound)
				_, err = replica.GetNode(t.Context(), "MTIX-1.1")
				require.NoError(t, err)
			}
		})
	}
}

func TestDeletePayload_ReleasedReader054(t *testing.T) {
	reader := os.Getenv("MTIX_TEST_RELEASE_054")
	if reader == "" {
		reader = compat054Binary(t)
	} else {
		raw, err := os.ReadFile(reader)
		require.NoError(t, err)
		require.Equal(t, "5ded6a3244e33a1ca2bbd1f449fd203cc8f207753ad0ba599fc7ce82fa433998", fmt.Sprintf("%x", sha256.Sum256(raw)))
	}
	creates, del := deleteReplayFixture(t, true)
	for _, payload := range []json.RawMessage{del.Payload, json.RawMessage("{}")} {
		project := readerProject054(t)
		path := filepath.Join(project, ".mtix", "data", "mtix.db")
		replica, err := sqlite.New(path, slog.Default())
		require.NoError(t, err)
		pullEvents(t, replica, creates)
		del.Payload = payload
		raw, err := json.Marshal(del)
		require.NoError(t, err)
		require.NoError(t, replica.WithTx(t.Context(), func(tx *sql.Tx) error {
			return sqlite.QuarantineEvent(t.Context(), tx, sqlite.QuarantinedEvent{EventID: del.EventID, RawEvent: string(raw), Source: "pull", Reason: "retry delete"})
		}))
		require.NoError(t, replica.Close())
		// The released pull commits its retry before checking for a hub DSN.
		out, code := runReader054(t, reader, project, "sync", "pull")
		require.NotZero(t, code)
		require.Contains(t, out, "no DSN configured")
		verifyReleasedDelete(t, path, del.EventID)
	}
}

func verifyReleasedDelete(t *testing.T, path, eventID string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()
	var applied, queued, deleted, childLive int
	require.NoError(t, db.QueryRowContext(t.Context(), `SELECT
 (SELECT count(*) FROM applied_events WHERE event_id=?),
 (SELECT count(*) FROM sync_quarantine WHERE event_id=?),
 (SELECT count(*) FROM nodes WHERE id='MTIX-1' AND deleted_at IS NOT NULL AND deleted_by='alice'),
 (SELECT count(*) FROM nodes WHERE id='MTIX-1.1' AND deleted_at IS NULL)`, eventID, eventID).Scan(&applied, &queued, &deleted, &childLive))
	require.Equal(t, []int{1, 0, 1, 1}, []int{applied, queued, deleted, childLive})
}
