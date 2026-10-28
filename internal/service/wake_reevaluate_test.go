// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/testutil"
)

type fakeReevaluator struct {
	agents []string
	err    error
}

func (f *fakeReevaluator) Reevaluate(_ context.Context, agent string) error {
	f.agents = append(f.agents, agent)
	return f.err
}

func TestReevaluate_SettingChange_CalledAfterCommitOnly(t *testing.T) {
	state := testutil.OperatorState(t)
	fake := &fakeReevaluator{err: errors.New("dispatcher unavailable")}
	cs := wakeConfig(t, state, "worker", true).WithWakeReevaluator(fake)
	_, err := cs.Set("wake.wakes_per_hour", "5")
	require.NoError(t, err, "a re-evaluation error never fails the user's command")
	require.Equal(t, []string{"worker"}, fake.agents)
	value, err := cs.Get("wake.wakes_per_hour")
	require.NoError(t, err)
	require.Equal(t, "5", value, "the change stays committed")

	denied := wakeConfig(t, state, "worker", false).WithWakeReevaluator(fake)
	_, err = denied.Set("wake.wakes_per_hour", "9")
	require.Error(t, err)
	_, err = cs.Set("wake.max_hop_depth", "0")
	require.Error(t, err)
	require.Len(t, fake.agents, 1, "refused and rolled-back changes never re-evaluate")

	require.NoError(t, cs.Delete("wake.wakes_per_hour"))
	require.Equal(t, []string{"worker", "worker"}, fake.agents)
}

func TestReevaluate_HostWideSettingChange_NamesEveryAgent(t *testing.T) {
	state := testutil.OperatorState(t)
	fake := &fakeReevaluator{}
	_, err := wakeConfig(t, state, "", true).WithWakeReevaluator(fake).Set("wake.max_hop_depth", "40")
	require.NoError(t, err)
	require.Equal(t, []string{""}, fake.agents)
}

func TestReevaluate_InboxCalls_CalledOnSuccessOnly(t *testing.T) {
	prompts, nodes, store, _ := newTestPromptService(t)
	node, err := nodes.CreateNode(t.Context(), &service.CreateNodeRequest{Project: "TEST", Title: "inbox", Creator: "sender"})
	require.NoError(t, err)
	for range 2 {
		require.NoError(t, prompts.AddAnnotation(t.Context(), node.ID, "event", "sender", "worker"))
	}
	entries, err := store.InboxList(t.Context(), "worker")
	require.NoError(t, err)
	fake := &fakeReevaluator{err: errors.New("dispatcher unavailable")}
	svc := service.NewInboxService(store).WithWakeReevaluator(fake)
	require.NoError(t, svc.InboxAck(t.Context(), "worker", entries[0].Seq))
	require.NoError(t, svc.InboxAckThrough(t.Context(), "worker", entries[1].Seq))
	_, err = svc.InboxList(t.Context(), "worker")
	require.NoError(t, err)
	_, err = svc.InboxWait(t.Context(), "worker", time.Millisecond)
	require.NoError(t, err)
	require.Equal(t, []string{"worker", "worker", "worker", "worker"}, fake.agents)

	require.Error(t, svc.InboxAck(t.Context(), "worker", entries[1].Seq+100), "an ack beyond the journal tail fails")
	_, err = svc.InboxList(t.Context(), "")
	require.Error(t, err)
	require.Len(t, fake.agents, 4, "failed inbox calls never re-evaluate")
}

func TestReevaluate_GrantChange_CalledPerAgentAfterCommitOnly(t *testing.T) {
	f := newWakePermissionFixture(t)
	f.register(t, "first")
	f.register(t, "second")
	permissionRoot(t, f, "TEST-1")
	fake := &fakeReevaluator{err: errors.New("dispatcher unavailable")}
	f.svc.WithReevaluator(fake)
	changes := []oplocal.PermissionChange{{AgentID: "first", Action: "allow"}, {AgentID: "second", Action: "allow"}}
	require.NoError(t, f.svc.ApplyBatch(t.Context(), f.board, oplocal.PermissionBatch{Mode: "yes", Source: "mtix agent allow", Changes: changes}))
	require.Equal(t, []string{"first", "second"}, fake.agents)

	err := f.svc.ApplyBatch(t.Context(), f.board, oplocal.PermissionBatch{Mode: "bogus", Changes: changes[:1]})
	require.ErrorIs(t, err, model.ErrInvalidInput)
	require.Len(t, fake.agents, 2, "a refused batch never re-evaluates")
}

func TestReevaluate_NilReevaluator_IsNoOp(t *testing.T) {
	state := testutil.OperatorState(t)
	_, err := wakeConfig(t, state, "worker", true).Set("wake.wakes_per_hour", "5")
	require.NoError(t, err)
	store, _, _ := wakeServiceStore(t)
	_, err = service.NewInboxService(store).InboxList(t.Context(), "worker")
	require.NoError(t, err)
}

// TestReevaluate_LoosenedBudget_WakesHeldEventWithoutExplicitPass drives a real dispatcher:
// the setting change entry point alone lifts the hold and wakes the held event.
func TestReevaluate_LoosenedBudget_WakesHeldEventWithoutExplicitPass(t *testing.T) {
	prompts, nodes, store, _ := newTestPromptService(t)
	node, err := nodes.CreateNode(t.Context(), &service.CreateNodeRequest{Project: "TEST", Title: "held", Creator: "sender"})
	require.NoError(t, err)
	for range 2 {
		require.NoError(t, prompts.AddAnnotation(t.Context(), node.ID, "event", "sender", "worker"))
	}
	entries, err := store.InboxList(t.Context(), "worker")
	require.NoError(t, err)
	require.Len(t, entries, 2)
	state := testutil.OperatorState(t)
	_, err = wakeConfig(t, state, "", true).Set("wake.wakes_per_hour", "1")
	require.NoError(t, err)
	_, err = store.WriteDB().ExecContext(t.Context(), `INSERT INTO agent_registrations
		(agent_id,harness,uid,host_id,origin,epoch,high_water,pending_hold,registered_at,last_seen,registrar_version,attach_protocol)
		VALUES ('worker','claude','uid','host','manual',1,0,0,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','test',1)`)
	require.NoError(t, err)
	var sent []int64
	alerts := service.NewAlerts(operatorAlertsFixture(t), time.Now)
	d, err := service.NewWakeDispatcher(service.WakeDispatcherOptions{Store: store, Alerts: alerts, Settings: state, BudgetState: state, Clock: time.Now, Held: func(string) {},
		Send: func(_ context.Context, a model.WakeAttempt) error { sent = append(sent, a.Key.EventSeq); return nil }})
	require.NoError(t, err)
	keys := make([]model.WakeKey, len(entries))
	for i, entry := range entries {
		keys[i] = model.WakeKey{AgentID: "worker", HostID: "host", UID: "uid", EventSeq: entry.Seq, Epoch: 1}
	}
	require.NoError(t, d.Pass(t.Context(), keys))
	require.Equal(t, []int64{entries[0].Seq}, sent, "the budget of one holds the second event")

	_, err = wakeConfig(t, state, "", true).WithWakeReevaluator(d).Set("wake.wakes_per_hour", "5")
	require.NoError(t, err)
	require.Equal(t, []int64{entries[0].Seq, entries[1].Seq}, sent, "loosening alone wakes the held event")
	var hold int
	require.NoError(t, store.ReadDB().QueryRowContext(t.Context(), "SELECT pending_hold FROM agent_registrations WHERE agent_id='worker'").Scan(&hold))
	require.Zero(t, hold)
}
