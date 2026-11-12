// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// NFR-2.8: both recover inbox notes tell the operator to keep the database
// and its -wal and -shm together, whether or not the mail could be counted.
func TestWriteRecoverInboxNote_BothSentences_KeepSidecarsTogether(t *testing.T) {
	t.Run("count unavailable", func(t *testing.T) {
		var out bytes.Buffer
		writeRecoverInboxNote(t.Context(), &out, filepath.Join(t.TempDir(), "gone.db"))
		require.Contains(t, out.String(), "could not be counted")
		require.Contains(t, out.String(), "Keep the source database and its -wal and -shm together")
	})
	t.Run("unread mail present", func(t *testing.T) {
		initTestApp(t)
		events := envelopeCLIEvents(t, "one", "two", "three")
		require.NotEmpty(t, events)
		var out bytes.Buffer
		writeRecoverInboxNote(t.Context(), &out, filepath.Join(app.mtixDir, "data", "mtix.db"))
		require.Contains(t, out.String(), "unacknowledged inbox event(s)")
		require.Contains(t, out.String(), "keep the source database and its -wal and -shm together")
	})
}
