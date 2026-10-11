// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/stretchr/testify/require"
)

type pushHandoffFrame struct {
	Kind    string `json:"kind"`
	PID     int    `json:"pid"`
	EventID string `json:"event_id,omitempty"`
	Pending *int   `json:"pending,omitempty"`
}

func pushHandoffEmit(t *testing.T, kind, eventID string) {
	t.Helper()
	require.NoError(t, json.NewEncoder(os.Stdout).Encode(pushHandoffFrame{Kind: kind, PID: os.Getpid(), EventID: eventID}))
}
func pushHandoffRead(t *testing.T) bool {
	t.Helper()
	_, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if errors.Is(err, io.EOF) {
		return false
	}
	require.NoError(t, err)
	return true
}

// This per-instance writer parks after the real native empty read and summary,
// while runSyncPush still owns its deferred OS lock. Later summaries pass through.
type pushHandoffSummary struct {
	t      *testing.T
	parked bool
}

func (w *pushHandoffSummary) Write(p []byte) (int, error) {
	const empty = "push complete: 0 events pushed across 0 batches (0 inserted, 0 already on the hub); 0 renumbered, 0 conflicts surfaced\n"
	if !w.parked && string(p) == empty {
		w.parked = true
		pushHandoffEmit(w.t, "empty", "")
		pushHandoffRead(w.t)
	}
	return len(p), nil
}
func TestPushHandoffProcessHelper(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "--" {
		return
	}
	mode := os.Args[len(os.Args)-1]
	dir, err := os.Getwd()
	require.NoError(t, err)
	store := newDaemonTestStore(t, dir)
	app = appContext{mtixDir: dir, store: store}
	if mode == "holder" || mode == "cap-holder" {
		var output io.Writer = &pushHandoffSummary{t: t}
		if mode == "cap-holder" {
			output = &pushHandoffCapSummary{t: t}
		}
		require.NoError(t, runSyncPush(context.Background(), output, os.Stderr, nil, transport.Options{InsecureTLS: true}, false))
		pushHandoffEmit(t, "done", "")
		return
	}
	pushHandoffEmit(t, "ready", "")
	if !pushHandoffRead(t) {
		return
	}
	pushHandoffCommand(t, mode)
}

func pushHandoffCommand(t *testing.T, mode string) {
	t.Helper()
	switch mode {
	case "protocol":
		pushHandoffEmit(t, "done", "")
	case "contender", "contender-1":
		pushHandoffContender(t, 1)
	case "contender-2":
		pushHandoffContender(t, 2)
	case "contender-3":
		pushHandoffContender(t, 3)
	case "status":
		pushHandoffStatus(t)
	case "later-push":
		require.NoError(t, runSyncPush(context.Background(), io.Discard, os.Stderr, nil, transport.Options{InsecureTLS: true}, false))
		pushHandoffEmit(t, "done", "")
	default:
		t.Fatalf("unknown owned helper mode %q", mode)
	}
}
func pushHandoffContender(t *testing.T, seq int) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	require.NoError(t, app.store.CreateNode(ctx, &model.Node{ID: fmt.Sprintf("HAND-%d", seq), Project: "HAND", Seq: seq, Title: "Handoff task", Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1, NodeType: model.NodeTypeEpic, CreatedAt: now, UpdatedAt: now}))
	events, err := app.store.ReadPendingEventsAfter(ctx, 0, "", 10)
	require.NoError(t, err)
	require.Len(t, events, 1, "actual committed native journal event")
	var stdout, stderr bytes.Buffer
	require.NoError(t, runSyncPush(ctx, &stdout, &stderr, nil, transport.Options{InsecureTLS: true}, false))
	require.Contains(t, stderr.String(), "push lock is held; skipping")
	require.Empty(t, stdout.String(), "contender must not perform a push")
	pushHandoffEmit(t, "skipped", events[0].EventID)
}
