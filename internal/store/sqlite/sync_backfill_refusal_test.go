// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests default refusal, explicit append, malformed-source atomicity and lifecycle errors.
package sqlite_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

func TestBackfillRefusal_ExistingHistory_RefusesOrExplicitlyAppends(t *testing.T) {
	for _, force := range []bool{false, true} {
		t.Run(map[bool]string{false: "default", true: "force"}[force], func(t *testing.T) {
			s := backfillBehaviorStore(t)
			require.NoError(t, s.CreateNode(t.Context(), backfillBehaviorNode(1)))
			before := backfillAllState(t, s)
			original := backfillEvents(t, s)
			result, err := s.Backfill(t.Context(), force)
			if !force {
				require.ErrorIs(t, err, sqlite.ErrBackfillSyncEventsNonEmpty)
				require.Zero(t, result)
				require.Equal(t, before, backfillAllState(t, s))
				return
			}
			require.NoError(t, err)
			require.Equal(t, sqlite.BackfillResult{NodeCount: 1, CreateEvents: 1, TotalEvents: 1}, result)
			after := backfillEvents(t, s)
			require.Len(t, after, len(original)+1)
			require.Equal(t, original, after[:len(original)])
			require.NotEqual(t, original[0].EventID, after[len(original)].EventID)
			require.Equal(t, model.OpCreateNode, after[len(original)].OpType)
			require.Equal(t, original[0].UID, after[len(original)].UID)
			require.Equal(t, backfillCreated.UnixMilli(), after[len(original)].WallClockTS)
			raw := backfillAllState(t, s)
			require.Equal(t, before["events"], raw["events"][:len(before["events"])])
			require.Equal(t, before["nodes"], raw["nodes"])
			require.Equal(t, before["dependencies"], raw["dependencies"])
		})
	}
}

func TestBackfillRefusal_MalformedLegacySource_RollsBackJournalAndMetadata(t *testing.T) {
	tests := []struct{ name, column, value, category string }{
		{"annotation syntax", "annotations", "[", "annotations"},
		{"annotation shape", "annotations", "{}", "annotations"},
		{"created time", "created_at", "not-a-time", "created_at"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := backfillBehaviorStore(t)
			for _, seq := range []int{1, 2} {
				n := backfillBehaviorNode(seq)
				n.CreatedAt = backfillCreated.Add(time.Duration(seq) * time.Minute)
				require.NoError(t, s.CreateNode(t.Context(), n))
			}
			query := "UPDATE nodes SET annotations=? WHERE id=?"
			if tt.column == "created_at" {
				query = "UPDATE nodes SET created_at=? WHERE id=?"
			}
			_, err := s.WriteDB().ExecContext(t.Context(), query, tt.value, "TEST-2")
			require.NoError(t, err)
			clearBackfillJournal(t, s)
			before := backfillAllState(t, s)
			result, err := s.Backfill(t.Context(), false)
			require.Error(t, err)
			require.Zero(t, result)
			require.Contains(t, err.Error(), tt.category)
			require.Contains(t, err.Error(), "TEST-2")
			if tt.column == "annotations" {
				var syntax *json.SyntaxError
				var shape *json.UnmarshalTypeError
				require.True(t, jsonErrorCause(err, &syntax, &shape))
			} else {
				var parse *time.ParseError
				require.ErrorAs(t, err, &parse)
			}
			require.Equal(t, before, backfillAllState(t, s))
			count, err := s.CountSyncEvents(t.Context())
			require.NoError(t, err)
			require.Zero(t, count)
		})
	}
}

func jsonErrorCause(err error, syntax **json.SyntaxError, shape **json.UnmarshalTypeError) bool {
	return errors.As(err, syntax) || errors.As(err, shape)
}

func TestBackfillRefusal_PreCanceledContext_PreservesOwnedState(t *testing.T) {
	for _, dry := range []bool{false, true} {
		t.Run(map[bool]string{false: "write", true: "dry run"}[dry], func(t *testing.T) {
			s := backfillBehaviorStore(t)
			seedBackfillRich(t, s)
			before := backfillAllState(t, s)
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			var result sqlite.BackfillResult
			var err error
			if dry {
				result, err = s.BackfillDryRun(ctx)
			} else {
				result, err = s.Backfill(ctx, false)
			}
			require.ErrorIs(t, err, context.Canceled)
			require.Zero(t, result)
			require.Equal(t, before, backfillAllState(t, s))
		})
	}
}

func TestBackfillRefusal_ClosedStore_ReturnsErrorWithoutPersistentChange(t *testing.T) {
	for _, dry := range []bool{false, true} {
		t.Run(map[bool]string{false: "write", true: "dry run"}[dry], func(t *testing.T) {
			t.Setenv(sqlite.AuthorIDEnv, "")
			path := filepath.Join(t.TempDir(), "legacy.db")
			s, err := sqlite.New(path, slog.Default())
			require.NoError(t, err)
			closed := false
			t.Cleanup(func() {
				if !closed {
					require.NoError(t, s.Close())
				}
			})
			require.NoError(t, s.CreateNode(t.Context(), backfillBehaviorNode(1)))
			clearBackfillJournal(t, s)
			before := backfillSourceState(t, s)
			require.NoError(t, s.Close())
			closed = true
			var result sqlite.BackfillResult
			if dry {
				result, err = s.BackfillDryRun(t.Context())
			} else {
				result, err = s.Backfill(t.Context(), false)
			}
			require.Error(t, err)
			require.Contains(t, err.Error(), "closed")
			require.Zero(t, result)
			reopened, err := sqlite.New(path, slog.Default())
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, reopened.Close()) })
			require.Equal(t, before, backfillSourceState(t, reopened))
			count, err := reopened.CountSyncEvents(t.Context())
			require.NoError(t, err)
			require.Zero(t, count)
		})
	}
}
