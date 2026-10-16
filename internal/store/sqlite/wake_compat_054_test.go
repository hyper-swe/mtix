// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests I21 with an actual released mutation while a modern fenced sender is in flight.
package sqlite_test

import (
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

type wakeCompatFixture struct {
	store   *sqlite.Store
	project string
	key     model.WakeKey
	alerts  *service.Alerts
	sent    atomic.Int32
}

func newWakeCompatFixture(t *testing.T) *wakeCompatFixture {
	t.Helper()
	project := t.TempDir()
	dir := filepath.Join(project, ".mtix")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "data"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("prefix: TEST\nsync:\n  auto_sync: false\n"), 0o600))
	s, err := sqlite.New(filepath.Join(dir, "data", "mtix.db"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	s.SetClock(clock)
	nodes := service.NewNodeService(s, &service.NoopBroadcaster{}, &service.StaticConfig{}, slog.Default(), clock)
	node, err := nodes.CreateNode(t.Context(), &service.CreateNodeRequest{Project: "TEST", Title: "wake compatibility", Creator: "test-author"})
	require.NoError(t, err)
	prompts := service.NewPromptService(s, &service.NoopBroadcaster{}, slog.Default(), clock)
	require.NoError(t, prompts.AddAnnotation(t.Context(), node.ID, "pending compatibility wake", "test-author", "test-agent"))
	entries, err := s.InboxList(t.Context(), "test-agent")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	return &wakeCompatFixture{store: s, project: project,
		key:    model.WakeKey{AgentID: "test-agent", HostID: "test-host", UID: "test-uid", EventSeq: entries[0].Seq, Epoch: 1},
		alerts: service.NewAlerts(wakeCompatOperator(t), clock)}
}

func wakeCompatOperator(t *testing.T) *oplocal.State {
	t.Helper()
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	root, err := os.MkdirTemp(home, ".mtix-wake-compat-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(root)) })
	root, err = filepath.EvalSymlinks(root)
	require.NoError(t, err)
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv("APPDATA", root)
	return oplocal.New(oplocal.Env{GOOS: runtime.GOOS, Home: home, Values: map[string]string{"XDG_CONFIG_HOME": root, "APPDATA": root}})
}

func seedWakeCompatLegacyPass(t *testing.T, f *wakeCompatFixture) {
	t.Helper()
	for _, row := range []struct{ name, outcome string }{{"wake-compat-stale", "claimed"}, {"wake-compat-prune", "delivered"}} {
		_, err := f.store.WriteDB().ExecContext(t.Context(),
			"INSERT INTO hook_dispatch_ledger(hook_name,event_seq,fired_at,outcome) VALUES(?,?,?,?)",
			row.name, f.key.EventSeq, "2000-01-01T00:00:00Z", row.outcome)
		require.NoError(t, err)
	}
	previous := f.key
	previous.Epoch = 0
	claim, won, err := f.store.ClaimWake(t.Context(), previous, time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	require.NoError(t, f.store.RecordWakeOutcome(t.Context(), claim, model.WakeDelivered))
	claim.Token = "obsolete-attempt"
	require.ErrorIs(t, f.store.RecordWakeOutcome(t.Context(), claim, model.WakeError), model.ErrConflict)
	require.NoError(t, f.store.AdvanceWakeScanCursor(t.Context(), f.key.HostID, f.key.UID, f.key.EventSeq-1, time.Minute))
}

func TestWakeLedger_054DispatchPassAndPrune_RowUntouched(t *testing.T) {
	// Resolve and verify the real binary before acquiring a live claim.
	binary := compat054Binary(t)
	f := newWakeCompatFixture(t)
	seedWakeCompatLegacyPass(t, f)
	run := startWakeCompatSender(t, f)
	select {
	case attempt := <-run.entered:
		require.Equal(t, f.key, attempt.Key)
		require.NotEmpty(t, attempt.Token)
	case err := <-run.result:
		t.Fatalf("modern sender stopped before its live claim: %v", err)
	case <-t.Context().Done():
		t.Fatal("test ended before the live claim")
	}
	before := attachLocalState(t, f.store)
	output := runWakeCompatOldMutation(t, binary, f.project)
	t.Logf("released mutation output: %s", output)
	require.Equal(t, before, attachLocalState(t, f.store), "old dispatch, sweep and prune must preserve all wake state")
	assertWakeCompatLegacyPass(t, f)
	close(run.release)
	require.NoError(t, <-run.result)
	require.NoError(t, run.dispatcher.Pass(t.Context(), []model.WakeKey{f.key, f.key}))
	require.Equal(t, int32(1), f.sent.Load())
	entries, err := f.store.InboxList(t.Context(), f.key.AgentID)
	require.NoError(t, err)
	require.Len(t, entries, 1, "a wake never acknowledges the addressed event")
	require.Equal(t, f.key.EventSeq, entries[0].Seq)
}
