// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"bytes"
	"context"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// syncBuffer is a log sink safe for the wait goroutine and the test.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// touchWaitStore is a store with a registered session "h1" for agent "w" and a
// captured logger.
func touchWaitStore(t *testing.T) (*Store, *syncBuffer) {
	t.Helper()
	logs := &syncBuffer{}
	s, err := New(filepath.Join(t.TempDir(), "touch.db"), slog.New(slog.NewTextHandler(logs, nil)))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	_, err = s.RegisterSessionAtomic(context.Background(), RegistrationKey{AgentID: "w", HostID: "host-a", UID: "501"}, func(*Registration, int64) (*Registration, error) {
		return &Registration{AgentID: "w", HostID: "host-a", UID: "501", Harness: "claude", Origin: "session-env",
			SessionHandle: "h1", RegisteredAt: "r", LastSeen: "2000-01-01T00:00:00Z", RegistrarVersion: "t", AttachProtocol: 1}, nil
	})
	require.NoError(t, err)
	return s, logs
}

func lastSeen(t *testing.T, s *Store) string {
	t.Helper()
	rows, err := s.ListRegistrations(context.Background(), "host-a", "501")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	return rows[0].LastSeen
}

// TestInboxWait_TimeoutWithSessionTouch_NoWarn pins MTIX-112.17.19 without
// requiring a second poll to start before a real-time deadline. The internal
// touch boundary receives an already-expired wait context; the public wait path
// separately verifies that its own timeout remains an empty success without WARN.
func TestInboxWait_TimeoutWithSessionTouch_NoWarn(t *testing.T) {
	touched := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	t.Run("expired wait context", func(t *testing.T) {
		s, logs := touchWaitStore(t)
		s.SetClock(func() time.Time { return touched })
		ctx := WithSessionTouch(context.Background(), SessionTouch{HostID: "host-a", UID: "501", Handle: "h1"})
		waitCtx, cancel := context.WithDeadline(ctx, time.Unix(0, 0))
		defer cancel()
		require.ErrorIs(t, waitCtx.Err(), context.DeadlineExceeded)

		s.touchFromContext(waitCtx, "w")

		assert.NotContains(t, logs.String(), "refresh registration last_seen", "an expired wait context must not cause a WARN")
		assert.Equal(t, touched.Format(time.RFC3339), lastSeen(t, s), "the touch must use a fresh context")
	})
	t.Run("public timeout", func(t *testing.T) {
		s, logs := touchWaitStore(t)
		s.SetClock(func() time.Time { return touched })
		ctx := WithSessionTouch(context.Background(), SessionTouch{HostID: "host-a", UID: "501", Handle: "h1"})

		// Even an elapsed local timeout permits the initial caller-context read
		// and session touch. No assertion depends on the number of later polls.
		events, err := s.InboxWait(ctx, "w", time.Nanosecond)

		require.NoError(t, err, "a plain timeout is an empty success")
		assert.Empty(t, events)
		assert.NotContains(t, logs.String(), "refresh registration last_seen", "a normal timeout must not log a WARN")
		assert.Equal(t, touched.Format(time.RFC3339), lastSeen(t, s), "the public wait still touches the session")
	})
}

// TestInboxWait_SessionTouch_AdvancesLastSeen checks the ordinary path: a wait
// by the registered session moves last_seen, and another handle never does.
func TestInboxWait_SessionTouch_AdvancesLastSeen(t *testing.T) {
	tests := []struct {
		name   string
		handle string
		want   string
	}{
		{"registered session", "h1", "2026-10-15T09:00:00Z"},
		{"other session", "h2", "2000-01-01T00:00:00Z"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, logs := touchWaitStore(t)
			s.SetClock(func() time.Time { return time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC) })
			ctx := WithSessionTouch(context.Background(), SessionTouch{HostID: "host-a", UID: "501", Handle: tt.handle})

			_, err := s.InboxWait(ctx, "w", 50*time.Millisecond)

			require.NoError(t, err)
			assert.Equal(t, tt.want, lastSeen(t, s))
			assert.NotContains(t, logs.String(), "level=WARN")
		})
	}
}
