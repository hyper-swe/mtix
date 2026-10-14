// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// CLI inbox wait tests pin zero, invalid duration and documentation contracts (FR-19).
package main

import (
	"context"
	"log/slog"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func seedUnboundedInbox(t *testing.T) {
	t.Helper()
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	require.NoError(t, app.store.CreateNode(t.Context(), &model.Node{
		ID: "TEST-1", Project: "TEST", Seq: 1, Title: "CLI inbox target",
		NodeType: model.NodeTypeStory, Status: model.StatusOpen, Weight: 1, Creator: "tester",
		CreatedAt: now, UpdatedAt: now,
	}))
}

func putUnboundedInboxEvent(s *sqlite.Store) error {
	return s.SetAnnotations(context.Background(), "TEST-1", []model.Annotation{{
		ID: "wait-event", Author: "sender", Text: "CLI event", Addressee: "worker",
		CreatedAt: time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC),
	}})
}

func TestRunInbox_TimeoutZero_ReturnsOnLaterEvent(t *testing.T) {
	initTestApp(t)
	seedUnboundedInbox(t)
	app.jsonOutput = true
	writer, err := sqlite.New(filepath.Join(app.mtixDir, "data", "mtix.db"), slog.Default())
	require.NoError(t, err)
	defer func() { require.NoError(t, writer.Close()) }()
	written := make(chan error, 1)
	timer := time.AfterFunc(100*time.Millisecond, func() { written <- putUnboundedInboxEvent(writer) })
	defer timer.Stop()
	// Bound any broken implementation while leaving the real zero wait unlimited.
	waitStore := app.store
	guardErr := make(chan error, 1)
	guard := time.AfterFunc(5*time.Second, func() { guardErr <- waitStore.Close() })
	defer func() {
		if !guard.Stop() {
			require.NoError(t, <-guardErr)
		}
	}()
	var waitErr error
	out := captureStdout(t, func() { waitErr = runInbox("worker", true, 0, "") })
	require.NoError(t, <-written)
	require.NoError(t, waitErr, "zero timeout must wait for the later event, not return exit 5")
	assert.Contains(t, out, "CLI event")
	assert.Equal(t, 0, exitCodeForError(waitErr))
}

func TestRunInbox_TimeoutZero_PendingReturnsImmediately(t *testing.T) {
	initTestApp(t)
	seedUnboundedInbox(t)
	require.NoError(t, putUnboundedInboxEvent(app.store))
	var err error
	out := captureStdout(t, func() { err = runInbox("worker", true, 0, "context") })
	require.NoError(t, err)
	assert.Contains(t, out, "CLI event")
	assert.Equal(t, 0, exitCodeForError(err))
}

func TestRunInbox_NegativeOrOverflowTimeout_Refused(t *testing.T) {
	initTestApp(t)
	seedUnboundedInbox(t)
	require.NoError(t, putUnboundedInboxEvent(app.store))
	for _, tc := range []struct {
		name    string
		timeout int
		wait    bool
	}{
		{"negative waiting", -1, true},
		{"negative list", -1, false},
		{"duration overflow", int(math.MaxInt64/int64(time.Second)) + 1, true},
		{"largest int overflow", math.MaxInt, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			out := captureStdout(t, func() { err = runInbox("worker", tc.wait, tc.timeout, "") })
			require.ErrorIs(t, err, model.ErrInvalidInput)
			assert.Empty(t, out, "invalid timeout must not print pending events")
			assert.NotEqual(t, 0, exitCodeForError(err))
			assert.NotEqual(t, 5, exitCodeForError(err))
		})
	}
}

func TestInboxCmd_Help_DocumentsTimeoutZeroAndSignalExitCode(t *testing.T) {
	cmd := newInboxCmd()
	var output strings.Builder
	cmd.SetOut(&output)
	require.NoError(t, cmd.Help())
	for _, phrase := range []string{"--timeout 0", "exit 7", "SIGINT", "SIGTERM", "negative"} {
		assert.Contains(t, output.String(), phrase)
	}
	assert.Equal(t, "300", cmd.Flags().Lookup("timeout").DefValue)
}
