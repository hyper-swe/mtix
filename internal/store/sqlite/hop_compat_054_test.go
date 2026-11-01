// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// The real released binary must tolerate additive comment hop context.
package sqlite_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

func TestHopField_PushPull_054ReaderIgnores(t *testing.T) {
	binary := compat054Binary(t)
	t.Setenv("MTIX_WAKE_HOP", "31")
	f := newWakeCompatFixture(t)
	exported, err := f.store.Export(t.Context(), "TEST", "hop-test")
	require.NoError(t, err)
	raw, err := json.MarshalIndent(exported, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(f.project, ".mtix", "tasks.json"), raw, 0600))
	events, err := f.store.ReadPendingEvents(t.Context(), 100)
	require.NoError(t, err)
	var comment *model.SyncEvent
	for _, event := range events {
		if event.OpType == model.OpComment {
			comment = event
		}
	}
	require.NotNil(t, comment)
	var payload struct {
		Hop int64 `json:"hop"`
	}
	require.NoError(t, json.Unmarshal(comment.Payload, &payload))
	require.Equal(t, int64(32), payload.Hop)
	target, _ := mutationTestStore(t)
	pullEvents(t, target, events)
	assertHopJournal(t, target, comment)
	output := attachReaderCommand(t, binary, f.project, "", "inbox", "--agent", "test-agent", "--json")
	require.Contains(t, string(output), "pending compatibility wake")
	attachReaderCommand(t, binary, f.project, "", "sync", "--json")
	assertHopJournal(t, f.store, comment)
	if dsn := os.Getenv("MTIX_HOP_TEST_DSN"); dsn != "" {
		hopReleasedPushPull(t, binary, f, comment, dsn)
	} else {
		t.Log("PostgreSQL compatibility not run locally; hosted cmd matrix invokes this test with an isolated hub")
	}
}

func assertHopJournal(t *testing.T, store *sqlite.Store, event *model.SyncEvent) {
	t.Helper()
	var payload string
	require.NoError(t, store.ReadDB().QueryRowContext(t.Context(), "SELECT payload FROM sync_events WHERE event_id=?", event.EventID).Scan(&payload))
	require.JSONEq(t, string(event.Payload), payload)
	inbox, err := store.InboxList(t.Context(), "test-agent")
	require.NoError(t, err)
	require.NotEmpty(t, inbox)
	require.Equal(t, "pending compatibility wake", inbox[0].Body)
}

func hopReleasedPushPull(t *testing.T, binary string, f *wakeCompatFixture, comment *model.SyncEvent, dsn string) {
	t.Helper()
	pool, err := transport.New(t.Context(), dsn, transport.Options{InsecureTLS: true})
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	require.NoError(t, pool.Migrate(t.Context()))
	attachReaderCommand(t, binary, f.project, dsn, "sync", "push", "--insecure-tls")
	pulled, _, err := pool.PullEvents(t.Context(), transport.PullCursor{}, 100)
	require.NoError(t, err)
	found := false
	for _, event := range pulled {
		if event.EventID == comment.EventID {
			require.JSONEq(t, string(comment.Payload), string(event.Payload))
			found = true
		}
	}
	require.True(t, found, "real released push must preserve the hop event on the hub")
	project := t.TempDir()
	dir := filepath.Join(project, ".mtix")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "data"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("prefix: TEST\nsync:\n  auto_sync: false\n"), 0600))
	path := filepath.Join(dir, "data", "mtix.db")
	target, err := sqlite.New(path, nil)
	require.NoError(t, err)
	require.NoError(t, target.Close())
	attachReaderCommand(t, binary, project, dsn, "sync", "pull", "--insecure-tls")
	output := attachReaderCommand(t, binary, project, dsn, "inbox", "--agent", "test-agent", "--json")
	require.Contains(t, string(output), "pending compatibility wake")
	target, err = sqlite.New(path, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, target.Close()) })
	assertHopJournal(t, target, comment)
	t.Log("HOP_COMPAT_REAL_PG_PUSH_PULL")
}
