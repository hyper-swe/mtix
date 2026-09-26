// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests for MTIX-95.31.11 (FR-15.2h, FR-15.8): the rewrite of a conflict
// baseline recognized in an older form runs under the shared sync lock, so
// two commands can rewrite it at once. Each rewrite goes through its own
// temporary file and a rename, so the baseline is always one whole hash,
// never empty or partial, and no temporary file is left behind. Written
// red-first.
package service

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// baselineRewriteDir returns a .mtix directory whose conflict baseline is
// initial, and a service that logs to logs.
func baselineRewriteDir(t *testing.T, initial string) (string, *SyncService, *bytes.Buffer) {
	t.Helper()
	mtixDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(mtixDir, "data"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(mtixDir, "data", "sync-db.sha256"), []byte(initial), 0o644))
	logs := &bytes.Buffer{}
	return mtixDir, &SyncService{logger: slog.New(slog.NewTextHandler(logs, nil))}, logs
}

// leftoverTempFiles lists the files in the data directory other than the
// baseline.
func leftoverTempFiles(t *testing.T, mtixDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(mtixDir, "data"))
	require.NoError(t, err)
	var extra []string
	for _, e := range entries {
		if e.Name() != "sync-db.sha256" {
			extra = append(extra, e.Name())
		}
	}
	return extra
}

// TestUpgradeBaseline_ConcurrentRewrites_BaselineAlwaysWhole verifies that
// rewrites running at once, as two commands under the shared lock do, never
// leave the baseline empty or partial: a reader sees only whole hashes,
// each the old one or one written, and no temporary file remains.
func TestUpgradeBaseline_ConcurrentRewrites_BaselineAlwaysWhole(t *testing.T) {
	const writers, rounds = 8, 150
	initial := strings.Repeat("0", 64)
	mtixDir, s, _ := baselineRewriteDir(t, initial)
	s.logger = slog.New(slog.NewTextHandler(io.Discard, nil)) // the failed renames of the fixed-name file would fill the buffer
	valid := map[string]bool{initial: true}
	for w := 0; w < writers; w++ {
		valid[fmt.Sprintf("%064x", w+1)] = true
	}

	var bad int          // reads that saw neither the old hash nor a whole written one
	var samples []string // the first few of them
	var badMu sync.Mutex
	done := make(chan struct{})
	readerDone := make(chan struct{})
	go func() { // reads the baseline as the next command's hasConflict does
		defer close(readerDone)
		for {
			select {
			case <-done:
				return
			default:
			}
			raw, err := os.ReadFile(filepath.Join(mtixDir, "data", "sync-db.sha256"))
			if err == nil && !valid[string(raw)] {
				badMu.Lock()
				bad++
				if len(samples) < 5 {
					samples = append(samples, fmt.Sprintf("%q", raw))
				}
				badMu.Unlock()
			}
		}
	}()
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(hash string) {
			defer wg.Done()
			for r := 0; r < rounds; r++ {
				// Each rewrite expects the baseline it read, as hasConflict does.
				read, err := os.ReadFile(filepath.Join(mtixDir, "data", "sync-db.sha256"))
				if err != nil {
					t.Errorf("read the baseline: %v", err) // a rename replaces it atomically: it always exists
					return
				}
				s.upgradeBaseline(mtixDir, string(read), hash, form100)
			}
		}(fmt.Sprintf("%064x", w+1))
	}
	wg.Wait()
	close(done)
	<-readerDone

	assert.Zero(t, bad, "a reader saw an empty or partial baseline, for example %v", samples)
	final, err := os.ReadFile(filepath.Join(mtixDir, "data", "sync-db.sha256"))
	require.NoError(t, err)
	assert.True(t, valid[string(final)], "the baseline is a whole hash: %q", final)
	assert.Empty(t, leftoverTempFiles(t, mtixDir), "no temporary file is left behind")
}

// removedTempFile wraps the temporary baseline file and removes it once it
// is written and closed, so the rename that follows fails.
type removedTempFile struct{ f *os.File }

// Write writes p to the temporary file.
func (r *removedTempFile) Write(p []byte) (int, error) { return r.f.Write(p) }

// Close closes the temporary file, then removes it.
func (r *removedTempFile) Close() error {
	if err := r.f.Close(); err != nil {
		return err
	}
	return os.Remove(r.f.Name())
}

// TestUpgradeBaseline_RenameFails_WarnsAndLeavesNoTempFile verifies a
// rewrite that cannot complete is logged, not reported as done, keeps the
// baseline as it was and leaves no temporary file: when the rename fails,
// and when the baseline cannot be read again before the rename (its mode
// is 0000), which a rename would otherwise replace unchecked.
func TestUpgradeBaseline_RenameFails_WarnsAndLeavesNoTempFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a file of mode 0000")
	}
	tests := []struct {
		name  string
		setup func(t *testing.T, baselinePath string, s *SyncService)
	}{
		{"the rename fails", func(_ *testing.T, _ string, s *SyncService) {
			s.wrapBaselineFile = func(f *os.File) io.WriteCloser { return &removedTempFile{f: f} }
		}},
		{"the baseline cannot be read before the rename", func(t *testing.T, baselinePath string, _ *SyncService) {
			require.NoError(t, os.Chmod(baselinePath, 0o000))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mtixDir, s, logs := baselineRewriteDir(t, "older")
			baselinePath := filepath.Join(mtixDir, "data", "sync-db.sha256")
			tt.setup(t, baselinePath, s)

			s.upgradeBaseline(mtixDir, "older", strings.Repeat("a", 64), form100)

			assert.Contains(t, logs.String(), "could not rewrite the conflict baseline in the current form")
			assert.NotContains(t, logs.String(), "event=sync_baseline_upgraded")
			assert.Empty(t, leftoverTempFiles(t, mtixDir), "the temporary file is removed")
			require.NoError(t, os.Chmod(baselinePath, 0o644))
			after, err := os.ReadFile(baselinePath)
			require.NoError(t, err)
			assert.Equal(t, "older", string(after), "the baseline is left as it was")
		})
	}
}

// TestUpgradeBaseline_Rewrite_BesideTheBaselineWithMode0644 verifies the
// rewrite needs nothing outside the data directory (its temporary file is
// created beside the baseline, so the rename never crosses a file system),
// and leaves the baseline readable as before, mode 0644.
func TestUpgradeBaseline_Rewrite_BesideTheBaselineWithMode0644(t *testing.T) {
	mtixDir, s, logs := baselineRewriteDir(t, "older")
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "no-such-directory")) // the system temporary directory is unusable
	want := strings.Repeat("b", 64)

	s.upgradeBaseline(mtixDir, "older", want, form100)

	path := filepath.Join(mtixDir, "data", "sync-db.sha256")
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, want, string(got))
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm())
	assert.Contains(t, logs.String(), "event=sync_baseline_upgraded")
	assert.Empty(t, leftoverTempFiles(t, mtixDir))
}

// errDiskFull is the error failingFile returns, as a full disk would.
var errDiskFull = errors.New("no space left on device")

// failingFile wraps the temporary baseline file (SyncService.wrapBaselineFile)
// and fails its write after writing half of the data, or its close after
// closing the file, as a full disk can (MTIX-26).
type failingFile struct {
	f         *os.File
	failWrite bool
	failClose bool
}

// Write writes p, or half of it and then fails when failWrite is set.
func (w *failingFile) Write(p []byte) (int, error) {
	if !w.failWrite {
		return w.f.Write(p)
	}
	n, err := w.f.Write(p[:len(p)/2])
	if err != nil {
		return n, err
	}
	return n, errDiskFull
}

// Close closes the file, then fails when failClose is set.
func (w *failingFile) Close() error {
	if err := w.f.Close(); err != nil {
		return err
	}
	if w.failClose {
		return errDiskFull
	}
	return nil
}

// TestUpgradeBaseline_WriteOrCloseFails_KeepsOldBaselineAndNoTempFile
// verifies a rewrite whose write or close fails, as on a full disk, never
// puts a torn baseline in place: the older baseline stays as it was, the
// temporary file is removed, and the failure is logged, not reported as a
// rewrite.
func TestUpgradeBaseline_WriteOrCloseFails_KeepsOldBaselineAndNoTempFile(t *testing.T) {
	tests := []struct {
		name                 string
		failWrite, failClose bool
	}{
		{"a failed write", true, false},
		{"a failed close", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mtixDir, s, logs := baselineRewriteDir(t, "older")
			s.wrapBaselineFile = func(f *os.File) io.WriteCloser {
				return &failingFile{f: f, failWrite: tt.failWrite, failClose: tt.failClose}
			}

			s.upgradeBaseline(mtixDir, "older", strings.Repeat("c", 64), form100)

			got, err := os.ReadFile(filepath.Join(mtixDir, "data", "sync-db.sha256"))
			require.NoError(t, err)
			assert.Equal(t, "older", string(got), "the older baseline is kept whole")
			assert.Empty(t, leftoverTempFiles(t, mtixDir), "the temporary file is removed")
			assert.Contains(t, logs.String(), "could not rewrite the conflict baseline in the current form")
			assert.Contains(t, logs.String(), errDiskFull.Error())
			assert.NotContains(t, logs.String(), "event=sync_baseline_upgraded")
		})
	}
}

// concurrentImportFile wraps the temporary baseline file of a rewrite and,
// once the rewrite has written and closed it, writes fresh to the baseline
// itself, as another command's import (recordImported) does when it
// finishes under the same shared sync lock between this rewrite's read of
// the baseline and its rename.
type concurrentImportFile struct {
	f        *os.File
	baseline string
	fresh    string
}

// Write writes p to the temporary file.
func (c *concurrentImportFile) Write(p []byte) (int, error) {
	return c.f.Write(p)
}

// Close closes the temporary file, then writes the concurrent import's
// fresh baseline.
func (c *concurrentImportFile) Close() error {
	if err := c.f.Close(); err != nil {
		return err
	}
	return os.WriteFile(c.baseline, []byte(c.fresh), 0o644)
}

// TestHasConflict_BaselineRefreshedDuringUpgrade_KeepsTheFreshBaseline
// verifies the rewrite of a baseline recognized in an older form never
// replaces a fresher baseline that a concurrent command's import wrote
// meanwhile: the rewrite goes ahead only while the baseline still holds
// the older hash that matched, so the fresh one stays and no rewrite is
// logged. The import that follows is then checked as usual.
func TestHasConflict_BaselineRefreshedDuringUpgrade_KeepsTheFreshBaseline(t *testing.T) {
	local := &sqlite.ExportData{}
	older, err := exportHash(local, form100)
	require.NoError(t, err)
	mtixDir, s, logs := baselineRewriteDir(t, older)
	baselinePath := filepath.Join(mtixDir, "data", "sync-db.sha256")
	fresh := strings.Repeat("f", 64)
	s.wrapBaselineFile = func(f *os.File) io.WriteCloser {
		return &concurrentImportFile{f: f, baseline: baselinePath, fresh: fresh}
	}

	conflict, err := s.hasConflict(mtixDir, local)

	require.NoError(t, err)
	assert.False(t, conflict, "the baseline matched the store in an older form")
	got, err := os.ReadFile(baselinePath)
	require.NoError(t, err)
	assert.Equal(t, fresh, string(got), "the baseline the concurrent import wrote is kept")
	assert.NotContains(t, logs.String(), "event=sync_baseline_upgraded")
	assert.NotContains(t, logs.String(), "could not rewrite", "a newer baseline is no failure")
	assert.Empty(t, leftoverTempFiles(t, mtixDir), "no temporary file is left behind")
}
