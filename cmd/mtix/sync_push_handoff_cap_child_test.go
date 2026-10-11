// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Each actual successful summary is emitted after native pushLoop's final
// empty read. Parent joins that pass's committing/skipping contender before release.
type pushHandoffCapSummary struct {
	t      *testing.T
	passes int
}

func (w *pushHandoffCapSummary) Write(p []byte) (int, error) {
	if !strings.HasPrefix(string(p), "push complete:") {
		return len(p), nil
	}
	expected := "push complete: 1 events pushed across 1 batches (1 inserted, 0 already on the hub); 0 renumbered, 0 conflicts surfaced\n"
	if w.passes == 0 {
		expected = "push complete: 0 events pushed across 0 batches (0 inserted, 0 already on the hub); 0 renumbered, 0 conflicts surfaced\n"
	}
	require.Equal(w.t, expected, string(p), "actual native successful per-pass summary")
	w.passes++
	require.LessOrEqual(w.t, w.passes, 3, "native holder must obey finite cap")
	pushHandoffEmit(w.t, fmt.Sprintf("pass-%d", w.passes), "")
	pushHandoffRead(w.t)
	return len(p), nil
}
func pushHandoffStatus(t *testing.T) {
	t.Helper()
	app.jsonOutput = true
	var stdout, stderr bytes.Buffer
	require.NoError(t, runSyncStatus(context.Background(), &stdout, &stderr))
	var status SyncStatus
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &status), "decode actual public sync status output")
	require.NoError(t, json.NewEncoder(os.Stdout).Encode(pushHandoffFrame{Kind: "status", PID: os.Getpid(), Pending: &status.Pending}))
}
