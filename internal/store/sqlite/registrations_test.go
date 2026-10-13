// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func newRegistrationStore(t *testing.T) *sqlite.Store {
	t.Helper()
	s, err := sqlite.New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func insertJournalEvents(t *testing.T, s *sqlite.Store, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		_, err := s.WriteDB().Exec(`INSERT INTO sync_events (event_id,project_prefix,node_id,op_type,payload,wall_clock_ts,lamport_clock,vector_clock,author_id,author_machine_hash,sync_status,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
			"ev-"+time.Duration(i).String(), "T", "T-1", "comment", `{"to":"w","text":"x"}`, 1, i+1, "{}", "a", "0123456789abcdef", "pending", "2026-10-12T00:00:00Z")
		require.NoError(t, err)
	}
}

func keyFor(agent string) sqlite.RegistrationKey {
	return sqlite.RegistrationKey{AgentID: agent, HostID: "host-a", UID: "501"}
}

func TestRegisterSessionAtomic_InitialHighWater_IsJournalMax(t *testing.T) {
	s := newRegistrationStore(t)
	insertJournalEvents(t, s, 50)
	var seen int64
	got, err := s.RegisterSessionAtomic(context.Background(), keyFor("w"), func(existing *sqlite.Registration, journalMax int64) (*sqlite.Registration, error) {
		assert.Nil(t, existing)
		seen = journalMax
		return &sqlite.Registration{AgentID: "w", HostID: "host-a", UID: "501", Harness: "codex", Origin: "manual",
			HighWater: journalMax, RegisteredAt: "2026-10-12T00:00:00Z", LastSeen: "2026-10-12T00:00:00Z", RegistrarVersion: "t", AttachProtocol: 1}, nil
	})
	require.NoError(t, err)
	assert.EqualValues(t, 50, seen)
	assert.EqualValues(t, 50, got.HighWater)
}

func TestRegisterSessionAtomic_DecideNil_LeavesRowUntouched(t *testing.T) {
	s := newRegistrationStore(t)
	ctx := context.Background()
	_, err := s.RegisterSessionAtomic(ctx, keyFor("w"), func(*sqlite.Registration, int64) (*sqlite.Registration, error) {
		return &sqlite.Registration{AgentID: "w", HostID: "host-a", UID: "501", Harness: "codex", Origin: "manual", Epoch: 3,
			RegisteredAt: "r", LastSeen: "l", RegistrarVersion: "t", AttachProtocol: 1}, nil
	})
	require.NoError(t, err)
	got, err := s.RegisterSessionAtomic(ctx, keyFor("w"), func(existing *sqlite.Registration, _ int64) (*sqlite.Registration, error) {
		require.NotNil(t, existing)
		assert.EqualValues(t, 3, existing.Epoch)
		return nil, nil
	})
	require.NoError(t, err)
	assert.EqualValues(t, 3, got.Epoch)
}

func TestListRegistrations_OtherUID_Ignored(t *testing.T) {
	s := newRegistrationStore(t)
	ctx := context.Background()
	for _, uid := range []string{"501", "502"} {
		key := sqlite.RegistrationKey{AgentID: "w", HostID: "host-a", UID: uid}
		_, err := s.RegisterSessionAtomic(ctx, key, func(*sqlite.Registration, int64) (*sqlite.Registration, error) {
			return &sqlite.Registration{AgentID: "w", HostID: "host-a", UID: uid, Harness: "codex", Origin: "manual",
				RegisteredAt: "r", LastSeen: "l", RegistrarVersion: "t", AttachProtocol: 1}, nil
		})
		require.NoError(t, err)
	}
	rows, err := s.ListRegistrations(ctx, "host-a", "501")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "501", rows[0].UID)
}

func TestTouchRegistration_OnlyMatchingHandle_UpdatesLastSeen(t *testing.T) {
	s := newRegistrationStore(t)
	ctx := context.Background()
	_, err := s.RegisterSessionAtomic(ctx, keyFor("w"), func(*sqlite.Registration, int64) (*sqlite.Registration, error) {
		return &sqlite.Registration{AgentID: "w", HostID: "host-a", UID: "501", Harness: "codex", Origin: "session-env", SessionHandle: "h1",
			RegisteredAt: "r", LastSeen: "2026-10-12T00:00:00Z", RegistrarVersion: "t", AttachProtocol: 1}, nil
	})
	require.NoError(t, err)

	n, err := s.TouchRegistration(ctx, keyFor("w"), "other", "2026-10-12T01:00:00Z")
	require.NoError(t, err)
	assert.Zero(t, n)
	n, err = s.TouchRegistration(ctx, keyFor("w"), "", "2026-10-12T01:00:00Z")
	require.NoError(t, err)
	assert.Zero(t, n, "an empty handle never matches")
	n, err = s.TouchRegistration(ctx, keyFor("w"), "h1", "2026-10-12T02:00:00Z")
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
	rows, err := s.ListRegistrations(ctx, "host-a", "501")
	require.NoError(t, err)
	assert.Equal(t, "2026-10-12T02:00:00Z", rows[0].LastSeen)
}

func TestDeleteRegistration_RemovesRowAndReportsCount(t *testing.T) {
	s := newRegistrationStore(t)
	ctx := context.Background()
	_, err := s.RegisterSessionAtomic(ctx, keyFor("w"), func(*sqlite.Registration, int64) (*sqlite.Registration, error) {
		return &sqlite.Registration{AgentID: "w", HostID: "host-a", UID: "501", Harness: "codex", Origin: "manual",
			RegisteredAt: "r", LastSeen: "l", RegistrarVersion: "t", AttachProtocol: 1}, nil
	})
	require.NoError(t, err)
	stale := sqlite.RegistrationSnapshot{Epoch: 0, LastSeen: "other"}
	n, err := s.DeleteRegistrationIf(ctx, keyFor("w"), stale)
	require.NoError(t, err)
	assert.Zero(t, n, "a row that changed since it was read survives")
	snap := sqlite.RegistrationSnapshot{Epoch: 0, LastSeen: "l"}
	n, err = s.DeleteRegistrationIf(ctx, keyFor("w"), snap)
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
	n, err = s.DeleteRegistrationIf(ctx, keyFor("w"), snap)
	require.NoError(t, err)
	assert.Zero(t, n)
}

func TestInboxList_SessionHandleInContext_TouchesLastSeen(t *testing.T) {
	s := newRegistrationStore(t)
	ctx := context.Background()
	_, err := s.RegisterSessionAtomic(ctx, keyFor("w"), func(*sqlite.Registration, int64) (*sqlite.Registration, error) {
		return &sqlite.Registration{AgentID: "w", HostID: "host-a", UID: "501", Harness: "codex", Origin: "session-env", SessionHandle: "h1",
			RegisteredAt: "r", LastSeen: "2000-01-01T00:00:00Z", RegistrarVersion: "t", AttachProtocol: 1}, nil
	})
	require.NoError(t, err)

	_, err = s.InboxList(ctx, "w")
	require.NoError(t, err)
	rows, _ := s.ListRegistrations(ctx, "host-a", "501")
	assert.Equal(t, "2000-01-01T00:00:00Z", rows[0].LastSeen, "no handle in context: last_seen untouched")

	tctx := sqlite.WithSessionTouch(ctx, sqlite.SessionTouch{HostID: "host-a", UID: "501", Handle: "h1"})
	_, err = s.InboxList(tctx, "w")
	require.NoError(t, err)
	rows, _ = s.ListRegistrations(ctx, "host-a", "501")
	assert.NotEqual(t, "2000-01-01T00:00:00Z", rows[0].LastSeen)

	rows[0].LastSeen = "x"
	_, err = s.WriteDB().Exec(`UPDATE agent_registrations SET last_seen='1999-01-01T00:00:00Z'`)
	require.NoError(t, err)
	_, err = s.InboxList(sqlite.WithSessionTouch(ctx, sqlite.SessionTouch{HostID: "host-a", UID: "501", Handle: "wrong"}), "w")
	require.NoError(t, err)
	rows, _ = s.ListRegistrations(ctx, "host-a", "501")
	assert.Equal(t, "1999-01-01T00:00:00Z", rows[0].LastSeen, "a foreign handle never touches the row")
}

func TestInboxAck_SessionHandleInContext_TouchesLastSeen(t *testing.T) {
	s := newRegistrationStore(t)
	ctx := context.Background()
	insertJournalEvents(t, s, 2)
	_, err := s.RegisterSessionAtomic(ctx, keyFor("w"), func(*sqlite.Registration, int64) (*sqlite.Registration, error) {
		return &sqlite.Registration{AgentID: "w", HostID: "host-a", UID: "501", Harness: "codex", Origin: "session-env", SessionHandle: "h1",
			RegisteredAt: "r", LastSeen: "2000-01-01T00:00:00Z", RegistrarVersion: "t", AttachProtocol: 1}, nil
	})
	require.NoError(t, err)
	tctx := sqlite.WithSessionTouch(ctx, sqlite.SessionTouch{HostID: "host-a", UID: "501", Handle: "h1"})

	require.NoError(t, s.InboxAck(tctx, "w", 1))
	rows, _ := s.ListRegistrations(ctx, "host-a", "501")
	assert.NotEqual(t, "2000-01-01T00:00:00Z", rows[0].LastSeen)

	_, err = s.WriteDB().Exec(`UPDATE agent_registrations SET last_seen='1999-01-01T00:00:00Z'`)
	require.NoError(t, err)
	require.NoError(t, s.InboxAckThrough(tctx, "w", 2))
	rows, _ = s.ListRegistrations(ctx, "host-a", "501")
	assert.NotEqual(t, "1999-01-01T00:00:00Z", rows[0].LastSeen)

	_, err = s.WriteDB().Exec(`UPDATE agent_registrations SET last_seen='1998-01-01T00:00:00Z'`)
	require.NoError(t, err)
	require.Error(t, s.InboxAck(tctx, "w", 99), "an out-of-journal ack fails")
	rows, _ = s.ListRegistrations(ctx, "host-a", "501")
	assert.Equal(t, "1998-01-01T00:00:00Z", rows[0].LastSeen, "a failed ack never refreshes last_seen")
}

func TestRegisterSessionAtomic_DecideError_RollsBackAndPropagates(t *testing.T) {
	s := newRegistrationStore(t)
	boom := errors.New("refused")
	_, err := s.RegisterSessionAtomic(context.Background(), keyFor("w"), func(*sqlite.Registration, int64) (*sqlite.Registration, error) {
		return nil, boom
	})
	require.ErrorIs(t, err, boom)
	rows, _ := s.ListRegistrations(context.Background(), "host-a", "501")
	assert.Empty(t, rows)
}

func TestRegistrationStore_ClosedStore_ReturnsWrappedErrors(t *testing.T) {
	s := newRegistrationStore(t)
	require.NoError(t, s.Close())
	ctx := context.Background()
	_, err := s.ListRegistrations(ctx, "h", "1")
	assert.Error(t, err)
	_, err = s.DeleteRegistrationIf(ctx, keyFor("w"), sqlite.RegistrationSnapshot{})
	assert.Error(t, err)
	_, err = s.TouchRegistration(ctx, keyFor("w"), "h", "now")
	assert.Error(t, err)
	_, err = s.RegisterSessionAtomic(ctx, keyFor("w"), func(*sqlite.Registration, int64) (*sqlite.Registration, error) { return nil, nil })
	assert.Error(t, err)
}

func TestRegisterSessionAtomic_ConcurrentSessions_ExactlyOneWins(t *testing.T) {
	s := newRegistrationStore(t)
	ctx := context.Background()
	const racers = 12
	var wg sync.WaitGroup
	wins := make(chan string, racers)
	for i := 0; i < racers; i++ {
		handle := fmt.Sprintf("h%d", i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.RegisterSessionAtomic(ctx, keyFor("w"), func(existing *sqlite.Registration, _ int64) (*sqlite.Registration, error) {
				if existing != nil {
					return nil, errors.New("live row")
				}
				return &sqlite.Registration{AgentID: "w", HostID: "host-a", UID: "501", Harness: "codex", Origin: "manual", SessionHandle: handle,
					RegisteredAt: "r", LastSeen: "l", RegistrarVersion: "t", AttachProtocol: 1}, nil
			})
			if err == nil {
				wins <- handle
			}
		}()
	}
	wg.Wait()
	close(wins)
	assert.Len(t, wins, 1, "the existing row is read inside the write transaction, so exactly one racer sees an empty slot")
}

// A deterministic barrier, not timing: racer A holds the write transaction
// open inside its decide function until racer B is provably blocked waiting
// for the single write connection (the pool's WaitCount rises). Whatever B
// reads before it blocks was read while A's row did not exist yet, so a
// decide that reads outside the transaction sees an empty slot and both win.
func TestRegisterSessionAtomic_SecondRacerBlockedBehindFirst_SeesFirstRow(t *testing.T) {
	s := newRegistrationStore(t)
	ctx := context.Background()
	row := func(handle string) *sqlite.Registration {
		return &sqlite.Registration{AgentID: "w", HostID: "host-a", UID: "501", Harness: "codex", Origin: "manual", SessionHandle: handle,
			RegisteredAt: "r", LastSeen: "l", RegistrarVersion: "t", AttachProtocol: 1}
	}
	inside, release := make(chan struct{}), make(chan struct{})
	aErr := make(chan error, 1)
	go func() {
		_, err := s.RegisterSessionAtomic(ctx, keyFor("w"), func(existing *sqlite.Registration, _ int64) (*sqlite.Registration, error) {
			close(inside)
			<-release
			return row("a"), nil
		})
		aErr <- err
	}()
	<-inside

	before := s.WriteDB().Stats().WaitCount
	var seenByB *sqlite.Registration
	bErr := make(chan error, 1)
	go func() {
		_, err := s.RegisterSessionAtomic(ctx, keyFor("w"), func(existing *sqlite.Registration, _ int64) (*sqlite.Registration, error) {
			seenByB = existing
			if existing != nil {
				return nil, errors.New("live row")
			}
			return row("b"), nil
		})
		bErr <- err
	}()
	deadline := time.Now().Add(30 * time.Second)
	for s.WriteDB().Stats().WaitCount == before {
		require.True(t, time.Now().Before(deadline), "racer B never blocked on the write connection")
		runtime.Gosched()
	}
	close(release)

	require.NoError(t, <-aErr)
	require.Error(t, <-bErr, "B must see A's committed row and be refused")
	require.NotNil(t, seenByB)
	assert.Equal(t, "a", seenByB.SessionHandle)
}
