// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Inbox waits accept zero as unbounded and release database resources between polls (FR-19).
package sqlite

import (
	"context"
	"database/sql"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

type inboxWaitOutcome struct {
	events []InboxEvent
	err    error
}

func inboxWaitTime() time.Time { return time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC) }

func inboxWaitStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(filepath.Join(t.TempDir(), "inbox.db"), slog.Default())
	require.NoError(t, err)
	s.SetClock(inboxWaitTime)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	now := inboxWaitTime()
	require.NoError(t, s.CreateNode(t.Context(), &model.Node{
		ID: "TEST-1", Project: "TEST", Seq: 1, Title: "Inbox wait target",
		NodeType: model.NodeTypeStory, Status: model.StatusOpen,
		Priority: model.PriorityMedium, Weight: 1, Creator: "tester", CreatedAt: now, UpdatedAt: now,
	}))
	return s
}

// Each successful query touches the calling session exactly once. Counting its
// injected timestamp observes real polls without a production query hook.
func observeInboxWait(t *testing.T, s *Store) (context.Context, *atomic.Int64, <-chan struct{}) {
	t.Helper()
	calls := &atomic.Int64{}
	first := make(chan struct{})
	s.SetClock(func() time.Time {
		if calls.Add(1) == 1 {
			close(first)
		}
		return inboxWaitTime()
	})
	ctx := WithSessionTouch(t.Context(), SessionTouch{HostID: "test-host", UID: "test-user", Handle: "test-session"})
	return ctx, calls, first
}

func startInboxWait(ctx context.Context, t *testing.T, s *Store, timeout time.Duration) <-chan inboxWaitOutcome {
	t.Helper()
	ctx, cancel := context.WithCancel(ctx)
	out := make(chan inboxWaitOutcome, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		events, err := s.InboxWait(ctx, "worker", timeout)
		out <- inboxWaitOutcome{events, err}
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Error("inbox wait goroutine did not stop")
		}
	})
	return out
}

func awaitInboxPoll(t *testing.T, first <-chan struct{}) {
	t.Helper()
	select {
	case <-first:
	case <-time.After(5 * time.Second):
		t.Fatal("inbox wait did not query")
	}
}

func requireInboxStillWaiting(t *testing.T, out <-chan inboxWaitOutcome) {
	t.Helper()
	select {
	case result := <-out:
		t.Fatalf("zero timeout returned before an event or cancellation: events=%v error=%v", result.events, result.err)
	case <-time.After(80 * time.Millisecond):
	}
}

func TestInboxWait_TimeoutZero_ReturnsOnLaterEvent(t *testing.T) {
	for _, source := range []string{"addressed journal event", "hook delivery", "sync apply"} {
		t.Run(source, func(t *testing.T) {
			s := inboxWaitStore(t)
			writer, err := New(s.dbPath, slog.Default())
			require.NoError(t, err)
			defer func() { require.NoError(t, writer.Close()) }()
			ctx, _, first := observeInboxWait(t, s)
			out := startInboxWait(ctx, t, s, 0)
			awaitInboxPoll(t, first)
			requireInboxStillWaiting(t, out)
			addressee := "worker"
			if source == "hook delivery" {
				addressee = ""
			}
			if source == "sync apply" {
				event := makeApplyEvent(t, model.OpComment, "TEST-1", "remote-sender", 100,
					&model.CommentPayload{AuthorID: "remote-sender", Body: "Later event", To: "worker"})
				event.ProjectPrefix = "TEST"
				target, err := writer.GetNode(t.Context(), "TEST-1")
				require.NoError(t, err)
				event.UID = target.UID
				event.WallClockTS = inboxWaitTime().UnixMilli()
				require.NoError(t, writer.WithTx(t.Context(), func(tx *sql.Tx) error {
					return writer.IdempotentApply(t.Context(), tx, event)
				}))
			} else {
				require.NoError(t, writer.SetAnnotations(t.Context(), "TEST-1", []model.Annotation{{
					ID: "wait-event", Author: "sender", Text: "Later event", Addressee: addressee, CreatedAt: inboxWaitTime(),
				}}))
			}
			if source == "hook delivery" {
				var seq int64
				require.NoError(t, writer.readDB.QueryRowContext(t.Context(), `SELECT MAX(rowid) FROM sync_events WHERE op_type = ?`, "comment").Scan(&seq))
				require.NoError(t, writer.RecordInboxDelivery(t.Context(), "worker", seq, "test-hook"))
			}
			select {
			case result := <-out:
				require.NoError(t, result.err)
				require.Len(t, result.events, 1)
				assert.Equal(t, "Later event", result.events[0].Body)
			case <-time.After(5 * time.Second):
				t.Fatal("wait did not return the separately delivered event")
			}
			events, err := s.InboxList(t.Context(), "worker")
			require.NoError(t, err)
			require.Len(t, events, 1, "waiting must never acknowledge an event")
		})
	}
}

func TestInboxWait_TimeoutZero_PendingReturnsImmediately(t *testing.T) {
	s := inboxWaitStore(t)
	require.NoError(t, s.SetAnnotations(t.Context(), "TEST-1", []model.Annotation{{
		ID: "pending", Author: "sender", Text: "Pending event", Addressee: "worker", CreatedAt: inboxWaitTime(),
	}}))
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	events, err := s.InboxWait(ctx, "worker", 0)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, "Pending event", events[0].Body)
}

func TestInboxWait_NegativeTimeout_Refused(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty", true: "pending"}[pending], func(t *testing.T) {
			s := inboxWaitStore(t)
			if pending {
				require.NoError(t, s.SetAnnotations(t.Context(), "TEST-1", []model.Annotation{{
					ID: "pending", Author: "sender", Text: "Pending", Addressee: "worker", CreatedAt: inboxWaitTime(),
				}}))
			}
			events, err := s.InboxWait(t.Context(), "worker", -time.Second)
			require.ErrorIs(t, err, model.ErrInvalidInput)
			assert.Empty(t, events)
		})
	}
}

func TestInboxWait_TimeoutZero_NoBusyLoop(t *testing.T) {
	s := inboxWaitStore(t)
	ctx, calls, first := observeInboxWait(t, s)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	out := startInboxWait(ctx, t, s, 0)
	awaitInboxPoll(t, first)
	select {
	case result := <-out:
		t.Fatalf("unbounded wait returned: %+v", result)
	case <-time.After(875 * time.Millisecond):
	}
	assert.GreaterOrEqual(t, calls.Load(), int64(2), "must continue polling after the immediate query")
	assert.LessOrEqual(t, calls.Load(), int64(6), "250ms polling must not spin")
	cancel()
	result := <-out
	require.ErrorIs(t, result.err, context.Canceled)
}

func TestInboxWait_TimeoutZero_ReleasesReadAndWriteLocks(t *testing.T) {
	s := inboxWaitStore(t)
	ctx, _, first := observeInboxWait(t, s)
	out := startInboxWait(ctx, t, s, 0)
	awaitInboxPoll(t, first)
	requireInboxStillWaiting(t, out)
	assert.Zero(t, s.readDB.Stats().InUse, "read connection must be released while sleeping")
	assert.Zero(t, s.writeDB.Stats().InUse, "session touch must not hold the writer while sleeping")
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	var busy, logFrames, checkpointed int
	require.NoError(t, s.writeDB.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logFrames, &checkpointed))
	assert.Zero(t, busy, "a sleeping waiter must not retain a read snapshot that blocks checkpointing")
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "TEST-2", Project: "TEST", Seq: 2, Title: "Concurrent write",
		NodeType: model.NodeTypeStory, Status: model.StatusOpen, Weight: 1, Creator: "tester",
		CreatedAt: inboxWaitTime(), UpdatedAt: inboxWaitTime(),
	}))
}

func TestInboxWait_TimeoutZero_CancellationPreservesError(t *testing.T) {
	for _, tc := range []struct {
		name     string
		timeout  time.Duration
		deadline bool
		want     error
	}{
		{"unbounded cancellation", 0, false, context.Canceled},
		{"unbounded parent deadline", 0, true, context.DeadlineExceeded},
		{"bounded parent deadline", time.Minute, true, context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := inboxWaitStore(t)
			ctx := t.Context()
			var cancel context.CancelFunc
			if tc.deadline {
				ctx, cancel = context.WithTimeout(ctx, 100*time.Millisecond)
			} else {
				ctx, cancel = context.WithCancel(ctx)
			}
			defer cancel()
			if !tc.deadline {
				time.AfterFunc(100*time.Millisecond, cancel)
			}
			events, err := s.InboxWait(ctx, "worker", tc.timeout)
			require.ErrorIs(t, err, tc.want)
			assert.Empty(t, events)
		})
	}
}

func TestInboxWait_TimeoutZero_StoreFailureReturnsError(t *testing.T) {
	for _, failure := range []string{"closed store", "migration replaced journal"} {
		t.Run(failure, func(t *testing.T) {
			s := inboxWaitStore(t)
			ctx, _, first := observeInboxWait(t, s)
			out := startInboxWait(ctx, t, s, 0)
			awaitInboxPoll(t, first)
			requireInboxStillWaiting(t, out)
			if failure == "closed store" {
				require.NoError(t, s.Close())
			} else {
				_, err := s.writeDB.ExecContext(t.Context(), `ALTER TABLE sync_events RENAME TO migrating_events`)
				require.NoError(t, err)
			}
			select {
			case result := <-out:
				require.Error(t, result.err, "a database failure must not become an empty timeout")
				assert.Empty(t, result.events)
				assert.Contains(t, result.err.Error(), "inbox")
			case <-time.After(5 * time.Second):
				t.Fatal("wait did not report database failure")
			}
		})
	}
}

func TestInboxWait_PositiveTimeout_FrozenClockStillExpires(t *testing.T) {
	s := inboxWaitStore(t)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	start := time.Now()
	events, err := s.InboxWait(ctx, "worker", 60*time.Millisecond)
	require.NoError(t, err, "only the wait's own timeout returns nil,nil")
	assert.Empty(t, events)
	assert.GreaterOrEqual(t, time.Since(start), 60*time.Millisecond)
}
