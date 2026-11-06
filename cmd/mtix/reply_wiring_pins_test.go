// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
)

// These tests pin the PRODUCTION wiring of the reply processor in the real
// daemon and the host dispatcher (MTIX-112.20.3). Each builds the real
// constructor on a real store with a real registration and drives the
// processor the constructor returned, so a nil processor, a wrong host id or a
// dropped Replies field fails here and not only in a customer's daemon.

// pulledReplyEvent journals an event the way a hub pull applies it (status
// applied), from a remote agent to the registered local agent, with a deadline.
func pulledReplyEvent(t *testing.T, to string) {
	t.Helper()
	payload := `{"text":"q","author_id":"remote-agent","to":"` + to + `","reply_by":"` + model.FormatReplyBy(utcNow().Add(2*time.Hour)) + `"}`
	_, err := app.store.WriteDB().ExecContext(t.Context(), `
		INSERT OR IGNORE INTO sync_events (event_id, project_prefix, node_id, op_type, payload, wall_clock_ts, lamport_clock,
		                         vector_clock, author_id, author_machine_hash, sync_status, created_at)
		VALUES ('ev-pulled-1', 'MTIX', 'MTIX-1', 'comment', ?, 1, 1, '{}', 'remote-agent', '0123456789abcdef', 'applied', ?)`,
		payload, utcNow().Format(time.RFC3339Nano))
	require.NoError(t, err)
}

func replyHolds(t *testing.T) []oplocal.ReplyHold {
	t.Helper()
	src, err := newHubContactSource(app.mtixDir)
	require.NoError(t, err)
	st, err := src.state.ReadReplyState(src.key())
	require.NoError(t, err)
	return st.Holds
}

func TestNewDaemonScheduler_WithHub_ReturnsProcessorBoundToThisHostAndStore(t *testing.T) {
	initRegistrationApp(t)
	require.NoError(t, runAgentRegister("local-agent", sessionOptions{}))
	var stderr bytes.Buffer
	sched, onContact, replies, err := newDaemonScheduler(io.Discard, &stderr, true, 5)
	require.NoError(t, err)
	require.NotNil(t, sched)
	require.NotNil(t, onContact)
	require.NotNil(t, replies, "without the processor no reply closes a window and no event holds")
	require.Same(t, app.store, replies.Journal)
	host, uid, err := registrationHostIdentity()
	require.NoError(t, err)
	require.Equal(t, service.RegistrationHost{HostID: host, UID: uid}, replies.Host, "the real host identity, so this host's registrations match")

	pulledReplyEvent(t, "local-agent")
	require.NoError(t, replies.Scan(t.Context()))
	require.Len(t, replyHolds(t), 1, "the daemon's own processor holds for an event pulled for a registered local agent")
}

func TestNewDaemonScheduler_NoHub_ReturnsNoProcessor(t *testing.T) {
	initRegistrationApp(t)
	sched, onContact, replies, err := newDaemonScheduler(io.Discard, io.Discard, false, 5)
	require.NoError(t, err)
	require.Nil(t, sched)
	require.Nil(t, onContact)
	require.Nil(t, replies)
}

func TestNewDaemonPlan_WithHub_CarriesProcessorThroughPassAndLoop(t *testing.T) {
	initRegistrationApp(t)
	require.NoError(t, runAgentRegister("local-agent", sessionOptions{}))
	pulls := 0
	plan, err := newDaemonPlan(t.Context(), daemonPlanSpec{stdout: io.Discard, stderr: io.Discard, hub: true, intervalSec: 5,
		pull: func() daemonPullOutcome {
			pulls++
			pulledReplyEvent(t, "local-agent")
			return daemonPullOutcome{attempted: true, ok: true}
		}})
	require.NoError(t, err)
	require.NotNil(t, plan.sched)
	require.NotNil(t, plan.replies, "the loop ticks plan.replies before every decision")

	plan.pass(true) // one contact pass: the pull fetches the event, the scan follows it
	require.Equal(t, 1, pulls)
	require.Len(t, replyHolds(t), 1, "the pass scans after the pull")

	// The loop's tick: an expired sender window is dropped even with no pull.
	require.NoError(t, service.OpenReplyWindow(t.Context(), mustSource(t).state, mustSource(t).key(), oplocal.ReplyWindow{
		AwaitingFrom: "peer", Sender: "me", Node: "N-1", OpenedAt: utcNow().Add(-2 * time.Hour), ReplyBy: utcNow().Add(-time.Hour)}))
	runScheduledPass(t.Context(), io.Discard, plan, daemonPassControl{})
	st, err := mustSource(t).state.ReadReplyState(mustSource(t).key())
	require.NoError(t, err)
	require.Empty(t, st.Windows, "runScheduledPass ticks plan.replies")
}

func mustSource(t *testing.T) *hubContactSource {
	t.Helper()
	src, err := newHubContactSource(app.mtixDir)
	require.NoError(t, err)
	return src
}

func TestHostBoardOptionsFor_SetsReplyProcessorOnTheBoardsStoreAndHost(t *testing.T) {
	initRegistrationApp(t)
	require.NoError(t, runAgentRegister("local-agent", sessionOptions{}))
	st, err := operatorState()
	require.NoError(t, err)
	host, uid, err := registrationHostIdentity()
	require.NoError(t, err)
	regHost := service.RegistrationHost{HostID: host, UID: uid}
	opts := hostBoardOptionsFor(hostBoardDeps{Store: app.store, State: st, Source: mustSource(t), Host: regHost}, app.mtixDir, "mtix")

	require.NotNil(t, opts.Replies, "a board without the processor never holds and keeps windows to the TTL")
	require.Same(t, app.store, opts.Replies.Journal)
	require.Equal(t, regHost, opts.Replies.Host)
	require.Equal(t, regHost, opts.Host)
	pulledReplyEvent(t, "local-agent")
	require.NoError(t, opts.Replies.Scan(t.Context()))
	require.Len(t, replyHolds(t), 1)
}
