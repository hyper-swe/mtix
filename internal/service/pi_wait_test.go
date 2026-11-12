// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/attach"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

const piSession = "01a13d60-79cf-7000-8000-aaaaaaaaaaaa"

// piWaitStore wraps the real store so a test can fail or lose a claim.
type piWaitStore struct {
	service.PiWaitStore
	mu        sync.Mutex
	claimErr  error
	loseClaim bool
	claims    int
}

func (s *piWaitStore) ClaimWake(ctx context.Context, k model.WakeKey, l time.Duration) (model.WakeAttempt, bool, error) {
	s.mu.Lock()
	s.claims++
	err, lose := s.claimErr, s.loseClaim
	s.mu.Unlock()
	if err != nil {
		return model.WakeAttempt{}, false, err
	}
	if lose {
		return model.WakeAttempt{}, false, nil
	}
	return s.PiWaitStore.ClaimWake(ctx, k, l)
}

func (s *piWaitStore) claimCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.claims
}

// piFixture registers the fixture agent as a pi session and builds the adapter.
type piFixture struct {
	*scanFixture
	pws *piWaitStore
	pi  *service.PiWait
}

func newPiFixture(t *testing.T) *piFixture { return newPiFixtureLease(t, time.Minute) }

// newPiFixtureLease sets the claim lease; a short one lets a missing terminal row show up as a reclaim.
func newPiFixtureLease(t *testing.T, lease time.Duration) *piFixture {
	t.Helper()
	f := newScanFixture(t)
	_, err := f.store.WriteDB().ExecContext(t.Context(),
		"UPDATE agent_registrations SET harness='pi', session_handle=? WHERE agent_id=?", piSession, f.key.AgentID)
	require.NoError(t, err)
	pf := &piFixture{scanFixture: f, pws: &piWaitStore{PiWaitStore: f.store}}
	pf.pi, err = service.NewPiWait(service.PiWaitOptions{
		Open: func(context.Context) (service.PiWaitStore, func() error, error) {
			return pf.pws, func() error { return nil }, nil
		},
		Host:  func() (string, string, error) { return f.key.HostID, f.key.UID, nil },
		Lease: lease,
		Wait: func(ctx context.Context) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Millisecond):
				return nil
			}
		},
	})
	require.NoError(t, err)
	return pf
}

func (p *piFixture) run(ctx context.Context, session string) (string, error) {
	var out bytes.Buffer
	err := p.pi.Run(ctx, attach.VerbRequest{Verb: attach.VerbWait, Harness: attach.HarnessPi, Protocol: 1,
		Session: session, Stdout: &out, Stderr: &bytes.Buffer{}})
	return out.String(), err
}

func (p *piFixture) outcome(seq int64) string {
	p.t.Helper()
	var o string
	require.NoError(p.t, p.store.ReadDB().QueryRowContext(p.t.Context(),
		"SELECT outcome FROM wake_ledger WHERE event_seq=?", seq).Scan(&o))
	return o
}

func TestAttachWaitPi_OneEvent_OneClaimThenExit0(t *testing.T) {
	p := newPiFixture(t)
	seq := p.addEvent("sender")

	type res struct {
		out string
		err error
	}
	results := make(chan res, 2)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for i := 0; i < 2; i++ {
		go func() {
			out, err := p.run(ctx, piSession)
			results <- res{out, err}
		}()
	}
	first := <-results // exactly one waiter wins
	require.NoError(t, first.err, "the winner exits 0 after its claim")
	assert.Equal(t, 1, strings.Count(first.out, "\n"), "one wake line")
	assert.Contains(t, first.out, `"seq":`)
	assert.NotContains(t, first.out, "sender", "event content and author never reach the nudge")
	assert.Equal(t, "sent", p.outcome(seq), "the claim is terminal before the line is printed")
	assert.Equal(t, 1, p.ledgerRows(), "one claim for one event")

	cancel() // the loser keeps waiting until cancelled and never exits 0
	second := <-results
	require.Error(t, second.err)
	assert.Empty(t, second.out, "a lost race prints no wake line")
}

func TestAttachWaitPi_UnackedEvent_NoRetrigger(t *testing.T) {
	p := newPiFixtureLease(t, 20*time.Millisecond) // shorter than the wait below: only a terminal row prevents a reclaim
	seq := p.addEvent("sender")
	_, err := p.run(t.Context(), piSession)
	require.NoError(t, err)
	require.Equal(t, "sent", p.outcome(seq))

	// A restarted wait child meets the same unacked event: no new claim row, no line.
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	out, err := p.run(ctx, piSession)
	require.Error(t, err)
	assert.Empty(t, out)
	assert.Equal(t, 1, p.ledgerRows())
}

// Each event wakes once, in order; a claimed event is never woken again while a newer one waits.
func TestAttachWaitPi_TwoEvents_EachWakesOnce(t *testing.T) {
	p := newPiFixtureLease(t, 20*time.Millisecond)
	first, second := p.addEvent("sender"), p.addEvent("sender")
	out, err := p.run(t.Context(), piSession)
	require.NoError(t, err)
	assert.Contains(t, out, `"seq":`+itoa(first))
	out, err = p.run(t.Context(), piSession)
	require.NoError(t, err)
	assert.Contains(t, out, `"seq":`+itoa(second))

	time.Sleep(50 * time.Millisecond) // past the lease: only the terminal rows keep both quiet
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	out, err = p.run(ctx, piSession)
	require.Error(t, err)
	assert.Empty(t, out)
	assert.Equal(t, 2, p.ledgerRows())
}

func TestAttachWaitPi_LostRaceOrClaimError_NoExit0NoLine(t *testing.T) {
	tests := []struct {
		name     string
		claimErr error
		lose     bool
	}{
		{"another claimer won", nil, true},
		{"claim fails", model.ErrConflict, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newPiFixture(t)
			p.addEvent("sender")
			p.pws.claimErr, p.pws.loseClaim = tt.claimErr, tt.lose
			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancel()
			out, err := p.run(ctx, piSession)
			require.Error(t, err)
			assert.Empty(t, out)
			assert.Zero(t, p.ledgerRows())
			if tt.claimErr != nil {
				require.ErrorIs(t, err, tt.claimErr)
				assert.Equal(t, 1, p.pws.claimCalls(), "an error stops the wait at once")
			}
		})
	}
}

func TestAttachWaitPi_HostileSession_RejectedBeforeAnyRead(t *testing.T) {
	p := newPiFixture(t)
	p.addEvent("sender")
	for _, bad := range []string{"", "a\nb", "a\x00b", strings.Repeat("a", attach.MaxSessionLen+1)} {
		out, err := p.run(t.Context(), bad)
		require.Error(t, err, "session %q", bad)
		assert.Empty(t, out)
	}
	assert.Zero(t, p.pws.claimCalls())
	assert.Zero(t, p.ledgerRows())
}

func TestAttachWaitPi_UnknownSession_Waits(t *testing.T) {
	p := newPiFixture(t)
	p.addEvent(p.key.AgentID) // authored by the recipient itself: never wakes it
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	out, err := p.run(ctx, "some-other-session")
	require.Error(t, err)
	assert.Empty(t, out, "a session with no registration waits")
	assert.Zero(t, p.pws.claimCalls())
}

func TestAttachWaitPi_OwnEvent_NeverWakesItsAuthor(t *testing.T) {
	p := newPiFixture(t)
	p.addEvent(p.key.AgentID)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	out, err := p.run(ctx, piSession)
	require.Error(t, err)
	assert.Empty(t, out)
	assert.Zero(t, p.pws.claimCalls())
}

func TestAttachWaitPi_OtherVerbs_AreNoOps(t *testing.T) {
	p := newPiFixture(t)
	for _, verb := range []string{attach.VerbSessionStart, attach.VerbPromptSubmit, attach.VerbSessionEnd} {
		require.NoError(t, p.pi.Run(t.Context(), attach.VerbRequest{Verb: verb, Harness: attach.HarnessPi, Stdout: &bytes.Buffer{}}))
	}
	assert.Zero(t, p.pws.claimCalls())
}

func TestNewPiWait_MissingCapability_ReturnsInvalidInput(t *testing.T) {
	_, err := service.NewPiWait(service.PiWaitOptions{})
	require.ErrorIs(t, err, model.ErrInvalidInput)
}

func TestAttachWaitPi_StoreFailures_AreReturned(t *testing.T) {
	boom := errors.New("boom")
	base := service.PiWaitOptions{Lease: time.Minute, Wait: func(context.Context) error { return nil },
		Host: func() (string, string, error) { return "h", "u", nil }}
	cases := map[string]service.PiWaitOptions{}
	o := base
	o.Open = func(context.Context) (service.PiWaitStore, func() error, error) { return nil, nil, boom }
	cases["open"] = o
	o = base
	o.Host = func() (string, string, error) { return "", "", boom }
	o.Open = func(context.Context) (service.PiWaitStore, func() error, error) { return nil, nil, nil }
	cases["host"] = o
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			pi, err := service.NewPiWait(opts)
			require.NoError(t, err)
			err = pi.Run(t.Context(), attach.VerbRequest{Verb: attach.VerbWait, Session: piSession, Stdout: &bytes.Buffer{}})
			require.ErrorIs(t, err, boom)
		})
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// MTIX-112.3: the pi wait scans its own registration like the host scanner; its
// polling is not the agent reading, so it stamps no last read and registers no waiter.
func TestAttachWaitPi_Claim_StampsNoLastReadAndRegistersNoWaiter(t *testing.T) {
	p := newPiFixture(t)
	p.addEvent("sender")
	// The fixture's own InboxList stamped a read while setting up; clear it so only the wait is measured.
	_, err := p.store.WriteDB().ExecContext(t.Context(), `DELETE FROM agent_inbox_reads WHERE agent_id=?`, p.key.AgentID)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, err = p.run(ctx, piSession)
	require.NoError(t, err)
	var reads, waiters int
	require.NoError(t, p.store.ReadDB().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM agent_inbox_reads WHERE agent_id=?`, p.key.AgentID).Scan(&reads))
	require.NoError(t, p.store.ReadDB().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM agent_inbox_waiters WHERE agent_id=?`, p.key.AgentID).Scan(&waiters))
	require.Zero(t, reads, "agent_inbox_reads")
	require.Zero(t, waiters, "agent_inbox_waiters")
}
