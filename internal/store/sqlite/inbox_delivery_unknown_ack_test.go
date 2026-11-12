// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// MTIX-112.3: an ack with no recorded time (restore writes an empty acked_at,
// carry writes none for a watermark ack) is a watermark ack of unknown time.
// It is never reported as selective and never gets a made-up time.
func TestInboxAckInfo_AckRowWithoutTime_IsWatermarkTimeUnknown(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "u.db"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	_, err = s.WriteDB().ExecContext(t.Context(), `INSERT INTO agent_inbox_ack(agent_id,event_seq,acked_at) VALUES('a',2,'')`)
	require.NoError(t, err)
	info, err := s.InboxAckInfo(t.Context(), "a", 2)
	require.NoError(t, err)
	require.Equal(t, AckInfo{Kind: AckWatermark}, info)
}

// A selective mark with no time keeps its TRUE kind: selective, time unknown.
func TestInboxAckInfo_FoldedSelectiveMarkWithoutTime_StaysSelectiveTimeUnknown(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "f.db"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	_, err = s.WriteDB().ExecContext(t.Context(), `INSERT INTO agent_inbox_cursor_marks(agent_id,cursor,from_cursor,kind,advanced_at) VALUES('a',2,1,'selective','')`)
	require.NoError(t, err)
	_, err = s.WriteDB().ExecContext(t.Context(), `INSERT INTO agent_inbox_cursor(agent_id,cursor) VALUES('a',2)`)
	require.NoError(t, err)
	info, err := s.InboxAckInfo(t.Context(), "a", 2)
	require.NoError(t, err)
	require.Equal(t, AckInfo{Kind: AckSelective}, info, "kind stays, time unknown")
}

// A watermark mark with no time stays a watermark of unknown time.
func TestInboxAckInfo_WatermarkMarkWithoutTime_StaysWatermarkTimeUnknown(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "w.db"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	_, err = s.WriteDB().ExecContext(t.Context(), `INSERT INTO agent_inbox_cursor_marks(agent_id,cursor,from_cursor,kind,advanced_at) VALUES('a',2,1,'watermark','')`)
	require.NoError(t, err)
	_, err = s.WriteDB().ExecContext(t.Context(), `INSERT INTO agent_inbox_cursor(agent_id,cursor) VALUES('a',2)`)
	require.NoError(t, err)
	info, err := s.InboxAckInfo(t.Context(), "a", 2)
	require.NoError(t, err)
	require.Equal(t, AckInfo{Kind: AckWatermark}, info)
}
