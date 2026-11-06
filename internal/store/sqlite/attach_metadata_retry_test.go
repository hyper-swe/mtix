// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A side file seen while another process opens or closes the store goes away
// within moments; the identity read tries again briefly before it gives up, so
// a busy listed board is not declined by chance (MTIX-112.19.8).
func TestReadStoreInstanceIDFile_LoneSideFileThatClears_ReadsOnTheRetry(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir, slog.Default())
	require.NoError(t, err)
	want, err := s.StoreInstanceID(t.Context())
	require.NoError(t, err)
	require.NoError(t, s.Close())
	lone := filepath.Join(dir, "mtix.db-wal")
	require.NoError(t, os.WriteFile(lone, []byte("transient"), 0o600))
	go func() {
		time.Sleep(5 * time.Millisecond)
		_ = os.Remove(lone)
	}()

	got, err := ReadStoreInstanceIDFile(t.Context(), filepath.Join(dir, "mtix.db"))
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

// A lone side file that stays still declines, after a bounded wait, and the
// read writes nothing.
func TestReadStoreInstanceIDFile_LoneSideFileThatPersists_FailsWithinTheBound(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir, slog.Default())
	require.NoError(t, err)
	require.NoError(t, s.Close())
	require.NoError(t, os.WriteFile(filepath.Join(dir, "mtix.db-shm"), make([]byte, 32768), 0o600))
	before := dirListing(t, dir)

	start := time.Now()
	_, err = ReadStoreInstanceIDFile(t.Context(), filepath.Join(dir, "mtix.db"))
	elapsed := time.Since(start)
	require.Error(t, err)
	assert.Less(t, elapsed, 200*time.Millisecond, "the retry is short: a hook waits for it")
	assert.Equal(t, before, dirListing(t, dir))
}

// A missing or empty file is a settled answer: no retry spends time on it.
func TestReadStoreInstanceIDFile_MissingFile_FailsWithoutWaiting(t *testing.T) {
	start := time.Now()
	_, err := ReadStoreInstanceIDFile(t.Context(), filepath.Join(t.TempDir(), "absent.db"))
	require.Error(t, err)
	assert.Less(t, time.Since(start), retryPause, "a missing store is not retried")
}

// The review's shape: other processes open, write and close the store while a
// hook reads its identity again and again. No read may be declined by chance.
func TestReadStoreInstanceIDFile_ConcurrentOpenAndCloseChurn_NeverFails(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir, slog.Default())
	require.NoError(t, err)
	want, err := s.StoreInstanceID(t.Context())
	require.NoError(t, err)
	require.NoError(t, s.Close())

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			w, openErr := New(dir, slog.Default())
			if openErr != nil {
				continue
			}
			_, _ = w.StoreInstanceID(t.Context())
			_ = w.Close()
			time.Sleep(3 * time.Millisecond)
		}
	}()
	failures := 0
	for i := 0; i < 400; i++ {
		got, readErr := ReadStoreInstanceIDFile(t.Context(), filepath.Join(dir, "mtix.db"))
		if readErr != nil || got != want {
			failures++
		}
	}
	close(stop)
	wg.Wait()
	assert.Zero(t, failures, "reads declined while another process opened and closed the store")
}

// Only a busy or locked answer is worth a second try; any other failure is
// settled.
func TestTransientIfBusy_BusyAndLockedMessages_AreRetryable(t *testing.T) {
	tests := []struct {
		msg       string
		transient bool
	}{
		{"database is locked (5) (SQLITE_BUSY)", true},
		{"SQLITE_BUSY", true},
		{"sqlite_busy: timeout", true},
		{"database table is locked: SQLITE_LOCKED", true},
		{"database is locked", true},
		{"no such table: meta", false},
		{"file is not a database", false},
	}
	for _, tt := range tests {
		t.Run(tt.msg, func(t *testing.T) {
			err := transientIfBusy(errors.New(tt.msg))
			assert.Equal(t, tt.transient, errors.Is(err, errTransientRead))
			assert.ErrorContains(t, err, tt.msg)
		})
	}
}
