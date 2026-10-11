// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Provenance reads are bounded, independent and do not create operator files.
package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hyper-swe/mtix/internal/store/sqlite"

	"github.com/stretchr/testify/require"
)

func TestLastExchanged_PriorRecordAndOmission_ReturnsIndependentProvenance(t *testing.T) {
	s, dir := exchangeFixture(t)
	expected := exchangePublishReference(t, s, dir)
	require.NoError(t, s.saveExchangeReason(t.Context(), "partial-pull"))
	r, err := s.LastExchanged(t.Context(), dir)
	require.NoError(t, err)
	require.Equal(t, expected, r)
	r.Tasks[0].Fields["title"] = strings.Repeat("b", 64)
	again, err := s.LastExchanged(t.Context(), dir)
	require.NoError(t, err)
	require.Equal(t, expected, again)
	status, err := s.ExchangeRecordStatus(t.Context(), dir)
	require.NoError(t, err)
	require.True(t, status.Present)
	require.Equal(t, "partial-pull", status.Reason)
}

func TestLastExchanged_MissingPathsAndReadLock_DoesNotCreateFiles(t *testing.T) {
	s, dir := exchangeFixture(t)
	absent := filepath.Join(dir, "absent")
	_, err := s.LastExchanged(t.Context(), absent)
	require.ErrorIs(t, err, ErrNoExchangeRecord)
	_, err = os.Stat(absent)
	require.ErrorIs(t, err, os.ErrNotExist)
	exchangePublishReference(t, s, dir)
	require.NoError(t, os.Remove(filepath.Join(dir, "data", "sync.lock")))
	_, err = s.LastExchanged(t.Context(), dir)
	require.ErrorIs(t, err, ErrNoExchangeRecord)
	_, err = os.Stat(filepath.Join(dir, "data", "sync.lock"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestLastExchanged_InvalidRecords_RefusedWithoutRewrite(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*ExchangeRecord)
	}{
		{"format", func(r *ExchangeRecord) { r.Format = 2 }},
		{"form", func(r *ExchangeRecord) { r.BoardForm = "unknown" }},
		{"source", func(r *ExchangeRecord) { r.Source = "push" }},
		{"time", func(r *ExchangeRecord) { r.WrittenAt = "bad" }},
		{"hash", func(r *ExchangeRecord) { r.BoardHash = strings.Repeat("z", 64) }},
		{"parent", func(r *ExchangeRecord) { r.ParentBoardHash = "bad" }},
		{"raw", func(r *ExchangeRecord) { r.BoardFileHash = "" }},
		{"count", func(r *ExchangeRecord) { r.NodeCount++ }},
		{"identity", func(r *ExchangeRecord) { r.Tasks[0].Identity = "" }},
		{"duplicate", func(r *ExchangeRecord) { r.Tasks = append(r.Tasks, r.Tasks[0]); r.NodeCount++ }},
		{"fields", func(r *ExchangeRecord) { delete(r.Tasks[0].Fields, "title") }},
		{"field hash", func(r *ExchangeRecord) { r.Tasks[0].Fields["title"] = "bad" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, dir := exchangeFixture(t)
			r := exchangePublishReference(t, s, dir)
			tc.edit(r)
			raw, err := json.Marshal(r)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(exchangePath(dir), raw, 0o600))
			_, err = s.LastExchanged(t.Context(), dir)
			require.ErrorIs(t, err, ErrNoExchangeRecord)
			after, err := os.ReadFile(exchangePath(dir))
			require.NoError(t, err)
			require.Equal(t, raw, after)
		})
	}
}

func TestLastExchanged_UnsafeEncodingAndOwnership_Refused(t *testing.T) {
	for _, kind := range []string{"truncated", "trailing", "unknown", "oversize", "mode", "temporary", "symlink", "locked", "cancelled", "zero cap", "negative cap"} {
		t.Run(kind, func(t *testing.T) {
			s, dir := exchangeFixture(t)
			exchangePublishReference(t, s, dir)
			path := exchangePath(dir)
			ctx := t.Context()
			switch kind {
			case "truncated":
				require.NoError(t, os.WriteFile(path, []byte(`{"format":`), 0o600))
			case "trailing":
				raw, err := os.ReadFile(path)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(path, append(raw, []byte(` {}`)...), 0o600))
			case "unknown":
				raw, err := os.ReadFile(path)
				require.NoError(t, err)
				raw = append([]byte(`{"unknown":true,`), raw[1:]...)
				require.NoError(t, os.WriteFile(path, raw, 0o600))
			case "oversize":
				s.MaxImportSize = 1
			case "mode":
				require.NoError(t, os.Chmod(path, 0o644))
			case "temporary":
				require.NoError(t, os.WriteFile(filepath.Join(dir, "data", ".exchange-record-owned"), []byte("partial"), 0o600))
			case "symlink":
				require.NoError(t, os.Rename(path, path+".old"))
				require.NoError(t, os.Symlink(path+".old", path))
			case "locked":
				lock, err := s.acquireLock(dir, lockExclusive)
				require.NoError(t, err)
				defer s.releaseLock(lock)
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "zero cap":
				s.MaxImportSize = 0
			case "negative cap":
				s.MaxImportSize = -1
			}
			_, err := s.LastExchanged(ctx, dir)
			require.ErrorIs(t, err, ErrNoExchangeRecord)
			if kind == "cancelled" {
				require.ErrorIs(t, err, context.Canceled)
			}
		})
	}
}

func TestLastExchanged_InvalidHubBoundary_RefusedWithoutRewrite(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*sqlite.ExchangeBoundary)
	}{
		{"sequence", func(b *sqlite.ExchangeBoundary) { b.LocalSequence = -1 }},
		{"cursor", func(b *sqlite.ExchangeBoundary) { b.PullClock = -1 }},
		{"pending", func(b *sqlite.ExchangeBoundary) { b.Pending = 1 }},
		{"unresolved", func(b *sqlite.ExchangeBoundary) { b.Unresolved = 1 }},
		{"sweep id", func(b *sqlite.ExchangeBoundary) { b.SweepAfterID = "pending" }},
		{"sweep time", func(b *sqlite.ExchangeBoundary) { b.SweepAfterCreatedAt = "pending" }},
		{"sweep start", func(b *sqlite.ExchangeBoundary) { b.SweepStartedAt = "pending" }},
		{"sweep completed", func(b *sqlite.ExchangeBoundary) { b.LastSweepAt = "bad" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, dir := exchangeFixture(t)
			r := exchangePublishReference(t, s, dir)
			r.Source, r.BoardFileHash = "hub-pull", ""
			r.Boundary = &sqlite.ExchangeBoundary{LastSweepAt: "2026-10-01T00:00:00Z"}
			tc.edit(r.Boundary)
			raw, err := json.Marshal(r)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(exchangePath(dir), raw, 0o600))
			_, err = s.LastExchanged(t.Context(), dir)
			require.ErrorIs(t, err, ErrNoExchangeRecord)
			after, err := os.ReadFile(exchangePath(dir))
			require.NoError(t, err)
			require.Equal(t, raw, after)
		})
	}
}

func TestExchangeRecord_BoundedByteReader_OwnedInputBoundaries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "receipt")
	_, err := readBoundedExchange(path, 3)
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = readBoundedExchange(dir, 3)
	require.Error(t, err, "directory cannot supply receipt bytes")
	require.NoError(t, os.WriteFile(path, []byte("four"), 0o600))
	_, err = readBoundedExchange(path, 3)
	require.ErrorIs(t, err, ErrNoExchangeRecord)
	require.Equal(t, "exchange record: oversize", err.Error())
	raw, err := readBoundedExchange(path, 4)
	require.NoError(t, err)
	require.Equal(t, []byte("four"), raw)
}
