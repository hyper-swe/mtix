// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/procinfo"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// seedAddressed journals n comments addressed to agent and returns their seqs.
func seedAddressed(t *testing.T, s *sqlite.Store, agent string, n int) []int64 {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, s.CreateNode(ctx, makeRootNode("PROJ-1", "PROJ", "Task", now)))
	var anns []model.Annotation
	for i := 0; i < n; i++ {
		anns = append(anns, model.Annotation{ID: "01ANN" + string(rune('A'+i)), Author: "peer",
			Text: "note", CreatedAt: now.Add(time.Duration(i) * time.Second), Addressee: agent})
		require.NoError(t, s.SetAnnotations(ctx, "PROJ-1", anns))
	}
	events, err := s.InboxList(ctx, agent)
	require.NoError(t, err)
	require.Len(t, events, n)
	seqs := make([]int64, 0, n)
	for _, e := range events {
		seqs = append(seqs, e.Seq)
	}
	return seqs
}

func ticking(start time.Time) (func() time.Time, *time.Time) {
	cur := start
	return func() time.Time { return cur }, &cur
}

func TestInboxAckInfo_SelectiveAck_ShowsAckTimeAndKind(t *testing.T) {
	s := newTestStore(t)
	clock, cur := ticking(time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC))
	s.SetClock(clock)
	seqs := seedAddressed(t, s, "w", 2)
	info, err := s.InboxAckInfo(t.Context(), "w", seqs[0])
	require.NoError(t, err)
	require.Equal(t, sqlite.AckNone, info.Kind)
	require.Nil(t, info.AckedAt)
	*cur = cur.Add(time.Minute)
	require.NoError(t, s.InboxAck(t.Context(), "w", seqs[0]))
	info, err = s.InboxAckInfo(t.Context(), "w", seqs[0])
	require.NoError(t, err)
	require.Equal(t, sqlite.AckSelective, info.Kind)
	require.True(t, info.TimeKnown)
	require.WithinDuration(t, *cur, *info.AckedAt, time.Nanosecond)
	other, err := s.InboxAckInfo(t.Context(), "w", seqs[1])
	require.NoError(t, err)
	require.Equal(t, sqlite.AckNone, other.Kind)
	_, err = s.InboxAckInfo(t.Context(), "", 1)
	require.ErrorIs(t, err, model.ErrInvalidInput)
}

func TestInboxAckInfo_WatermarkAck_UsesMarkTimeAndOlderAcksAreUnknown(t *testing.T) {
	s := newTestStore(t)
	clock, cur := ticking(time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC))
	s.SetClock(clock)
	seqs := seedAddressed(t, s, "w", 3)
	*cur = cur.Add(time.Minute)
	require.NoError(t, s.InboxAckThrough(t.Context(), "w", seqs[1]))
	markAt := *cur
	*cur = cur.Add(time.Hour)
	for _, seq := range seqs[:2] {
		info, err := s.InboxAckInfo(t.Context(), "w", seq)
		require.NoError(t, err)
		require.Equal(t, sqlite.AckWatermark, info.Kind)
		require.True(t, info.TimeKnown)
		require.WithinDuration(t, markAt, *info.AckedAt, time.Nanosecond)
	}
	info, err := s.InboxAckInfo(t.Context(), "w", seqs[2])
	require.NoError(t, err)
	require.Equal(t, sqlite.AckNone, info.Kind)

	// An ack made before the marks existed reports "acked, time unknown".
	_, err = s.WriteDB().Exec(`DELETE FROM agent_inbox_cursor_marks`)
	require.NoError(t, err)
	info, err = s.InboxAckInfo(t.Context(), "w", seqs[0])
	require.NoError(t, err)
	require.Equal(t, sqlite.AckWatermark, info.Kind)
	require.False(t, info.TimeKnown)
	require.Nil(t, info.AckedAt)
}

func TestInboxLastRead_ThrottleEdgeAndAckAlwaysWrites(t *testing.T) {
	s := newTestStore(t)
	t0 := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	clock, cur := ticking(t0)
	s.SetClock(clock)
	seqs := seedAddressed(t, s, "w", 1) // the seeding read stamps t0
	read := func() time.Time {
		got, err := s.InboxLastRead(t.Context(), "w")
		require.NoError(t, err)
		require.NotNil(t, got)
		return *got
	}
	require.Equal(t, t0, read())
	tests := []struct {
		name string
		at   time.Duration
		want time.Duration
		ack  bool
	}{
		{"inside the throttle keeps the stamp", 29*time.Second + 999*time.Millisecond, 0, false},
		{"exactly the throttle moves it", 30 * time.Second, 30 * time.Second, false},
		{"just after a move is throttled again", 30*time.Second + 5*time.Second, 30 * time.Second, false},
		{"an ack inside the throttle still writes", 36 * time.Second, 36 * time.Second, true},
	}
	for _, tt := range tests {
		*cur = t0.Add(tt.at)
		if tt.ack {
			require.NoError(t, s.InboxAck(t.Context(), "w", seqs[0]), tt.name)
		} else {
			_, err := s.InboxList(t.Context(), "w")
			require.NoError(t, err, tt.name)
		}
		require.Equal(t, t0.Add(tt.want), read(), tt.name)
	}
}

func TestInboxLastRead_NeverReadAndUnregisteredAgents(t *testing.T) {
	s := newTestStore(t)
	got, err := s.InboxLastRead(t.Context(), "nobody")
	require.NoError(t, err)
	require.Nil(t, got)
	_, err = s.InboxList(t.Context(), "unregistered-agent")
	require.NoError(t, err)
	got, err = s.InboxLastRead(t.Context(), "unregistered-agent")
	require.NoError(t, err)
	require.NotNil(t, got, "any agent id gets a last read, registered or not")
}

func TestInboxLastRead_WakeScanContext_DoesNotStamp(t *testing.T) {
	s := newTestStore(t)
	// The wake scanner and re-evaluation mark their reads as internal.
	scan := sqlite.WithoutReadStamp(t.Context())
	_, err := s.InboxList(scan, "w")
	require.NoError(t, err)
	got, err := s.InboxLastRead(t.Context(), "w")
	require.NoError(t, err)
	require.Nil(t, got, "a wake scan is not the agent reading its inbox")
}

func TestInboxStatus_WaitSetsAndClearsParked_OnEveryExit(t *testing.T) {
	ctxCancelled, cancel := context.WithCancel(t.Context())
	tests := []struct {
		name string
		ctx  context.Context
		seed bool
		stop func()
	}{
		{"timeout", t.Context(), false, nil},
		{"event arrives", t.Context(), true, nil},
		{"caller cancels", ctxCancelled, false, cancel},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestStore(t)
			if tt.seed {
				seedAddressed(t, s, "w", 1)
				_, err := s.WriteDB().Exec(`DELETE FROM agent_inbox_ack`)
				require.NoError(t, err)
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				_, _ = s.InboxWait(tt.ctx, "w", 700*time.Millisecond)
			}()
			if !tt.seed {
				require.Eventually(t, func() bool {
					p, err := s.InboxParked(t.Context(), "w")
					return err == nil && p.Parked && len(p.PIDs) == 1 && p.Since != nil
				}, 2*time.Second, 10*time.Millisecond, "parked while the wait lives")
			}
			if tt.stop != nil {
				tt.stop()
			}
			<-done
			p, err := s.InboxParked(t.Context(), "w")
			require.NoError(t, err)
			require.False(t, p.Parked, "cleared when the wait ends")
			var n int
			require.NoError(t, s.WriteDB().QueryRow(`SELECT COUNT(*) FROM agent_inbox_waiters`).Scan(&n))
			require.Zero(t, n)
		})
	}
}

func TestInboxStatus_CrashedWaiter_NotPermanentlyParked(t *testing.T) {
	self, err := procinfo.StartTime(os.Getppid())
	require.NoError(t, err)
	tests := []struct {
		name  string
		pid   int
		start string
		want  bool
	}{
		{"live process, matching start time", os.Getppid(), self, true},
		{"live process, reused pid (other start time)", os.Getppid(), self + "-reused", false},
		{"process gone", 2147483646, "x", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestStore(t)
			_, err := s.WriteDB().Exec(`INSERT INTO agent_inbox_waiters(agent_id,pid,pid_start_time,token,since) VALUES('w',?,?,1,?)`,
				tt.pid, tt.start, "2026-11-11T09:00:00.000000000Z")
			require.NoError(t, err)
			p, err := s.InboxParked(t.Context(), "w")
			require.NoError(t, err)
			require.Equal(t, tt.want, p.Parked)
			// The next wait start prunes dead rows without any timer.
			_, err = s.InboxWait(t.Context(), "w", 50*time.Millisecond)
			require.NoError(t, err)
			var n int
			require.NoError(t, s.WriteDB().QueryRow(`SELECT COUNT(*) FROM agent_inbox_waiters WHERE agent_id='w'`).Scan(&n))
			if tt.want {
				require.Equal(t, 1, n, "a live row of another wait is kept")
			} else {
				require.Zero(t, n)
			}
		})
	}
}

func TestInboxDelivery_LocalOnly_ExportAndJournalCarryNone(t *testing.T) {
	s := newTestStore(t)
	seqs := seedAddressed(t, s, "secret-agent-name", 2)
	require.NoError(t, s.InboxAck(t.Context(), "secret-agent-name", seqs[0]))
	require.NoError(t, s.InboxAckThrough(t.Context(), "secret-agent-name", seqs[1]))
	_, err := s.InboxWait(t.Context(), "secret-agent-name", 30*time.Millisecond)
	require.NoError(t, err)
	var before int
	require.NoError(t, s.WriteDB().QueryRow(`SELECT COUNT(*) FROM sync_events`).Scan(&before))
	_, err = s.InboxList(t.Context(), "another-agent")
	require.NoError(t, err)
	var after int
	require.NoError(t, s.WriteDB().QueryRow(`SELECT COUNT(*) FROM sync_events`).Scan(&after))
	require.Equal(t, before, after, "stamping a read journals nothing")
	for _, tbl := range []string{"agent_inbox_reads", "agent_inbox_cursor_marks", "agent_inbox_waiters"} {
		var n int
		require.NoError(t, s.WriteDB().QueryRow(`SELECT COUNT(*) FROM sync_events WHERE payload LIKE ?`, "%"+tbl+"%").Scan(&n))
		require.Zero(t, n)
	}
	exp, err := s.Export(t.Context(), "PROJ", "test")
	require.NoError(t, err)
	raw, err := json.Marshal(exp)
	require.NoError(t, err)
	for _, needle := range []string{"agent_inbox_reads", "agent_inbox_cursor_marks", "agent_inbox_waiters", "last_read_at", "advanced_at", "pid_start_time"} {
		require.NotContains(t, string(raw), needle)
	}
}

func TestSchema_DeliveryTables_AdditiveOnReleasedStoreUpgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "up.db")
	open := func() *sqlite.Store {
		st, err := sqlite.New(path, slog.New(slog.NewTextHandler(io.Discard, nil)))
		require.NoError(t, err)
		return st
	}
	s := open()
	version := func(st *sqlite.Store) string {
		var v string
		require.NoError(t, st.WriteDB().QueryRow(`SELECT value FROM meta WHERE key='schema_version'`).Scan(&v))
		return v
	}
	want := version(s)
	// A released (0.5.4) store has none of the three tables.
	for _, tbl := range []string{"agent_inbox_reads", "agent_inbox_cursor_marks", "agent_inbox_waiters"} {
		_, err := s.WriteDB().Exec(`DROP TABLE ` + tbl) // test-only constant identifiers
		require.NoError(t, err)
	}
	require.NoError(t, s.Close())
	s = open()
	t.Cleanup(func() { _ = s.Close() })
	require.Equal(t, want, version(s), "no schema version bump")
	for _, tbl := range []string{"agent_inbox_reads", "agent_inbox_cursor_marks", "agent_inbox_waiters"} {
		var name string
		require.NoError(t, s.WriteDB().QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, tbl).Scan(&name))
		require.True(t, strings.HasPrefix(name, "agent_inbox_"))
	}
}

func TestInboxAckInfo_TwoWatermarkAcks_EachEventTakesTheFirstCoveringMark(t *testing.T) {
	s := newTestStore(t)
	t1 := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	clock, cur := ticking(t1)
	s.SetClock(clock)
	seqs := seedAddressed(t, s, "w", 3)
	require.NoError(t, s.InboxAckThrough(t.Context(), "w", seqs[0]))
	t2 := t1.Add(time.Hour)
	*cur = t2
	require.NoError(t, s.InboxAckThrough(t.Context(), "w", seqs[2]))
	require.NoError(t, s.InboxAckThrough(t.Context(), "w", seqs[0]), "a lower seq never rewinds or adds a mark")
	for i, want := range []time.Time{t1, t2, t2} {
		info, err := s.InboxAckInfo(t.Context(), "w", seqs[i])
		require.NoError(t, err)
		require.Equal(t, sqlite.AckWatermark, info.Kind)
		require.True(t, info.TimeKnown)
		require.WithinDuration(t, want, *info.AckedAt, time.Nanosecond, "event %d", i)
	}
}

// A throttled list must not open a writer transaction: with the only writer
// connection held busy it still returns at once (MTIX-112.3).
func TestInboxLastRead_ThrottledList_TakesNoWriterTx(t *testing.T) {
	s := newTestStore(t)
	t0 := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	clock, _ := ticking(t0)
	s.SetClock(clock)
	seedAddressed(t, s, "w", 1) // stamps t0; the clock has not moved, so the next stamp is throttled

	conn, err := s.WriteDB().Conn(t.Context())
	require.NoError(t, err)
	defer func() { require.NoError(t, conn.Close()) }()

	done := make(chan error, 1)
	go func() {
		_, listErr := s.InboxList(t.Context(), "w")
		done <- listErr
	}()
	select {
	case listErr := <-done:
		require.NoError(t, listErr)
	case <-time.After(2 * time.Second):
		t.Fatal("a throttled list waited for the writer connection")
	}
}
