// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// salvageInboxJournal must refuse a zero- or one-byte source before any
// SQLite open. openSQLite is not injectable, so "not reached" is shown by
// its absence of effects: the open-and-read path would record into res or
// fail with a different error, and the directory must hold only the source.
func TestSalvageInboxJournal_ShortSourceSkipsOpen(t *testing.T) {
	for _, data := range [][]byte{{}, {'S'}} {
		t.Run(fmt.Sprint(len(data)), func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "short.db")
			require.NoError(t, os.WriteFile(path, data, 0600))
			res := &RecoverResult{}

			journal, err := salvageInboxJournal(t.Context(), path, res, 5*time.Second)

			require.ErrorIs(t, err, errEmptyRecoverySource)
			require.Nil(t, journal)
			require.False(t, res.InboxIncomplete)
			require.Empty(t, res.Notes)
			after, readErr := os.ReadFile(path)
			require.NoError(t, readErr)
			require.Equal(t, data, after)
			entries, dirErr := os.ReadDir(dir)
			require.NoError(t, dirErr)
			require.Len(t, entries, 1, "no SQLite sidecar may appear")
		})
	}
}
