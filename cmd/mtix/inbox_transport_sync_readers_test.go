// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func restoredInboxSource(t *testing.T) (*sqlite.Store, *sqlite.ExportData) {
	t.Helper()
	source, err := sqlite.New(filepath.Join(t.TempDir(), "source.db"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = source.Close() })
	require.NoError(t, source.CreateNode(t.Context(), mkPGNode("TEST-1", "", 0, 1, "transport")))
	require.NoError(t, source.SetAnnotations(t.Context(), "TEST-1", []model.Annotation{{ID: "mail", Author: "sender", Text: "private mail", Addressee: "worker", CreatedAt: time.Now().UTC()}}))
	file, err := source.ExportWithInbox(t.Context(), "", "")
	require.NoError(t, err)
	require.Len(t, file.InboxJournal.Events, 1)
	return source, file
}

func TestRestoredInbox_CloneAndStatusIgnoreMail(t *testing.T) {
	_, file := restoredInboxSource(t)
	initTestApp(t)
	_, err := app.store.Import(t.Context(), file, sqlite.ImportModeMerge, false)
	require.NoError(t, err)
	events, err := localHasEvents(t.Context(), app.store)
	require.NoError(t, err)
	require.False(t, events)
	status, err := readSyncStatus(t.Context(), app.store)
	require.NoError(t, err)
	require.Zero(t, status.Pending)
	require.Zero(t, status.Pushed)
	require.Zero(t, status.Applied)
}

func TestRestoredInbox_RelayInitDoesNotPublishMailIdentity(t *testing.T) {
	_, file := restoredInboxSource(t)
	initTestApp(t)
	setDiagnosticConfig(t, "sync.relay.peer_id", diagnosticSelf)
	_, err := app.store.Import(t.Context(), file, sqlite.ImportModeMerge, false)
	require.NoError(t, err)
	_, diagnostic, err := relayCommandOutput(newRelayInitCmd(), t.TempDir(), "--no-auth")
	require.ErrorContains(t, err, "no project history yet")
	require.NotContains(t, diagnostic, "discard-local")
	mail, err := app.store.InboxList(t.Context(), "worker")
	require.NoError(t, err)
	require.Len(t, mail, 1)
}
