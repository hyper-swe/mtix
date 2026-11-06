// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/sync/clock"
)

// MTIX-112.17.16.16 against a real hub: an import merge into a store that
// syncs reaches the hub, and a hub edit made before the import cannot
// replace the imported value on the next pull. They run in the cloud-contract
// shards (MTIX_PG_TEST_DSN) and skip without a hub.

// mergeFileRetitling writes the local export with node id retitled and
// described, hash and checksum recomputed, and returns its path.
func mergeFileRetitling(t *testing.T, id, title, description string) string {
	t.Helper()
	data, err := app.store.Export(context.Background(), "TEST", "")
	require.NoError(t, err)
	for i := range data.Nodes {
		if data.Nodes[i].ID != id {
			continue
		}
		data.Nodes[i].Title, data.Nodes[i].Description = title, description
		data.Nodes[i].ContentHash = model.ComputeContentHash(title, description,
			data.Nodes[i].Prompt, data.Nodes[i].Acceptance, nil)
	}
	require.NoError(t, sqlite.RecomputeExportChecksum(data))
	raw, err := json.Marshal(data)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "merge.json")
	require.NoError(t, os.WriteFile(path, raw, 0o644))
	return path
}

func importMergeUIDOf(t *testing.T, id string) string {
	t.Helper()
	var uid string
	require.NoError(t, app.store.WriteDB().QueryRow(`SELECT COALESCE(uid, '') FROM nodes WHERE id = ?`, id).Scan(&uid))
	return uid
}

// hubTitleEdit is a title edit by another author, older than any import now.
func hubTitleEdit(t *testing.T, nodeID, uid, title string) *model.SyncEvent {
	t.Helper()
	payload, err := model.EncodePayload(&model.UpdateFieldPayload{FieldName: "title", NewValue: json.RawMessage(strconv.Quote(title))})
	require.NoError(t, err)
	eid, err := clock.NewEventID()
	require.NoError(t, err)
	return &model.SyncEvent{EventID: eid, ProjectPrefix: "TEST", NodeID: nodeID, UID: uid, OpType: model.OpUpdateField,
		Payload: payload, WallClockTS: 1000, LamportClock: 2, VectorClock: model.VectorClock{"remote-author": 2},
		AuthorID: "remote-author", AuthorMachineHash: "ffffffffffffffff"}
}

// seedPushedTask creates a task and pushes it, so the store has synced.
func seedPushedTask(t *testing.T, pool *transport.Pool, title string) {
	t.Helper()
	require.NoError(t, runCreate(title, "", "", 3, "", "", "", "", ""))
	var stderr bytes.Buffer
	_, err := pushLoop(context.Background(), &stderr, pool, app.store)
	require.NoError(t, err, stderr.String())
}

func hubHasTitleEvent(t *testing.T, pool *transport.Pool, title string) bool {
	t.Helper()
	events, _, err := pool.PullEvents(context.Background(), transport.PullCursor{}, 1000)
	require.NoError(t, err)
	for _, e := range events {
		if e.OpType == model.OpUpdateField && bytes.Contains(e.Payload, []byte(strconv.Quote(title))) {
			return true
		}
	}
	return false
}

// TestCloudPath_ImportMerge_ReachesHubAndHubEditCannotReplaceIt: the import's
// title is pushed, and the older hub edit, pulled afterwards, does not
// replace it. On main the import wrote no journal row, so the hub never saw
// it and the pull replaced it with the hub's title, with no notice.
func TestCloudPath_ImportMerge_ReachesHubAndHubEditCannotReplaceIt(t *testing.T) {
	pool := openCmdHub(t)
	initTestApp(t)
	ctx := context.Background()
	seedPushedTask(t, pool, "Seed")
	pushToHub(t, pool, hubTitleEdit(t, "TEST-1", importMergeUIDOf(t, "TEST-1"), "Hub title"))
	path := mergeFileRetitling(t, "TEST-1", "Imported title", "")

	_ = captureStdout(t, func() { require.NoError(t, runImport(path, importFlags{mode: "merge", force: true})) })
	var stderr bytes.Buffer
	_, err := pushLoop(ctx, &stderr, pool, app.store)
	require.NoError(t, err, stderr.String())
	_, _, err = pullLoop(ctx, testIngest(&stderr), pool, app.store, transport.PullCursor{}, 100)
	require.NoError(t, err, stderr.String())

	var title string
	require.NoError(t, app.store.WriteDB().QueryRow(`SELECT title FROM nodes WHERE id = 'TEST-1'`).Scan(&title))
	require.Equal(t, "Imported title", title, "the pull does not replace the import with the older hub edit")
	require.True(t, hubHasTitleEvent(t, pool, "Imported title"), "the import reached the hub")
}

// TestCloudPath_ImportMerge_QueueCapFixClearsTheRefusal: with the queue too
// full for the import's events, mtix import --mode merge refuses, naming
// sync.max_queue_size and mtix sync push; running that push drains the queue
// and the same import then succeeds.
func TestCloudPath_ImportMerge_QueueCapFixClearsTheRefusal(t *testing.T) {
	pool := openCmdHub(t)
	initTestApp(t)
	ctx := context.Background()
	seedPushedTask(t, pool, "First")
	seedPushedTask(t, pool, "Second")
	for _, title := range []string{"First edited", "First edited again"} {
		require.NoError(t, app.store.UpdateNode(ctx, "TEST-1", &store.NodeUpdate{Title: &title}))
	}
	_, err := app.store.WriteDB().Exec(`UPDATE meta SET value = '3' WHERE key = 'sync.max_queue_size'`)
	require.NoError(t, err)
	path := mergeFileRetitling(t, "TEST-2", "Imported title", "Imported description")

	err = runImport(path, importFlags{mode: "merge", force: true})

	require.Error(t, err)
	require.ErrorIs(t, err, model.ErrSyncQueueFull)
	require.Contains(t, err.Error(), "sync.max_queue_size")
	require.Contains(t, err.Error(), "mtix sync push")
	var stderr bytes.Buffer
	_, pushErr := pushLoop(ctx, &stderr, pool, app.store) // the command the refusal names
	require.NoError(t, pushErr, stderr.String())
	_ = captureStdout(t, func() { require.NoError(t, runImport(path, importFlags{mode: "merge", force: true})) })
}
