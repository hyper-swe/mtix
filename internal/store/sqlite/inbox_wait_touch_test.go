// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"bytes"
	"context"
	"log/slog"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
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
func touchWaitStore(t *testing.T) (*sqlite.Store, *syncBuffer) {
	t.Helper()
	logs := &syncBuffer{}
	s, err := sqlite.New(filepath.Join(t.TempDir(), "touch.db"), slog.New(slog.NewTextHandler(logs, nil)))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	_, err = s.RegisterSessionAtomic(context.Background(), keyFor("w"), func(*sqlite.Registration, int64) (*sqlite.Registration, error) {
		return &sqlite.Registration{AgentID: "w", HostID: "host-a", UID: "501", Harness: "claude", Origin: "session-env",
			SessionHandle: "h1", RegisteredAt: "r", LastSeen: "2000-01-01T00:00:00Z", RegistrarVersion: "t", AttachProtocol: 1}, nil
	})
	require.NoError(t, err)
	return s, logs
}

func lastSeen(t *testing.T, s *sqlite.Store) string {
	t.Helper()
	rows, err := s.ListRegistrations(context.Background(), "host-a", "501")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	return rows[0].LastSeen
}

// TestInboxWait_TimeoutWithSessionTouch_NoWarn pins MTIX-112.17.19: a poll whose
// query finished just before the wait deadline must not refresh last_seen on the
// expired wait context. The injected clock is read right before the touch. The
// first read runs on the caller's context (MTIX-170); on the second poll, which
// runs on the wait context, the clock sleeps past the deadline, which reproduces
// the race deterministically.
func TestInboxWait_TimeoutWithSessionTouch_NoWarn(t *testing.T) {
	s, logs := touchWaitStore(t)
	const timeout = 300 * time.Millisecond // the second poll (250 ms) lands before the deadline
	touched := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	var calls atomic.Int64
	s.SetClock(func() time.Time {
		if calls.Add(1) == 2 {
			time.Sleep(timeout) // the touch now starts after the wait deadline
		}
		return touched
	})
	ctx := sqlite.WithSessionTouch(context.Background(), sqlite.SessionTouch{HostID: "host-a", UID: "501", Handle: "h1"})

	events, err := s.InboxWait(ctx, "w", timeout)

	require.NoError(t, err, "a plain timeout is an empty success")
	assert.Empty(t, events)
	require.GreaterOrEqual(t, calls.Load(), int64(2), "the second poll must have touched")
	assert.NotContains(t, logs.String(), "refresh registration last_seen", "a normal timeout must not log a WARN")
	assert.Equal(t, touched.Format(time.RFC3339), lastSeen(t, s), "the registered session's last_seen still advances")
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
			ctx := sqlite.WithSessionTouch(context.Background(), sqlite.SessionTouch{HostID: "host-a", UID: "501", Handle: tt.handle})

			_, err := s.InboxWait(ctx, "w", 50*time.Millisecond)

			require.NoError(t, err)
			assert.Equal(t, tt.want, lastSeen(t, s))
			assert.NotContains(t, logs.String(), "level=WARN")
		})
	}
}
