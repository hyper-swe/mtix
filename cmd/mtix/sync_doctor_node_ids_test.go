// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// holdInvalidIDs gives the peer store n local tasks whose id is outside the
// node id grammar, as a sync before MTIX-187 could bring in, and one pulled
// event quarantined for such an id next to one quarantined for another
// reason.
func holdInvalidIDs(t *testing.T, n int) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	for i := 1; i <= n; i++ {
		require.NoError(t, app.store.CreateNode(ctx, &model.Node{
			ID: fmt.Sprintf("TEST-%dé", i), Project: "TEST", Seq: i, Title: "synced task",
			Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
			NodeType: model.NodeTypeIssue, ContentHash: "h", CreatedAt: now, UpdatedAt: now,
		}))
	}
	require.NoError(t, app.store.WithTx(ctx, func(tx *sql.Tx) error {
		for i, reason := range []string{
			`envelope validation: invalid node id: event e0 node_id "TEST-1é.4": invalid node id`,
			"constraint failed: FOREIGN KEY constraint failed",
		} {
			if err := sqlite.QuarantineEvent(ctx, tx, sqlite.QuarantinedEvent{
				EventID: fmt.Sprintf("0193fb00-0000-7000-8000-00000000000%d", i), Source: "pull",
				RawEvent: `{"lamport_clock":1}`, Reason: reason,
				FirstSeen: "2026-09-25T00:00:00Z", LastAttempt: "2026-09-25T00:00:00Z",
			}); err != nil {
				return err
			}
		}
		return nil
	}))
}

// TestRunSyncStatus_InvalidNodeIDs_ShownInTableAndJSON: `mtix sync status`
// counts the quarantined events refused for an invalid node id and the
// local tasks whose id is outside the grammar (MTIX-187).
func TestRunSyncStatus_InvalidNodeIDs_ShownInTableAndJSON(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	holdInvalidIDs(t, 2)

	var out, errOut bytes.Buffer
	app.jsonOutput = true
	require.NoError(t, runSyncStatus(ctx, &out, &errOut))
	var got map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &got))
	require.Equal(t, float64(2), got["quarantined_events"])
	require.Equal(t, float64(1), got["quarantined_invalid_ids"])
	require.Equal(t, float64(2), got["invalid_node_ids"])

	out.Reset()
	app.jsonOutput = false
	require.NoError(t, runSyncStatus(ctx, &out, &errOut))
	require.Regexp(t, regexp.MustCompile(`(?m)^quarantined invalid ids\s+1$`), out.String())
	require.Regexp(t, regexp.MustCompile(`(?m)^invalid local ids\s+2$`), out.String())
}

// TestRunSyncDoctor_InvalidNodeIDs_WarnsAndNamesFix: the "valid node ids"
// check passes on a store whose ids are all inside the grammar, and warns
// (the doctor still passes it) naming the tasks, their uids and the fix
// when any is not. The quarantine check names the invalid-id action when
// a quarantined event was refused for an invalid node id (MTIX-187).
func TestRunSyncDoctor_InvalidNodeIDs_WarnsAndNamesFix(t *testing.T) {
	t.Run("none", func(t *testing.T) {
		initTestApp(t)
		t.Setenv(transport.EnvDSN, "")
		var out bytes.Buffer
		app.jsonOutput = true
		require.ErrorIs(t, runSyncDoctor(context.Background(), &out, nil, transport.Options{}), errDoctorChecksFailed)
		check := doctorCheckFromJSON(t, out.Bytes(), nodeIDCheckName)
		require.True(t, check.Pass)
		require.False(t, check.Warn)
		require.Equal(t, "ok", check.Detail)
	})
	t.Run("seven held", func(t *testing.T) {
		initTestApp(t)
		t.Setenv(transport.EnvDSN, "")
		holdInvalidIDs(t, 7)
		var out bytes.Buffer
		app.jsonOutput = true
		require.ErrorIs(t, runSyncDoctor(context.Background(), &out, nil, transport.Options{}), errDoctorChecksFailed)

		check := doctorCheckFromJSON(t, out.Bytes(), nodeIDCheckName)
		require.True(t, check.Pass, "a held task stays usable, so the check warns")
		require.True(t, check.Warn)
		require.Contains(t, check.Detail, "7 local tasks have an id outside the node id grammar")
		require.Contains(t, check.Detail, `"TEST-1é" (uid `)
		require.Contains(t, check.Detail, "and 2 more")
		require.Contains(t, check.Fix, "mtix show <uid>")
		require.Contains(t, check.Fix, "mtix create")

		q := doctorCheckFromJSON(t, out.Bytes(), quarantineCheckName)
		require.False(t, q.Pass)
		require.Contains(t, q.Detail, "(1 of them for an invalid node id)")
		require.Contains(t, q.Detail, "'Quarantined Pulled Events: invalid node id'")
	})
}
