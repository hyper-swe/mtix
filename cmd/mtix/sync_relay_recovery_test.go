// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests one-pass publishing, degraded transport diagnostics, reset positions,
// republish immutability and operator rendering with owned relay files.
package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/relay/ingest"
	"github.com/hyper-swe/mtix/internal/relay/keyring"
	"github.com/hyper-swe/mtix/internal/relay/tick"
	"github.com/hyper-swe/mtix/internal/service"
)

func relaySegmentBytes(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	root := filepath.Join(dir, tick.PeersDirName, diagnosticSelf, tick.SegmentsDirName)
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	out := make(map[string][]byte, len(entries))
	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		out[path], err = os.ReadFile(path)
		require.NoError(t, err)
	}
	return out
}

func requireRelaySegmentsUnchanged(t *testing.T, before map[string][]byte) {
	t.Helper()
	for path, body := range before {
		after, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, body, after, path)
	}
}

func TestRelayTick_PendingWork_AdvancesPersistentCursorOnce(t *testing.T) {
	dir := initCommandRelay(t, true)
	tail, err := app.store.JournalTail(t.Context())
	require.NoError(t, err)
	out, diagnostic, err := relayCommandOutput(newRelayTickCmd())
	require.NoError(t, err)
	require.Empty(t, diagnostic)
	require.Equal(t, fmt.Sprintf("published %d, applied 0\n", tail), out)
	pos, err := app.store.RelayPushCursor(t.Context())
	require.NoError(t, err)
	require.Equal(t, tail, pos.Seq)
	require.Equal(t, uint64(tail+1), pos.NextRS)
	before := relaySegmentBytes(t, dir)
	require.NotEmpty(t, before)
	out, diagnostic, err = relayCommandOutput(newRelayTickCmd())
	require.NoError(t, err)
	require.Empty(t, diagnostic)
	require.Equal(t, "published 0, applied 0\n", out)
	requireRelaySegmentsUnchanged(t, before)
}

func TestRelayTick_CancelledPass_ReportsDegradationAndReturnsSuccess(t *testing.T) {
	initCommandRelay(t, true)
	before, err := app.store.RelayPushCursor(t.Context())
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	cmd := newRelayTickCmd()
	cmd.SetContext(ctx)
	out, diagnostic, err := relayCommandOutput(cmd)
	require.NoError(t, err)
	require.Equal(t, "published 0, applied 0\n", out)
	require.Contains(t, diagnostic, "relay tick:")
	require.Contains(t, diagnostic, "context canceled")
	after, err := app.store.RelayPushCursor(t.Context())
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestRelayTick_HookDispatcher_AdvancesOwnedScanFloor(t *testing.T) {
	initCommandRelay(t, true)
	before, err := app.store.HookCursor(t.Context())
	require.NoError(t, err)
	tailBefore, err := app.store.JournalTail(t.Context())
	require.NoError(t, err)
	require.Less(t, before, tailBefore, "tick must have undispatched history")
	app.hooksDisp = service.NewHooksDispatcher(app.store, app.mtixDir, app.logger)
	_, _, err = relayCommandOutput(newRelayTickCmd())
	require.NoError(t, err)
	// Dispatch is observable even without executable hooks: its durable floor
	// passes the locally journaled history, so a later tick cannot replay it.
	scan, err := app.store.HookCursor(t.Context())
	require.NoError(t, err)
	tail, err := app.store.JournalTail(t.Context())
	require.NoError(t, err)
	require.Equal(t, tail, scan)
}

func TestRelayRecovery_Unconfigured_ExplainsSetup(t *testing.T) {
	initTestApp(t)
	tests := []struct {
		name string
		make func() *cobra.Command
		args []string
	}{
		{"tick", newRelayTickCmd, nil},
		{"reset", newRelayResetPeerCmd, nil},
		{"republish", newRelayRepublishCmd, []string{"--from", "1"}},
		{"clone", newRelayCloneCmd, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := relayCommandOutput(tt.make(), tt.args...)
			require.ErrorContains(t, err, "no relay configured; run `mtix config set sync.relay.dir <path>`")
		})
	}
}

func TestRelayResetPeer_FloorAndBaseRS_RepublishesUnderNewEpoch(t *testing.T) {
	for _, floorAtTail := range []bool{false, true} {
		t.Run(map[bool]string{false: "republish all", true: "skip past floor"}[floorAtTail], func(t *testing.T) {
			dir := initCommandRelay(t, true)
			_, _, err := relayCommandOutput(newRelayTickCmd())
			require.NoError(t, err)
			before, err := app.store.RelayPushCursor(t.Context())
			require.NoError(t, err)
			old := relaySegmentBytes(t, dir)
			floor := int64(0)
			if floorAtTail {
				floor = before.Seq
			}
			out, _, err := relayCommandOutput(newRelayResetPeerCmd(), "--floor", strconv.FormatInt(floor, 10), "--base-rs", "20")
			require.NoError(t, err)
			n := before.Seq - floor
			require.Equal(t, fmt.Sprintf("publisher epoch bumped; republished %d event(s) from journal floor %d at rs 20\n", n, floor), out)
			after, err := app.store.RelayPushCursor(t.Context())
			require.NoError(t, err)
			require.Equal(t, before.PubEpoch+1, after.PubEpoch)
			require.Equal(t, before.Seq, after.Seq)
			require.Equal(t, uint64(20+n), after.NextRS)
			requireRelaySegmentsUnchanged(t, old)
		})
	}
}

func TestRelayResetPeer_InvalidPositions_LeaveCursorUnchanged(t *testing.T) {
	for _, args := range [][]string{{"--base-rs", "0"}, {"--floor", "-1"}} {
		t.Run(args[0], func(t *testing.T) {
			initCommandRelay(t, true)
			before, err := app.store.RelayPushCursor(t.Context())
			require.NoError(t, err)
			out, _, err := relayCommandOutput(newRelayResetPeerCmd(), args...)
			require.Error(t, err)
			require.NotContains(t, out, "publisher epoch bumped")
			after, err := app.store.RelayPushCursor(t.Context())
			require.NoError(t, err)
			require.Equal(t, before, after)
		})
	}
}

func TestRelayRepublish_ExistingSequence_WritesFreshSegments(t *testing.T) {
	dir := initCommandRelay(t, true)
	_, _, err := relayCommandOutput(newRelayTickCmd())
	require.NoError(t, err)
	before, err := app.store.RelayPushCursor(t.Context())
	require.NoError(t, err)
	old := relaySegmentBytes(t, dir)
	out, _, err := relayCommandOutput(newRelayRepublishCmd(), "--from", "1")
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf("re-emitted %d event(s) from rs 1 into fresh segments\n", before.Seq), out)
	after, err := app.store.RelayPushCursor(t.Context())
	require.NoError(t, err)
	require.Equal(t, before.PubEpoch, after.PubEpoch)
	require.Equal(t, before.Seq, after.Seq)
	require.Equal(t, before.NextRS+uint64(before.Seq), after.NextRS)
	require.Greater(t, len(relaySegmentBytes(t, dir)), len(old))
	requireRelaySegmentsUnchanged(t, old)
}

func TestRelayRepublish_InvalidSequence_ExplainsRefusal(t *testing.T) {
	initCommandRelay(t, true)
	for _, args := range [][]string{nil, {"--from", "0"}, {"--from", "99"}} {
		name := "missing"
		if len(args) > 0 {
			name = args[1]
		}
		t.Run(name, func(t *testing.T) {
			_, _, err := relayCommandOutput(newRelayRepublishCmd(), args...)
			if name == "99" {
				require.ErrorContains(t, err, "no record at rs 99")
			} else {
				require.ErrorContains(t, err, "--from is required")
			}
		})
	}
}

func TestPrintRelayPass_Diagnostics_RenderExactOperatorOutput(t *testing.T) {
	tests := []struct {
		name   string
		result tick.Result
		want   string
	}{
		{"counts", tick.Result{Published: 2, Ingest: ingest.Stats{Applied: 3}}, "published 2, applied 3\n"},
		{"quarantine", tick.Result{Ingest: ingest.Stats{Quarantined: 1, Quarantines: []ingest.Quarantine{{PeerID: diagnosticOther, RS: 7, Reason: "invalid event"}}}}, "published 0, applied 0\nquarantined 1 record(s) from authenticated peers:\n  " + diagnosticOther + " rs 7: invalid event\n"},
		{"authentication", tick.Result{Ingest: ingest.Stats{AuthFailures: 2}}, "published 0, applied 0\nRELAY_AUTH_FAIL: 2 authentication failure(s) — check who else can write the relay\n"},
		{"coded stall", tick.Result{Ingest: ingest.Stats{Stalls: []ingest.Stall{{PeerID: diagnosticOther, Code: keyring.CodeKeyAbsent, Reason: "epoch missing"}}}}, "published 0, applied 0\nstalled on " + diagnosticOther + " (RELAY_KEY_ABSENT): epoch missing\n  fix: install the missing key epoch under the relay keys directory (mode 0600)\n"},
		{"uncoded stall", tick.Result{Ingest: ingest.Stats{Stalls: []ingest.Stall{{PeerID: diagnosticOther, Reason: "offline"}}}}, "published 0, applied 0\nstalled on " + diagnosticOther + ": offline\n  fix: check that the relay directory is reachable, then retry\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			cmd := &cobra.Command{Use: "tick"}
			cmd.SetOut(&out)
			printRelayPass(cmd, tt.result)
			require.Equal(t, tt.want, out.String())
		})
	}
}

func TestRelayRecovery_ClosedStore_ReportsPersistentStateFailure(t *testing.T) {
	tests := []struct {
		name string
		make func() *cobra.Command
		args []string
		want string
	}{
		{"reset", newRelayResetPeerCmd, nil, "database is closed"},
		{"republish", newRelayRepublishCmd, []string{"--from", "1"}, "database is closed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initCommandRelay(t, true)
			_, _, err := relayCommandOutput(newRelayTickCmd())
			require.NoError(t, err)
			require.NoError(t, app.store.Close())
			out, _, err := relayCommandOutput(tt.make(), tt.args...)
			require.ErrorContains(t, err, tt.want)
			require.NotContains(t, out, "republished")
			require.NotContains(t, out, "re-emitted")
		})
	}
}
