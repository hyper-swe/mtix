// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package main

import (
	"bytes"
	"database/sql"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
	"time"
)

// FR-19.1: a restored row below the cursor remains missing until the real sweep applies it.
func TestRestoredInbox_PostgresSweepFetchesLateComment(t *testing.T) {
	for _, restored := range []bool{false, true} {
		t.Run(map[bool]string{false: "control", true: "restored"}[restored], func(t *testing.T) {
			f := newSweepFixture(t)
			f.seedSharedNode(t)
			f.editPeer(t, 10)
			f.pushPeer(t)
			f.pullPeer(t, 100)
			cursor := f.peerCursor(t)
			require.NoError(t, f.b.AppendAnnotation(t.Context(), "TEST-1", model.Annotation{ID: "late-mail", Author: "sender", Text: "late private mail", Addressee: "worker", CreatedAt: time.Now().UTC()}))
			private, err := f.b.ExportWithInbox(t.Context(), "", "")
			require.NoError(t, err)
			if restored {
				current, err := app.store.Export(t.Context(), "", "")
				require.NoError(t, err)
				current.InboxJournal = private.InboxJournal
				_, err = app.store.Import(t.Context(), current, sqlite.ImportModeMerge, false)
				require.NoError(t, err)
			}
			late := f.pushB(t)
			require.Len(t, late, 1)
			require.Less(t, late[0].LamportClock, cursor)
			f.pullPeer(t, 100)
			require.True(t, f.appliedOnPeer(t, late[0].EventID), "real late event must be fetched even when its mail row was restored")
			node, err := app.store.GetNode(t.Context(), "TEST-1")
			require.NoError(t, err)
			require.Len(t, node.Annotations, 1)
			require.Equal(t, "late private mail", node.Annotations[0].Text)
			var marked, clock int
			require.NoError(t, app.store.QueryRow(t.Context(), `SELECT COUNT(*) FROM inbox_restored_events`).Scan(&marked))
			require.Zero(t, marked)
			require.NoError(t, app.store.QueryRow(t.Context(), `SELECT lamport_clock FROM sync_events WHERE event_id=?`, late[0].EventID).Scan(&clock))
			require.Positive(t, clock)
			mail, err := app.store.InboxList(t.Context(), "worker")
			require.NoError(t, err)
			require.Len(t, mail, 1)
		})
	}
}

// FR-19.1: mixed restored/real history must never receive a destructive remedy.
func TestRestoredInbox_PostgresDivergentRemedies(t *testing.T) {
	for _, action := range []string{"init", "clone", "discard", "dry-run"} {
		t.Run(action, func(t *testing.T) {
			hub := openCmdHub(t)
			source, file := restoredInboxSource(t)
			prefix, _, err := source.GetOrComputeLocalFirstEventHash(t.Context())
			require.NoError(t, err)
			require.NoError(t, registerProjectOnHub(t.Context(), hub, prefix, strings.Repeat("f", 64)))
			initTestApp(t)
			_, err = app.store.Import(t.Context(), file, sqlite.ImportModeMerge, false)
			require.NoError(t, err)
			events, err := source.ReadPendingEvents(t.Context(), 100)
			require.NoError(t, err)
			require.NoError(t, app.store.WithTx(t.Context(), func(tx *sql.Tx) error { return app.store.IdempotentApply(t.Context(), tx, events[0]) }))
			before, err := app.store.InboxList(t.Context(), "worker")
			require.NoError(t, err)
			require.Len(t, before, 1)
			var out, diagnostic bytes.Buffer
			switch action {
			case "init":
				err = runSyncInit(t.Context(), &out, &diagnostic, nil, transport.Options{InsecureTLS: true})
			case "clone":
				err = runSyncClone(t.Context(), &out, &diagnostic, nil, transport.Options{InsecureTLS: true}, false, 100)
			case "discard":
				err = sqlite.DiscardLocal(t.Context(), app.store, app.mtixDir)
			case "dry-run":
				_, err = sqlite.DryRunDiscardLocal(t.Context(), app.store)
			}
			require.Error(t, err)
			text := out.String() + diagnostic.String() + err.Error()
			require.Contains(t, text, "restored mail")
			require.Contains(t, text, "new empty directory")
			require.Contains(t, text, "mtix sync clone")
			if action == "init" || action == "clone" {
				require.NotContains(t, text, "--discard-local")
			}
			after, err := app.store.InboxList(t.Context(), "worker")
			require.NoError(t, err)
			require.Equal(t, before, after)
		})
	}
}
