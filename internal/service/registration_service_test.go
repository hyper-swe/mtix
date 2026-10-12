// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/procinfo"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

type fakeLiveness map[int]service.LiveState

func (f fakeLiveness) Probe(_ context.Context, r sqlite.Registration) service.LiveState {
	if st, ok := f[int(r.PID)]; ok {
		return st
	}
	return service.LiveUnknown
}

type memTakeOverLog struct{ recs []service.TakeOverRecord }

func (m *memTakeOverLog) Recent(_ context.Context, agent string, since time.Time) ([]service.TakeOverRecord, error) {
	var out []service.TakeOverRecord
	for _, r := range m.recs {
		if r.AgentID == agent && !r.At.Before(since) {
			out = append(out, r)
		}
	}
	return out, nil
}

func (m *memTakeOverLog) Append(_ context.Context, r service.TakeOverRecord) error {
	m.recs = append(m.recs, r)
	return nil
}

type countingNotice struct {
	sent []string
	err  error
}

func (c *countingNotice) SendTakeOverNotice(_ context.Context, agent string) error {
	if c.err != nil {
		return c.err
	}
	c.sent = append(c.sent, agent)
	return nil
}

type regFixture struct {
	svc    *service.RegistrationService
	store  *sqlite.Store
	live   fakeLiveness
	log    *memTakeOverLog
	notice *countingNotice
	now    *time.Time
}

func newRegFixture(t *testing.T) *regFixture {
	t.Helper()
	s, err := sqlite.New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	now := time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)
	f := &regFixture{store: s, live: fakeLiveness{}, log: &memTakeOverLog{}, notice: &countingNotice{}, now: &now}
	f.svc = service.NewRegistrationService(service.RegistrationDeps{
		Store: s, Liveness: f.live, TakeOvers: f.log, Notices: f.notice, Logger: slog.Default(),
		Clock: func() time.Time { return *f.now },
		Host:  service.RegistrationHost{HostID: "host-a", UID: "501", Version: "test", Protocol: 1},
	})
	return f
}

func req(agent string, pid int) service.RegisterSessionRequest {
	return service.RegisterSessionRequest{AgentID: agent, Harness: "codex", SessionHandle: fmt.Sprintf("h%d", pid),
		Origin: "session-env", PID: pid, PIDStartTime: "t1"}
}

func addEvents(t *testing.T, s *sqlite.Store, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		_, err := s.WriteDB().Exec(`INSERT INTO sync_events (event_id,project_prefix,node_id,op_type,payload,wall_clock_ts,lamport_clock,vector_clock,author_id,author_machine_hash,sync_status,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
			fmt.Sprintf("ev-%d", i), "T", "T-1", "comment", `{"to":"w","text":"x"}`, 1, i+1, "{}", "a", "0123456789abcdef", "pending", "2026-10-12T00:00:00Z")
		require.NoError(t, err)
	}
}

func TestRegisterSession_DeadExistingRow_ReplacedSilently(t *testing.T) {
	f := newRegFixture(t)
	ctx := context.Background()
	_, err := f.svc.RegisterSession(ctx, req("w", 100))
	require.NoError(t, err)
	f.live[100] = service.LiveDead

	res, err := f.svc.RegisterSession(ctx, req("w", 200))
	require.NoError(t, err)
	assert.Equal(t, service.OutcomeReplacedDead, res.Outcome)
	assert.EqualValues(t, 200, res.Registration.PID)
	assert.EqualValues(t, 2, res.Registration.Epoch, "a replacement is a new epoch")
	assert.Empty(t, f.notice.sent, "no notice for a dead row")
	assert.Empty(t, f.log.recs, "no take-over count for a dead row")
}

func TestRegisterSession_LiveRowWithoutTakeOver_Refused(t *testing.T) {
	for name, state := range map[string]service.LiveState{"alive": service.LiveAlive, "unknown fails safe": service.LiveUnknown} {
		t.Run(name, func(t *testing.T) {
			f := newRegFixture(t)
			ctx := context.Background()
			_, err := f.svc.RegisterSession(ctx, req("w", 100))
			require.NoError(t, err)
			f.live[100] = state

			_, err = f.svc.RegisterSession(ctx, req("w", 200))
			require.ErrorIs(t, err, model.ErrAgentStillActive)
			assert.Contains(t, err.Error(), "--take-over")
			rows, _ := f.store.ListRegistrations(ctx, "host-a", "501")
			assert.EqualValues(t, 100, rows[0].PID, "the live row is untouched")
		})
	}
}

func TestRegisterSession_SameSessionAgain_RefreshesWithoutTakeOver(t *testing.T) {
	f := newRegFixture(t)
	ctx := context.Background()
	_, err := f.svc.RegisterSession(ctx, req("w", 100))
	require.NoError(t, err)
	f.live[100] = service.LiveAlive
	*f.now = f.now.Add(time.Minute)

	res, err := f.svc.RegisterSession(ctx, req("w", 100))
	require.NoError(t, err)
	assert.Equal(t, service.OutcomeRefreshed, res.Outcome)
	assert.EqualValues(t, 1, res.Registration.Epoch)
	assert.Equal(t, f.now.Format(time.RFC3339), res.Registration.LastSeen)
}

func TestRegisterSession_TakeOver_BumpsEpochAuditsAndNotifiesOnce(t *testing.T) {
	f := newRegFixture(t)
	ctx := context.Background()
	_, err := f.svc.RegisterSession(ctx, req("w", 100))
	require.NoError(t, err)
	f.live[100] = service.LiveAlive

	r := req("w", 200)
	r.TakeOver = true
	res, err := f.svc.RegisterSession(ctx, r)
	require.NoError(t, err)
	assert.Equal(t, service.OutcomeTakenOver, res.Outcome)
	assert.EqualValues(t, 2, res.Registration.Epoch)
	assert.True(t, res.NoticeSent)
	assert.Equal(t, []string{"w"}, f.notice.sent)
	require.Len(t, f.log.recs, 1)
	assert.Equal(t, "h100", f.log.recs[0].DisplacedSession)
}

func TestRegisterSession_TakeOverNoticeRate_OnePerDisplacedSessionPerTenMinutes(t *testing.T) {
	f := newRegFixture(t)
	ctx := context.Background()
	f.log.recs = append(f.log.recs, service.TakeOverRecord{AgentID: "w", At: f.now.Add(-5 * time.Minute), DisplacedSession: "h100", NoticeAttempted: true})
	_, err := f.svc.RegisterSession(ctx, req("w", 100))
	require.NoError(t, err)
	f.live[100] = service.LiveAlive
	r := req("w", 200)
	r.TakeOver = true
	res, err := f.svc.RegisterSession(ctx, r)
	require.NoError(t, err)
	assert.False(t, res.NoticeSent, "the same displaced session was noticed 5 minutes ago")
	assert.Empty(t, f.notice.sent)

	f.log.recs[0].At = f.now.Add(-11 * time.Minute)
	f.live[200] = service.LiveAlive
	r2 := req("w", 100)
	r2.SessionHandle = "h100"
	r2.TakeOver = true
	r2.PID = 300
	res, err = f.svc.RegisterSession(ctx, r2)
	require.NoError(t, err)
	assert.True(t, res.NoticeSent, "a different displaced session is noticed independently")
}

func TestRegisterSession_NoticeUndeliverable_TakeOverStillSucceeds(t *testing.T) {
	f := newRegFixture(t)
	ctx := context.Background()
	_, err := f.svc.RegisterSession(ctx, req("w", 100))
	require.NoError(t, err)
	f.live[100] = service.LiveAlive
	f.notice.err = service.ErrNoNoticeRoute
	r := req("w", 200)
	r.TakeOver = true
	res, err := f.svc.RegisterSession(ctx, r)
	require.NoError(t, err)
	assert.False(t, res.NoticeSent)
}

func TestRegisterSession_TakeOverRateLimit_RequiresTTYAfterThree(t *testing.T) {
	f := newRegFixture(t)
	ctx := context.Background()
	_, err := f.svc.RegisterSession(ctx, req("w", 1))
	require.NoError(t, err)
	for pid := 2; pid <= 4; pid++ {
		f.live[pid-1] = service.LiveAlive
		r := req("w", pid)
		r.TakeOver = true
		_, err = f.svc.RegisterSession(ctx, r)
		require.NoError(t, err, "take-over %d", pid-1)
	}
	f.live[4] = service.LiveAlive
	r := req("w", 5)
	r.TakeOver = true
	_, err = f.svc.RegisterSession(ctx, r)
	require.ErrorIs(t, err, service.ErrTakeOverTTYRequired)

	r.HasTTY = true
	res, err := f.svc.RegisterSession(ctx, r)
	require.NoError(t, err)
	assert.Equal(t, service.OutcomeTakenOver, res.Outcome)

	*f.now = f.now.Add(61 * time.Minute)
	f.live[5] = service.LiveAlive
	r6 := req("w", 6)
	r6.TakeOver = true
	_, err = f.svc.RegisterSession(ctx, r6)
	require.NoError(t, err, "the window is one hour")
}

func TestRegisterSession_InitialHighWaterIsJournalMax(t *testing.T) {
	f := newRegFixture(t)
	addEvents(t, f.store, 50)
	res, err := f.svc.RegisterSession(context.Background(), req("w", 100))
	require.NoError(t, err)
	assert.EqualValues(t, 50, res.Registration.HighWater)

	addEvents2 := func() {
		_, e := f.store.WriteDB().Exec(`INSERT INTO sync_events (event_id,project_prefix,node_id,op_type,payload,wall_clock_ts,lamport_clock,vector_clock,author_id,author_machine_hash,sync_status,created_at) VALUES ('ev-new','T','T-1','comment','{"to":"w","text":"x"}',1,99,'{}','a','0123456789abcdef','pending','2026-10-12T00:00:00Z')`)
		require.NoError(t, e)
	}
	addEvents2()
	rows, err := f.store.ListRegistrations(context.Background(), "host-a", "501")
	require.NoError(t, err)
	var tail int64
	require.NoError(t, f.store.WriteDB().QueryRow(`SELECT MAX(rowid) FROM sync_events`).Scan(&tail))
	assert.Greater(t, tail, rows[0].HighWater, "the next new event is above the high-water mark: exactly one wake candidate")

	*f.now = f.now.Add(time.Minute)
	f.live[100] = service.LiveAlive
	res, err = f.svc.RegisterSession(context.Background(), req("w", 100))
	require.NoError(t, err)
	assert.EqualValues(t, 50, res.Registration.HighWater, "a refresh never advances the high-water mark")
}

func TestRegisterSession_MCPOrigin_NoLiveRoute(t *testing.T) {
	f := newRegFixture(t)
	ctx := context.Background()
	r := req("w", 100)
	r.Origin = "mcp"
	res, err := f.svc.RegisterSession(ctx, r)
	require.NoError(t, err)
	assert.Empty(t, res.Registration.SessionHandle, "no handle is stored for an mcp origin")
	assert.Zero(t, res.Registration.PID)

	views, err := f.svc.ListRegistrations(ctx)
	require.NoError(t, err)
	require.Len(t, views, 1)
	assert.False(t, views[0].LiveRoute)

	// A shell session replaces an mcp-only row with no take-over (nothing live to displace).
	res, err = f.svc.RegisterSession(ctx, req("w", 200))
	require.NoError(t, err)
	assert.Equal(t, service.OutcomeReplacedDead, res.Outcome)
}

func TestRegisterSession_InvalidInput_Rejected(t *testing.T) {
	f := newRegFixture(t)
	tests := map[string]func(*service.RegisterSessionRequest){
		"empty agent":       func(r *service.RegisterSessionRequest) { r.AgentID = "" },
		"bad origin":        func(r *service.RegisterSessionRequest) { r.Origin = "other" },
		"bad harness":       func(r *service.RegisterSessionRequest) { r.Harness = "Bad Harness" },
		"control in handle": func(r *service.RegisterSessionRequest) { r.SessionHandle = "a\nb" },
		"huge handle":       func(r *service.RegisterSessionRequest) { r.SessionHandle = string(make([]byte, 300)) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			r := req("w", 1)
			mutate(&r)
			_, err := f.svc.RegisterSession(context.Background(), r)
			require.ErrorIs(t, err, model.ErrInvalidInput)
		})
	}
}

func TestRegistrations_OtherUID_Ignored(t *testing.T) {
	f := newRegFixture(t)
	ctx := context.Background()
	_, err := f.store.RegisterSessionAtomic(ctx, sqlite.RegistrationKey{AgentID: "w", HostID: "host-a", UID: "999"},
		func(*sqlite.Registration, int64) (*sqlite.Registration, error) {
			return &sqlite.Registration{AgentID: "w", HostID: "host-a", UID: "999", Harness: "codex", Origin: "manual", SessionHandle: "x", PID: 7,
				RegisteredAt: "r", LastSeen: "2000-01-01T00:00:00Z", RegistrarVersion: "t", AttachProtocol: 1}, nil
		})
	require.NoError(t, err)

	views, err := f.svc.ListRegistrations(ctx)
	require.NoError(t, err)
	assert.Empty(t, views)
	res, err := f.svc.RegisterSession(ctx, req("w", 100))
	require.NoError(t, err, "another uid's live row is not ours to refuse")
	assert.Equal(t, service.OutcomeRegistered, res.Outcome)
	expired, err := f.svc.ExpireStale(ctx)
	require.NoError(t, err)
	assert.Empty(t, expired)
	var n int
	require.NoError(t, f.store.WriteDB().QueryRow(`SELECT COUNT(*) FROM agent_registrations WHERE uid='999'`).Scan(&n))
	assert.Equal(t, 1, n)
}

func TestUnregister_Rules(t *testing.T) {
	f := newRegFixture(t)
	ctx := context.Background()
	require.ErrorIs(t, f.svc.Unregister(ctx, "w", service.SessionIdentity{}, false), model.ErrNotFound)
	_, err := f.svc.RegisterSession(ctx, req("w", 100))
	require.NoError(t, err)
	f.live[100] = service.LiveAlive

	require.ErrorIs(t, f.svc.Unregister(ctx, "w", service.SessionIdentity{Handle: "other"}, false), model.ErrAgentStillActive)
	require.NoError(t, f.svc.Unregister(ctx, "w", service.SessionIdentity{Handle: "h100"}, false), "the owning session may unregister")

	_, err = f.svc.RegisterSession(ctx, req("w", 101))
	require.NoError(t, err)
	f.live[101] = service.LiveDead
	require.NoError(t, f.svc.Unregister(ctx, "w", service.SessionIdentity{}, false), "a dead row may be unregistered by anyone")

	_, err = f.svc.RegisterSession(ctx, req("w", 102))
	require.NoError(t, err)
	f.live[102] = service.LiveAlive
	require.NoError(t, f.svc.Unregister(ctx, "w", service.SessionIdentity{}, true), "--force by the operator")
}

func TestExpireStale_DeadAndOld_DeletedUnknownKept(t *testing.T) {
	f := newRegFixture(t)
	ctx := context.Background()
	for pid, agent := range map[int]string{1: "dead-old", 2: "dead-new", 3: "unknown-old", 4: "alive-old"} {
		_, err := f.svc.RegisterSession(ctx, req(agent, pid))
		require.NoError(t, err)
	}
	f.live[1], f.live[2], f.live[3], f.live[4] = service.LiveDead, service.LiveDead, service.LiveUnknown, service.LiveAlive
	_, err := f.store.WriteDB().Exec(`UPDATE agent_registrations SET last_seen='2026-10-10T00:00:00Z' WHERE agent_id <> 'dead-new'`)
	require.NoError(t, err)

	expired, err := f.svc.ExpireStale(ctx)
	require.NoError(t, err)
	require.Len(t, expired, 1)
	assert.Equal(t, "dead-old", expired[0].AgentID)
	views, err := f.svc.ListRegistrations(ctx)
	require.NoError(t, err)
	assert.Len(t, views, 3)
}

func TestListRegistrations_ShowsLivenessAndHold(t *testing.T) {
	f := newRegFixture(t)
	ctx := context.Background()
	_, err := f.svc.RegisterSession(ctx, req("w", 100))
	require.NoError(t, err)
	_, err = f.store.WriteDB().Exec(`UPDATE agent_registrations SET pending_hold=1`)
	require.NoError(t, err)
	f.live[100] = service.LiveAlive
	views, err := f.svc.ListRegistrations(ctx)
	require.NoError(t, err)
	require.Len(t, views, 1)
	assert.Equal(t, "alive", views[0].Liveness)
	assert.True(t, views[0].Hold)
	assert.True(t, views[0].LiveRoute)
	f.live[100] = service.LiveDead
	views, _ = f.svc.ListRegistrations(ctx)
	assert.Equal(t, "dead", views[0].Liveness)
	assert.False(t, views[0].LiveRoute)
}

func TestProcessLiveness_Probe_ClassifiesByPidAndStartTime(t *testing.T) {
	tests := []struct {
		name  string
		reg   sqlite.Registration
		start string
		err   error
		want  service.LiveState
	}{
		{"no pid is unknown", sqlite.Registration{}, "", nil, service.LiveUnknown},
		{"no start time recorded is unknown", sqlite.Registration{PID: 5}, "", nil, service.LiveUnknown},
		{"same start is alive", sqlite.Registration{PID: 5, PIDStartTime: "t"}, "t", nil, service.LiveAlive},
		{"reused pid is dead", sqlite.Registration{PID: 5, PIDStartTime: "t"}, "u", nil, service.LiveDead},
		{"unreadable start time is unknown", sqlite.Registration{PID: 5, PIDStartTime: "t"}, "", errors.New("x"), service.LiveUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := service.NewProcessLiveness(func(int) (string, error) { return tt.start, tt.err })
			assert.Equal(t, tt.want, p.Probe(context.Background(), tt.reg))
		})
	}
	gone := service.NewProcessLiveness(func(int) (string, error) { return "", procinfo.ErrNoProcess })
	assert.Equal(t, service.LiveDead, gone.Probe(context.Background(), sqlite.Registration{PID: 5, PIDStartTime: "t"}))
	assert.Equal(t, "unknown", service.LiveUnknown.String())
}

func TestRegisterSession_SamePIDDifferentHandle_IsAnotherSession(t *testing.T) {
	f := newRegFixture(t)
	ctx := context.Background()
	first := req("w", 100)
	_, err := f.svc.RegisterSession(ctx, first)
	require.NoError(t, err)
	f.live[100] = service.LiveAlive
	second := first
	second.SessionHandle = "another-thread"
	_, err = f.svc.RegisterSession(ctx, second)
	require.ErrorIs(t, err, model.ErrAgentStillActive)

	noHandle := first
	noHandle.SessionHandle = ""
	res, err := f.svc.RegisterSession(ctx, noHandle)
	require.NoError(t, err, "a handle-less caller falls back to pid and start time")
	assert.Equal(t, service.OutcomeRefreshed, res.Outcome)
}

type failingTakeOverLog struct{ recentErr, appendErr error }

func (f failingTakeOverLog) Recent(context.Context, string, time.Time) ([]service.TakeOverRecord, error) {
	return nil, f.recentErr
}
func (f failingTakeOverLog) Append(context.Context, service.TakeOverRecord) error { return f.appendErr }

func TestRegisterSession_TakeOverRecordFailures_AbortTakeOver(t *testing.T) {
	for name, log := range map[string]failingTakeOverLog{
		"unreadable record": {recentErr: errors.New("read failed")},
		"unwritable audit":  {appendErr: errors.New("write failed")},
	} {
		t.Run(name, func(t *testing.T) {
			f := newRegFixture(t)
			f.svc.TakeOvers = log
			ctx := context.Background()
			_, err := f.svc.RegisterSession(ctx, req("w", 100))
			require.NoError(t, err)
			f.live[100] = service.LiveAlive
			r := req("w", 200)
			r.TakeOver = true
			_, err = f.svc.RegisterSession(ctx, r)
			require.Error(t, err)
			rows, _ := f.store.ListRegistrations(ctx, "host-a", "501")
			assert.EqualValues(t, 100, rows[0].PID, "a take-over without its audit entry does not happen")
		})
	}
}

func TestRegisterSession_PIDOnlyRowTakenOver_NoticeKeyedByPID(t *testing.T) {
	f := newRegFixture(t)
	ctx := context.Background()
	noHandle := service.RegisterSessionRequest{AgentID: "w", Origin: "manual", PID: 100, PIDStartTime: "t1"}
	_, err := f.svc.RegisterSession(ctx, noHandle)
	require.NoError(t, err)
	f.live[100] = service.LiveAlive
	r := req("w", 200)
	r.TakeOver = true
	_, err = f.svc.RegisterSession(ctx, r)
	require.NoError(t, err)
	require.Len(t, f.log.recs, 1)
	assert.Equal(t, "pid:100:t1", f.log.recs[0].DisplacedSession)
}

func TestRegisterSession_NoticeFailure_IsNotFatal(t *testing.T) {
	f := newRegFixture(t)
	ctx := context.Background()
	_, err := f.svc.RegisterSession(ctx, req("w", 100))
	require.NoError(t, err)
	f.live[100] = service.LiveAlive
	f.notice.err = errors.New("inbox write failed")
	r := req("w", 200)
	r.TakeOver = true
	res, err := f.svc.RegisterSession(ctx, r)
	require.NoError(t, err)
	assert.False(t, res.NoticeSent)
}

func TestNewRegistrationService_NilLogger_DefaultsAndHarnessDefaults(t *testing.T) {
	s, err := sqlite.New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	svc := service.NewRegistrationService(service.RegistrationDeps{Store: s, Liveness: fakeLiveness{}, Clock: time.Now,
		Host: service.RegistrationHost{HostID: "h", UID: "1"}})
	res, err := svc.RegisterSession(context.Background(), service.RegisterSessionRequest{AgentID: "w", Origin: "manual"})
	require.NoError(t, err)
	assert.Equal(t, "unknown", res.Registration.Harness)
}

func TestUnregisterAndExpire_StoreFailure_Wrapped(t *testing.T) {
	f := newRegFixture(t)
	require.NoError(t, f.store.Close())
	ctx := context.Background()
	assert.Error(t, f.svc.Unregister(ctx, "w", service.SessionIdentity{}, true))
	_, err := f.svc.ExpireStale(ctx)
	assert.Error(t, err)
	_, err = f.svc.ListRegistrations(ctx)
	assert.Error(t, err)
}

func TestRegisterSession_ResumedSessionNewProcess_RecordsNewPID(t *testing.T) {
	f := newRegFixture(t)
	ctx := context.Background()
	_, err := f.svc.RegisterSession(ctx, req("w", 100))
	require.NoError(t, err)
	f.live[100] = service.LiveDead

	resumed := req("w", 200) // same handle? req derives the handle from the pid, so pin it
	resumed.SessionHandle = "h100"
	resumed.PIDStartTime = "t2"
	res, err := f.svc.RegisterSession(ctx, resumed)
	require.NoError(t, err)
	assert.Equal(t, service.OutcomeRefreshed, res.Outcome)
	assert.EqualValues(t, 200, res.Registration.PID, "a refresh moves the row to the live process")
	assert.Equal(t, "t2", res.Registration.PIDStartTime)

	f.live[200] = service.LiveAlive
	_, err = f.svc.RegisterSession(ctx, req("w", 300))
	require.ErrorIs(t, err, model.ErrAgentStillActive, "the resumed lane is live and must not be replaced silently")
}

func TestRegisterSession_ReusedPIDWithOtherStartTime_IsNotMine(t *testing.T) {
	f := newRegFixture(t)
	ctx := context.Background()
	first := service.RegisterSessionRequest{AgentID: "w", Origin: "manual", PID: 100, PIDStartTime: "t1"}
	_, err := f.svc.RegisterSession(ctx, first)
	require.NoError(t, err)
	f.live[100] = service.LiveAlive
	reused := first
	reused.PIDStartTime = "t2"
	_, err = f.svc.RegisterSession(ctx, reused)
	require.ErrorIs(t, err, model.ErrAgentStillActive)
}

func TestRegisterSession_SameDisplacedSessionAfterTenMinutes_NoticedAgain(t *testing.T) {
	f := newRegFixture(t)
	ctx := context.Background()
	f.log.recs = append(f.log.recs, service.TakeOverRecord{AgentID: "w", At: f.now.Add(-11 * time.Minute), DisplacedSession: "h100", NoticeAttempted: true})
	_, err := f.svc.RegisterSession(ctx, req("w", 100))
	require.NoError(t, err)
	f.live[100] = service.LiveAlive
	r := req("w", 200)
	r.TakeOver = true
	res, err := f.svc.RegisterSession(ctx, r)
	require.NoError(t, err)
	assert.True(t, res.NoticeSent, "the 10-minute window has passed for the same displaced session")
}

func TestRegisterSession_NoticeSuppressed_AuditRecordsNoAttempt(t *testing.T) {
	f := newRegFixture(t)
	ctx := context.Background()
	f.log.recs = append(f.log.recs, service.TakeOverRecord{AgentID: "w", At: f.now.Add(-time.Minute), DisplacedSession: "h100", NoticeAttempted: true})
	_, err := f.svc.RegisterSession(ctx, req("w", 100))
	require.NoError(t, err)
	f.live[100] = service.LiveAlive
	r := req("w", 200)
	r.TakeOver = true
	_, err = f.svc.RegisterSession(ctx, r)
	require.NoError(t, err)
	assert.False(t, f.log.recs[len(f.log.recs)-1].NoticeAttempted)
}

func TestRegisterSession_RouteLessManualRow_ReplacedSilently(t *testing.T) {
	f := newRegFixture(t)
	ctx := context.Background()
	_, err := f.svc.RegisterSession(ctx, service.RegisterSessionRequest{AgentID: "w", Origin: "manual"})
	require.NoError(t, err)
	res, err := f.svc.RegisterSession(ctx, req("w", 200))
	require.NoError(t, err, "a row with neither handle nor pid holds nothing live")
	assert.Equal(t, service.OutcomeReplacedDead, res.Outcome)
}

func TestRegisterSession_ConcurrentSessions_OneWinsOthersRefused(t *testing.T) {
	f := newRegFixture(t)
	ctx := context.Background()
	const racers = 10
	for pid := 1; pid <= racers; pid++ {
		f.live[pid] = service.LiveAlive
	}
	var wg sync.WaitGroup
	results := make(chan error, racers)
	for pid := 1; pid <= racers; pid++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.svc.RegisterSession(ctx, req("w", pid))
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	ok, refused := 0, 0
	for err := range results {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, model.ErrAgentStillActive):
			refused++
		}
	}
	assert.Equal(t, 1, ok)
	assert.Equal(t, racers-1, refused)
}

// midProbeLiveness lets a test commit a fresh registration while a probe is in flight.
type midProbeLiveness struct {
	state service.LiveState
	hook  func()
}

func (m *midProbeLiveness) Probe(context.Context, sqlite.Registration) service.LiveState {
	if hook := m.hook; hook != nil {
		m.hook = nil // clear first: the hook registers a session, which probes again
		hook()
	}
	return m.state
}

func TestExpireStale_RegisterCommitsDuringProbe_FreshRowSurvives(t *testing.T) {
	f := newRegFixture(t)
	ctx := context.Background()
	_, err := f.svc.RegisterSession(ctx, req("w", 100))
	require.NoError(t, err)
	_, err = f.store.WriteDB().Exec(`UPDATE agent_registrations SET last_seen='2020-01-01T00:00:00Z'`)
	require.NoError(t, err)
	probe := &midProbeLiveness{state: service.LiveDead}
	probe.hook = func() {
		_, e := f.svc.RegisterSession(ctx, req("w", 200)) // a new session registers while doctor probes
		require.NoError(t, e)
	}
	f.svc.Liveness = probe

	expired, err := f.svc.ExpireStale(ctx)
	require.NoError(t, err)
	assert.Empty(t, expired, "the row changed after it was read, so it is not expired")
	rows, _ := f.store.ListRegistrations(ctx, "host-a", "501")
	require.Len(t, rows, 1)
	assert.EqualValues(t, 200, rows[0].PID)
}

func TestUnregister_RegisterCommitsDuringProbe_ReportsConflictAndKeepsRow(t *testing.T) {
	f := newRegFixture(t)
	ctx := context.Background()
	_, err := f.svc.RegisterSession(ctx, req("w", 100))
	require.NoError(t, err)
	probe := &midProbeLiveness{state: service.LiveDead}
	probe.hook = func() {
		_, e := f.svc.RegisterSession(ctx, req("w", 200))
		require.NoError(t, e)
	}
	f.svc.Liveness = probe
	err = f.svc.Unregister(ctx, "w", service.SessionIdentity{}, false)
	require.ErrorIs(t, err, model.ErrConflict)
	rows, _ := f.store.ListRegistrations(ctx, "host-a", "501")
	require.Len(t, rows, 1)
	assert.EqualValues(t, 200, rows[0].PID)
}

type failDeleteStore struct{ *sqlite.Store }

func (failDeleteStore) DeleteRegistrationIf(context.Context, sqlite.RegistrationKey, sqlite.RegistrationSnapshot) (int64, error) {
	return 0, errors.New("delete failed")
}

func TestUnregisterAndExpire_DeleteFailure_Propagates(t *testing.T) {
	f := newRegFixture(t)
	ctx := context.Background()
	_, err := f.svc.RegisterSession(ctx, req("a", 1))
	require.NoError(t, err)
	_, err = f.svc.RegisterSession(ctx, req("b", 2)) // a second agent: Unregister must skip rows that are not its target
	require.NoError(t, err)
	_, err = f.store.WriteDB().Exec(`UPDATE agent_registrations SET last_seen='2020-01-01T00:00:00Z'`)
	require.NoError(t, err)
	f.live[1], f.live[2] = service.LiveDead, service.LiveDead
	f.svc.Store = failDeleteStore{f.store}

	require.ErrorContains(t, f.svc.Unregister(ctx, "b", service.SessionIdentity{}, true), "delete failed")
	_, err = f.svc.ExpireStale(ctx)
	require.ErrorContains(t, err, "delete failed")
}

func TestRegisterSession_AgentIDTooLong_Rejected(t *testing.T) {
	f := newRegFixture(t)
	r := req(string(make([]byte, 70)), 1)
	r.AgentID = "x" + string(bytes.Repeat([]byte("y"), 70))
	_, err := f.svc.RegisterSession(context.Background(), r)
	require.ErrorIs(t, err, model.ErrInvalidInput)
}

func TestRegisterSession_UnknownStartTime_SamePIDRefreshesOtherPIDRefused(t *testing.T) {
	f := newRegFixture(t)
	ctx := context.Background()
	uncertain := service.RegisterSessionRequest{AgentID: "w", Origin: "manual", PID: 100}
	_, err := f.svc.RegisterSession(ctx, uncertain)
	require.NoError(t, err)

	res, err := f.svc.RegisterSession(ctx, uncertain)
	require.NoError(t, err, "the same caller re-registering is not another session")
	assert.Equal(t, service.OutcomeRefreshed, res.Outcome)

	other := uncertain
	other.PID = 200
	_, err = f.svc.RegisterSession(ctx, other)
	require.ErrorIs(t, err, model.ErrAgentStillActive, "unknown liveness is never replaced silently by another process")
	other.TakeOver = true
	_, err = f.svc.RegisterSession(ctx, other)
	require.NoError(t, err)
}
