// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// errSpyOpener is returned by the spy opener so a reached open is observable.
var errSpyOpener = errors.New("spy opener reached")

// TestSalvageInboxJournal_ShortSource_SkipsOpen pins that inbox salvage
// refuses a zero- or one-byte source with errEmptyRecoverySource before any
// SQLite open (FR-19.1, MTIX-112.50.7). The opener is injected and counts
// calls; the source file, the result and the directory stay untouched.
func TestSalvageInboxJournal_ShortSource_SkipsOpen(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{"zero bytes", []byte{}},
		{"one byte", []byte{'S'}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "short.db")
			require.NoError(t, os.WriteFile(path, tt.data, 0600))
			res := &RecoverResult{}
			opens := 0
			spy := func(string, ...string) (*sql.DB, error) { opens++; return nil, errSpyOpener }

			journal, err := salvageInboxJournalWith(t.Context(), path, res, 5*time.Second, spy)

			require.ErrorIs(t, err, errEmptyRecoverySource)
			require.Zero(t, opens, "a short source must never reach the opener")
			require.Nil(t, journal)
			require.False(t, res.InboxIncomplete)
			require.Empty(t, res.Notes)
			after, readErr := os.ReadFile(path)
			require.NoError(t, readErr)
			require.Equal(t, tt.data, after)
			entries, dirErr := os.ReadDir(dir)
			require.NoError(t, dirErr)
			require.Len(t, entries, 1, "no SQLite sidecar may appear")
		})
	}
}

// TestSalvageInboxJournalWith_FullHeaderSource_ReachesOpener proves the spy
// is wired: a source past the short-source check does reach the opener, so a
// zero count above is evidence and not a dead spy (MTIX-112.50.7).
func TestSalvageInboxJournalWith_FullHeaderSource_ReachesOpener(t *testing.T) {
	path := filepath.Join(t.TempDir(), "full.db")
	require.NoError(t, os.WriteFile(path, []byte("SQLite format 3\x00"), 0600))
	opens := 0
	spy := func(string, ...string) (*sql.DB, error) { opens++; return nil, errSpyOpener }

	_, err := salvageInboxJournalWith(t.Context(), path, &RecoverResult{}, 5*time.Second, spy)

	require.ErrorIs(t, err, errSpyOpener)
	require.Equal(t, 1, opens)
}
