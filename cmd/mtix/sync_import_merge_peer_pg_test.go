// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/sync/clock"
)

// MTIX-112.17.16.16 round 2 against a real hub, with a second peer: the
// importer, the hub and a fresh peer agree on every field the merge touched,
// and a merged create whose number the hub holds under another task is
// renumbered instead of wedging the push. They run in the cloud-contract
// shards (MTIX_PG_TEST_DSN) and skip without a hub.

// importPeerView is the fields a merge can touch, read from the current app.
type importPeerView struct {
	Title, IssueType, Status, DeferUntil string
	Priority                             int
}

func peerView(t *testing.T, id string) importPeerView {
	t.Helper()
	var v importPeerView
	require.NoError(t, app.store.QueryRow(context.Background(), `SELECT title, COALESCE(issue_type, ''), status,
		COALESCE(defer_until, ''), priority FROM nodes WHERE id = ?`, id).Scan(&v.Title, &v.IssueType, &v.Status, &v.DeferUntil, &v.Priority))
	return v
}

// pullAsFreshPeer switches to a new replica and pulls the whole hub into it.
func pullAsFreshPeer(t *testing.T, pool *transport.Pool) {
	t.Helper()
	t.Setenv(sqlite.AuthorIDEnv, "agent-peer")
	initTestApp(t)
	var stderr bytes.Buffer
	_, _, err := pullLoop(context.Background(), testIngest(&stderr), pool, app.store, transport.PullCursor{}, 1000)
	require.NoError(t, err, stderr.String())
}

func writeBoard(t *testing.T, data *sqlite.ExportData) string {
	t.Helper()
	require.NoError(t, sqlite.RecomputeExportChecksum(data))
	raw, err := json.Marshal(data)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "board.json")
	require.NoError(t, os.WriteFile(path, raw, 0o644))
	return path
}

func hubPayloadHas(t *testing.T, pool *transport.Pool, op model.OpType, needle string) bool {
	t.Helper()
	events, _, err := pool.PullEvents(context.Background(), transport.PullCursor{}, 1000)
	require.NoError(t, err)
	for _, e := range events {
		if e.OpType == op && strings.Contains(string(e.Payload), needle) {
			return true
		}
	}
	return false
}

// TestCloudPath_ImportMerge_EqualHash_NothingReachesHubAndPeerAgrees: a file
// with the same content hash and another priority and issue type leaves the
// importer's values; no event carries the file's, and a fresh peer holds what
// the importer holds. Repeating the import adds nothing.
func TestCloudPath_ImportMerge_EqualHash_NothingReachesHubAndPeerAgrees(t *testing.T) {
	pool := openCmdHub(t)
	t.Setenv(sqlite.AuthorIDEnv, "agent-importer")
	initTestApp(t)
	seedPushedTask(t, pool, "Seed")
	data, err := app.store.Export(context.Background(), "TEST", "")
	require.NoError(t, err)
	data.Nodes[0].Priority, data.Nodes[0].IssueType = 1, string(model.IssueTypeBug)
	path := writeBoard(t, data)

	for i := 0; i < 2; i++ {
		_ = captureStdout(t, func() { require.NoError(t, runImport(path, importFlags{mode: "merge", force: true})) })
		var stderr bytes.Buffer
		_, err = pushLoop(context.Background(), &stderr, pool, app.store)
		require.NoError(t, err, stderr.String())
	}

	importer := peerView(t, "TEST-1")
	require.Equal(t, 3, importer.Priority, "the importer keeps its own priority")
	require.False(t, hubPayloadHas(t, pool, model.OpUpdateField, `"priority"`), "no event carries the file's priority")
	require.False(t, hubPayloadHas(t, pool, model.OpUpdateField, `"issue_type"`), "no event carries the file's issue type")
	pullAsFreshPeer(t, pool)
	require.Equal(t, importer, peerView(t, "TEST-1"))
}

// TestCloudPath_ImportMerge_ChangesDeferAndBlock_PeerAgreesOnEveryField: a
// merge that retitles, reprioritises and defers a task with a wake time,
// adds a task and a blocks dependency, leaves a fresh peer with the
// importer's values.
func TestCloudPath_ImportMerge_ChangesDeferAndBlock_PeerAgreesOnEveryField(t *testing.T) {
	pool := openCmdHub(t)
	t.Setenv(sqlite.AuthorIDEnv, "agent-importer")
	initTestApp(t)
	seedPushedTask(t, pool, "Seed")
	seedPushedTask(t, pool, "Second")
	wake := time.Date(2031, 3, 4, 5, 6, 7, 0, time.UTC).Format(time.RFC3339)
	data, err := app.store.Export(context.Background(), "TEST", "")
	require.NoError(t, err)
	data.Nodes[0].Title, data.Nodes[0].Priority = "Imported", 1
	data.Nodes[0].ContentHash = model.ComputeContentHash("Imported", "", "", "", nil)
	data.Nodes[1].Title, data.Nodes[1].Status, data.Nodes[1].DeferUntil = "Deferred", string(model.StatusDeferred), wake
	data.Nodes[1].ContentHash = model.ComputeContentHash("Deferred", "", "", "", nil)
	data.Dependencies = append(data.Dependencies, struct {
		FromID    string `json:"from_id"`
		ToID      string `json:"to_id"`
		DepType   string `json:"dep_type"`
		CreatedAt string `json:"created_at"`
	}{"TEST-2", "TEST-1", "blocks", wake})
	path := writeBoard(t, data)

	_ = captureStdout(t, func() {
		require.NoError(t, runImport(path, importFlags{mode: "merge", force: true, prefer: "theirs"}))
	})
	var stderr bytes.Buffer
	_, err = pushLoop(context.Background(), &stderr, pool, app.store)
	require.NoError(t, err, stderr.String())

	first, second := peerView(t, "TEST-1"), peerView(t, "TEST-2")
	require.Equal(t, "blocked", first.Status, "the importer blocks the dependent as the peer will")
	require.Equal(t, wake, second.DeferUntil)
	pullAsFreshPeer(t, pool)
	require.Equal(t, first, peerView(t, "TEST-1"))
	require.Equal(t, second, peerView(t, "TEST-2"))
}

// TestCloudPath_ImportMerge_TaskOnANumberTheHubHolds_IsNamedUnsentAndNeverWedgesThePush:
// the hub holds TEST-2 under another task; a merge brings a different TEST-2.
// The merge journals no create (ruling 1), so the push neither fails nor
// delivers it: the merged task stays on this machine until its author pushes it.
func TestCloudPath_ImportMerge_TaskOnANumberTheHubHolds_IsNamedUnsentAndNeverWedgesThePush(t *testing.T) {
	pool := openCmdHub(t)
	t.Setenv(sqlite.AuthorIDEnv, "agent-importer")
	initTestApp(t)
	seedPushedTask(t, pool, "Seed")
	theirs := clock.MustNewEventID()
	payload, err := model.EncodePayload(&model.CreateNodePayload{Title: "Teammate task", NodeType: model.NodeTypeForDepth(0)})
	require.NoError(t, err)
	pushToHub(t, pool, &model.SyncEvent{EventID: theirs, UID: theirs, ProjectPrefix: "TEST", NodeID: "TEST-2",
		OpType: model.OpCreateNode, Payload: payload, WallClockTS: 1000, LamportClock: 5,
		VectorClock: model.VectorClock{"remote-author": 5}, AuthorID: "remote-author", AuthorMachineHash: "ffffffffffffffff"})
	mine := clock.MustNewEventID()
	data, err := app.store.Export(context.Background(), "TEST", "")
	require.NoError(t, err)
	extra := data.Nodes[0]
	extra.ID, extra.Seq, extra.UID, extra.Title = "TEST-2", 2, mine, "Merged task"
	extra.ContentHash = model.ComputeContentHash("Merged task", "", "", "", nil)
	data.Nodes = append(data.Nodes, extra)
	data.NodeCount = len(data.Nodes)
	path := writeBoard(t, data)
	out := captureStdout(t, func() { require.NoError(t, runImport(path, importFlags{mode: "merge", force: true})) })
	require.Contains(t, out, "TEST-2", "the merged task is named unsent")

	var stderr bytes.Buffer
	_, err = pushLoop(context.Background(), &stderr, pool, app.store)

	require.NoError(t, err, stderr.String())
	require.Zero(t, countTestRows(t, `SELECT COUNT(*) FROM sync_events WHERE op_type = 'create_node' AND node_id = 'TEST-2'`),
		"the merge journaled no create, so nothing is held or pushed")
	events, _, err := pool.PullEvents(context.Background(), transport.PullCursor{}, 1000)
	require.NoError(t, err)
	for _, e := range events {
		require.False(t, e.OpType == model.OpCreateNode && e.UID == mine, "the merged task never reached the hub")
	}
}
