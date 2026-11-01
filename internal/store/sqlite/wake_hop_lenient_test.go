// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"bytes"
	"log/slog"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/testutil"
	"github.com/stretchr/testify/require"
)

func storedHopFixture(t *testing.T) (*sqlite.Store, model.WakeKey) {
	t.Helper()
	testutil.OperatorState(t)
	s, _ := mutationTestStore(t)
	mustCreateNode(t, s, "MTIX-1", "")
	annotation := model.Annotation{
		ID: "hop-read", Author: "sender", Text: "private comment body",
		Addressee: "worker", Hop: 1, CreatedAt: time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC),
	}
	require.NoError(t, s.AppendAnnotation(t.Context(), "MTIX-1", annotation))
	entries, err := s.InboxList(t.Context(), "worker")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	return s, model.WakeKey{AgentID: "worker", EventSeq: entries[0].Seq}
}

func TestWakeEventHop_MalformedMetadata_ZeroWarnAndUnchanged(t *testing.T) {
	s, key := storedHopFixture(t)
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	for _, tt := range []struct{ name, hop string }{
		{"null", "null"}, {"negative", "-1"}, {"fraction", "1.5"}, {"string", `"private-hop-token"`},
		{"overflow", "9223372036854775808"}, {"object", "{}"}, {"array", "[]"}, {"boolean", "true"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			raw := `{"author_id":"sender","body":"private comment body","to":"worker","hop":` + tt.hop + `}`
			_, err := s.WriteDB().ExecContext(t.Context(), "UPDATE sync_events SET payload=? WHERE rowid=?", raw, key.EventSeq)
			require.NoError(t, err)
			logs.Reset()
			hop, err := s.WakeEventHop(t.Context(), key)
			require.NoError(t, err)
			require.Zero(t, hop)
			require.Contains(t, logs.String(), "invalid comment hop; using 0")
			require.NotContains(t, logs.String(), "private-hop-token")
			require.NotContains(t, logs.String(), "private comment body")
			var after string
			require.NoError(t, s.ReadDB().QueryRowContext(t.Context(), "SELECT payload FROM sync_events WHERE rowid=?", key.EventSeq).Scan(&after))
			require.Equal(t, raw, after, "metadata reads must not rewrite immutable events")
		})
	}
}

func TestWakeEventHop_ValidMetadata_NoWarning(t *testing.T) {
	s, key := storedHopFixture(t)
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	for _, tt := range []struct {
		raw  string
		want int64
	}{
		{`{"to":"worker"}`, 0}, {`{"to":"worker","hop":0}`, 0}, {`{"to":"worker","hop":32}`, 32},
		{`{"to":"worker","hop":33}`, 33}, {`{"to":"worker","hop":9223372036854775807}`, 9223372036854775807},
		{`{"body":"unaddressed","hop":33}`, 0},
	} {
		_, err := s.WriteDB().ExecContext(t.Context(), "UPDATE sync_events SET payload=? WHERE rowid=?", tt.raw, key.EventSeq)
		require.NoError(t, err)
		logs.Reset()
		hop, err := s.WakeEventHop(t.Context(), key)
		require.NoError(t, err)
		require.Equal(t, tt.want, hop)
		require.Empty(t, logs.String())
	}
}
