// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// relaySpec describes one skipped create the far peer sent.
type relaySpec struct {
	peer       string
	epoch      uint64
	op         model.OpType
	nodeID     string
	title      string
	parent     string
	rs         uint64
	prompt     string
	acceptance string
	priority   model.Priority
	issueType  model.IssueType
}

func relayRowSpec(t *testing.T, st *sqlite.Store, sp relaySpec) string {
	t.Helper()
	if sp.nodeID == "" {
		sp.nodeID = "PROJ-1"
	}
	if sp.title == "" {
		sp.title = "Skipped task"
	}
	pl, err := model.EncodePayload(&model.CreateNodePayload{Title: sp.title, ParentID: sp.parent,
		NodeType: model.NodeTypeEpic, Description: "kept", Labels: []string{"a"}, Prompt: sp.prompt,
		Acceptance: sp.acceptance, Priority: sp.priority, IssueType: sp.issueType})
	require.NoError(t, err)
	if sp.peer == "" {
		sp.peer = "fedcba9876543210"
	}
	ev := model.SyncEvent{EventID: "e-" + sp.nodeID, ProjectPrefix: "PROJ", NodeID: sp.nodeID, OpType: sp.op, Payload: pl}
	raw, err := json.Marshal(ev)
	require.NoError(t, err)
	rec := sqlite.RelayQuarantineRecord{
		RelayQuarantineKey: sqlite.RelayQuarantineKey{PeerID: sp.peer, PubEpoch: epochOr1(sp.epoch), RS: sp.rs},
		NodeID:             sp.nodeID, EventID: ev.EventID, OpType: string(sp.op), Reason: "collision", Payload: raw, SeenAt: "2026-11-13T01:00:00Z",
	}
	require.NoError(t, st.RecordRelayQuarantine(context.Background(), rec))
	return service.QuarantineHandle(service.RelayQuarantineKeyString(rec.RelayQuarantineKey))
}

func relayRowFor(t *testing.T, st *sqlite.Store, op model.OpType, parent string, rs uint64) string {
	t.Helper()
	return relayRowSpec(t, st, relaySpec{op: op, parent: parent, rs: rs})
}

func newResolver(t *testing.T) (*service.RelayQuarantineResolver, *sqlite.Store, *service.NodeService) {
	t.Helper()
	nodes, st, _ := newTestNodeService(t)
	now := time.Date(2026, 11, 13, 3, 0, 0, 0, time.UTC)
	return service.NewRelayQuarantineResolver(st, nodes, func() time.Time { return now }), st, nodes
}

// TestRelayQuarantineResolver_RecreateNonCreate_IsRefusedWithTheExactMessage.
func TestRelayQuarantineResolver_RecreateNonCreate_IsRefusedWithTheExactMessage(t *testing.T) {
	res, st, _ := newResolver(t)
	h := relayRowFor(t, st, model.OpUpdateField, "", 4)
	_, err := res.Resolve(context.Background(), service.RelayResolveRequest{Handle: h, Mode: service.RelayResolveRecreate, Actor: "a", Reason: "r"})
	require.ErrorIs(t, err, model.ErrInvalidInput)
	require.Contains(t, err.Error(), `--recreate applies only to a skipped task creation, and this record is a "update_field" operation`)
	require.Contains(t, err.Error(), "settle it with --accept")
	rows, _ := st.ListRelayQuarantine(context.Background())
	require.Equal(t, sqlite.RelayQuarantineOpen, rows[0].State)
}

// TestRelayQuarantineResolver_RecreateThatFails_LeavesTheRowOpenAndCreatesNothing:
// the create and the mark are one transaction, so a failed create (missing
// parent) leaves the row open, and a failed mark leaves no task.
func TestRelayQuarantineResolver_RecreateThatFails_LeavesTheRowOpenAndCreatesNothing(t *testing.T) {
	res, st, _ := newResolver(t)
	ctx := context.Background()
	h := relayRowFor(t, st, model.OpCreateNode, "PROJ-99", 4)
	_, err := res.Resolve(ctx, service.RelayResolveRequest{Handle: h, Mode: service.RelayResolveRecreate, Actor: "a", Reason: "r"})
	require.Error(t, err)
	rows, _ := st.ListRelayQuarantine(ctx)
	require.Equal(t, sqlite.RelayQuarantineOpen, rows[0].State)
	_, getErr := st.GetNode(ctx, "PROJ-1")
	require.Error(t, getErr)
}

// TestRelayQuarantineResolver_MarkFailsAfterCreate_RollsTheTaskBack makes the
// in-transaction mark fail AFTER the create succeeded (a trigger aborts the row
// update; the open-row precheck has already passed). The create must roll back
// with it: no task, no sync event, row still open. Marking in a separate
// transaction, or ignoring the hook error, leaves the task behind (MTIX-268).
func TestRelayQuarantineResolver_MarkFailsAfterCreate_RollsTheTaskBack(t *testing.T) {
	res, st, _ := newResolver(t)
	ctx := context.Background()
	h := relayRowFor(t, st, model.OpCreateNode, "", 4)
	_, err := st.WriteDB().ExecContext(ctx, `CREATE TRIGGER relay_quarantine_test_abort BEFORE UPDATE ON relay_quarantine
		BEGIN SELECT RAISE(ABORT, 'mark refused'); END`)
	require.NoError(t, err)

	_, err = res.Resolve(ctx, service.RelayResolveRequest{Handle: h, Mode: service.RelayResolveRecreate, Actor: "a", Reason: "r"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "mark refused")
	_, getErr := st.GetNode(ctx, "PROJ-1")
	require.Error(t, getErr, "the create rolled back with the mark")
	var events int
	require.NoError(t, st.QueryRow(ctx, `SELECT COUNT(*) FROM sync_events`).Scan(&events))
	require.Zero(t, events, "no create event survives")
	rows, _ := st.ListRelayQuarantine(ctx)
	require.Equal(t, sqlite.RelayQuarantineOpen, rows[0].State)
}

// TestRelayQuarantineResolver_AlreadyResolved_IsRefusedBeforeAnyCreate keeps the
// friendly message for the ordinary double resolve.
func TestRelayQuarantineResolver_AlreadyResolved_IsRefusedBeforeAnyCreate(t *testing.T) {
	res, st, _ := newResolver(t)
	ctx := context.Background()
	h := relayRowFor(t, st, model.OpCreateNode, "", 4)
	_, err := res.Resolve(ctx, service.RelayResolveRequest{Handle: h, Mode: service.RelayResolveAccept, Actor: "a", Reason: "r"})
	require.NoError(t, err)
	_, err = res.Resolve(ctx, service.RelayResolveRequest{Handle: h, Mode: service.RelayResolveRecreate, Actor: "a", Reason: "r"})
	require.ErrorContains(t, err, "already resolved")
	_, getErr := st.GetNode(ctx, "PROJ-1")
	require.Error(t, getErr)
}

// TestRelayQuarantineResolver_Recreate_KeepsEveryFieldUnderANewIDAndExistingParent
// pins each preserved field, with the parent an existing local task.
func TestRelayQuarantineResolver_Recreate_KeepsEveryFieldUnderANewIDAndExistingParent(t *testing.T) {
	res, st, nodes := newResolver(t)
	ctx := context.Background()
	parent, err := nodes.CreateNode(ctx, &service.CreateNodeRequest{Project: "PROJ", Title: "Local parent", Creator: "a"})
	require.NoError(t, err)
	h := relayRowSpec(t, st, relaySpec{op: model.OpCreateNode, nodeID: parent.ID + ".1", parent: parent.ID, rs: 4,
		title: "Skipped child", prompt: "do it", acceptance: "it works", priority: model.PriorityCritical,
		issueType: model.IssueTypeBug})

	out, err := res.Resolve(ctx, service.RelayResolveRequest{Handle: h, Mode: service.RelayResolveRecreate, Actor: "a", Reason: " why "})
	require.NoError(t, err)
	require.Equal(t, "recreated", out.Resolution)
	node, err := st.GetNode(ctx, out.RecreatedAs)
	require.NoError(t, err)
	require.Equal(t, "Skipped child", node.Title)
	require.Equal(t, "kept", node.Description)
	require.Equal(t, "do it", node.Prompt)
	require.Equal(t, "it works", node.Acceptance)
	require.Equal(t, model.PriorityCritical, node.Priority)
	require.Equal(t, model.IssueTypeBug, node.IssueType)
	require.Equal(t, []string{"a"}, node.Labels)
	require.Equal(t, parent.ID, node.ParentID)
	require.Equal(t, 1, node.Depth)
	rows, _ := st.ListRelayQuarantine(ctx)
	require.Equal(t, out.RecreatedAs, rows[0].RecreatedAs)
	require.Equal(t, "why", rows[0].ResolveReason)
	require.Equal(t, "2026-11-13T03:00:00Z", rows[0].ResolvedAt)
}

// TestRelayQuarantineResolver_Recreate_ParentIsAnotherSkippedRecord: the
// payload's parent id is from the far peer's numbering. When that parent is
// itself a skipped create of the same peer, a recreated parent supplies its new
// id; an open or accepted one refuses with the parent's handle (MTIX-268).
func TestRelayQuarantineResolver_Recreate_ParentIsAnotherSkippedRecord(t *testing.T) {
	ctx := context.Background()
	req := func(h, mode string) service.RelayResolveRequest {
		return service.RelayResolveRequest{Handle: h, Mode: mode, Actor: "a", Reason: "r"}
	}
	setup := func(t *testing.T) (*service.RelayQuarantineResolver, *sqlite.Store, string, string) {
		res, st, _ := newResolver(t)
		parentH := relayRowSpec(t, st, relaySpec{op: model.OpCreateNode, nodeID: "PROJ-2", title: "Skipped parent", rs: 4})
		childH := relayRowSpec(t, st, relaySpec{op: model.OpCreateNode, nodeID: "PROJ-2.1", title: "Skipped child", parent: "PROJ-2", rs: 5})
		return res, st, parentH, childH
	}
	t.Run("parent recreated: the child follows it", func(t *testing.T) {
		res, st, parentH, childH := setup(t)
		p, err := res.Resolve(ctx, req(parentH, service.RelayResolveRecreate))
		require.NoError(t, err)
		c, err := res.Resolve(ctx, req(childH, service.RelayResolveRecreate))
		require.NoError(t, err)
		node, err := st.GetNode(ctx, c.RecreatedAs)
		require.NoError(t, err)
		require.Equal(t, p.RecreatedAs, node.ParentID, "under the recreated parent, not a local task that holds PROJ-2")
	})
	t.Run("parent still open: refused naming its handle", func(t *testing.T) {
		res, st, parentH, childH := setup(t)
		_, err := res.Resolve(ctx, req(childH, service.RelayResolveRecreate))
		require.ErrorIs(t, err, model.ErrInvalidInput)
		require.Contains(t, err.Error(), `parent "PROJ-2" was itself skipped`)
		require.Contains(t, err.Error(), "mtix sync quarantine resolve --handle "+parentH+" --recreate")
		rows, _ := st.ListRelayQuarantine(ctx)
		require.Equal(t, sqlite.RelayQuarantineOpen, rows[1].State)
		_, getErr := st.GetNode(ctx, "PROJ-1")
		require.Error(t, getErr, "nothing was created")
	})
	t.Run("parent accepted: refused, the child can only be accepted", func(t *testing.T) {
		res, _, parentH, childH := setup(t)
		_, err := res.Resolve(ctx, req(parentH, service.RelayResolveAccept))
		require.NoError(t, err)
		_, err = res.Resolve(ctx, req(childH, service.RelayResolveRecreate))
		require.ErrorIs(t, err, model.ErrInvalidInput)
		require.Contains(t, err.Error(), "was accepted and is not a task here")
		require.Contains(t, err.Error(), `settle this record with mtix sync quarantine resolve --handle `+childH+` --accept --reason "<why>"`)
	})
}

// TestRelayQuarantineResolver_BadRequests are refused before any write.
func TestRelayQuarantineResolver_BadRequests(t *testing.T) {
	res, st, _ := newResolver(t)
	h := relayRowFor(t, st, model.OpCreateNode, "", 4)
	long := make([]byte, 501)
	for i := range long {
		long[i] = 'x'
	}
	for name, req := range map[string]service.RelayResolveRequest{
		"no mode":     {Handle: h, Actor: "a", Reason: "r"},
		"blank":       {Handle: h, Mode: "accept", Actor: "a", Reason: " "},
		"too long":    {Handle: h, Mode: "accept", Actor: "a", Reason: string(long)},
		"no actor":    {Handle: h, Mode: "accept", Reason: "r"},
		"bad handle":  {Handle: "zz", Mode: "accept", Actor: "a", Reason: "r"},
		"no such row": {Handle: "00000000", Mode: "accept", Actor: "a", Reason: "r"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := res.Resolve(context.Background(), req)
			require.Error(t, err)
		})
	}
	rows, _ := st.ListRelayQuarantine(context.Background())
	require.Equal(t, sqlite.RelayQuarantineOpen, rows[0].State)
}

func epochOr1(e uint64) uint64 {
	if e == 0 {
		return 1
	}
	return e
}

// TestRelayQuarantineResolver_Recreate_ParentSkippedFromAnotherPeer_IsAmbiguous:
// the parent id is the author's numbering; if ANY other peer also had a create
// of that id skipped (open, accepted or recreated) the id is ambiguous, so
// recreate is refused naming that peer's handle (MTIX-268).
func TestRelayQuarantineResolver_Recreate_ParentSkippedFromAnotherPeer_IsAmbiguous(t *testing.T) {
	ctx := context.Background()
	for _, settle := range []string{"", service.RelayResolveAccept, service.RelayResolveRecreate} {
		t.Run("other peer's parent "+settle, func(t *testing.T) {
			res, st, _ := newResolver(t)
			otherH := relayRowSpec(t, st, relaySpec{peer: "0123456789abcdee", op: model.OpCreateNode, nodeID: "PROJ-2", rs: 3})
			childH := relayRowSpec(t, st, relaySpec{op: model.OpCreateNode, nodeID: "PROJ-2.1", parent: "PROJ-2", rs: 5})
			if settle != "" {
				_, err := res.Resolve(ctx, service.RelayResolveRequest{Handle: otherH, Mode: settle, Actor: "a", Reason: "r"})
				require.NoError(t, err)
			}
			_, err := res.Resolve(ctx, service.RelayResolveRequest{Handle: childH, Mode: service.RelayResolveRecreate, Actor: "a", Reason: "r"})
			require.ErrorIs(t, err, model.ErrInvalidInput)
			require.Contains(t, err.Error(), "ambiguous")
			require.Contains(t, err.Error(), "peer 0123456789abcdee")
			require.Contains(t, err.Error(), "handle "+otherH)
			require.Contains(t, err.Error(), `mtix sync quarantine resolve --handle `+childH+` --accept --reason "<why>"`)
			rows, _ := st.ListRelayQuarantine(ctx)
			require.Equal(t, sqlite.RelayQuarantineOpen, rows[len(rows)-1].State)
		})
	}
}

// TestRelayQuarantineResolver_Recreate_ParentLookup_IgnoresOtherIDsAndPicksTheNewest:
// rows of other node ids (any peer) are not the parent, and when the same peer
// has several skipped creates of the parent id the newest in (epoch, rs) wins.
func TestRelayQuarantineResolver_Recreate_ParentLookup_IgnoresOtherIDsAndPicksTheNewest(t *testing.T) {
	ctx := context.Background()
	req := func(h, mode string) service.RelayResolveRequest {
		return service.RelayResolveRequest{Handle: h, Mode: mode, Actor: "a", Reason: "r"}
	}
	t.Run("unrelated rows are not the parent", func(t *testing.T) {
		res, st, nodes := newResolver(t)
		local, err := nodes.CreateNode(ctx, &service.CreateNodeRequest{Project: "PROJ", Title: "Local", Creator: "a"})
		require.NoError(t, err)
		relayRowSpec(t, st, relaySpec{peer: "0123456789abcdee", op: model.OpCreateNode, nodeID: "PROJ-7", rs: 1})
		relayRowSpec(t, st, relaySpec{op: model.OpCreateNode, nodeID: "PROJ-8", rs: 2})
		relayRowSpec(t, st, relaySpec{op: model.OpUpdateField, nodeID: local.ID, rs: 3})
		childH := relayRowSpec(t, st, relaySpec{op: model.OpCreateNode, nodeID: local.ID + ".1", parent: local.ID, rs: 6})
		out, err := res.Resolve(ctx, req(childH, service.RelayResolveRecreate))
		require.NoError(t, err)
		node, _ := st.GetNode(ctx, out.RecreatedAs)
		require.Equal(t, local.ID, node.ParentID, "the payload parent is used as it is")
	})
	for name, sp := range map[string]struct{ olderEpoch, newerEpoch, olderRS, newerRS uint64 }{
		"newer rs":    {1, 1, 2, 3},
		"newer epoch": {1, 2, 9, 1},
	} {
		t.Run("newest record wins: "+name, func(t *testing.T) {
			res, st, _ := newResolver(t)
			olderH := relayRowSpec(t, st, relaySpec{op: model.OpCreateNode, nodeID: "PROJ-2", epoch: sp.olderEpoch, rs: sp.olderRS})
			newerH := relayRowSpec(t, st, relaySpec{op: model.OpCreateNode, nodeID: "PROJ-2", epoch: sp.newerEpoch, rs: sp.newerRS})
			childH := relayRowSpec(t, st, relaySpec{op: model.OpCreateNode, nodeID: "PROJ-2.1", parent: "PROJ-2", rs: 20})
			_, err := res.Resolve(ctx, req(olderH, service.RelayResolveAccept)) // older: accepted
			require.NoError(t, err)
			newer, err := res.Resolve(ctx, req(newerH, service.RelayResolveRecreate)) // newer: recreated
			require.NoError(t, err)
			out, err := res.Resolve(ctx, req(childH, service.RelayResolveRecreate))
			require.NoError(t, err, "the newest record of the parent id decides, not the older accepted one")
			node, _ := st.GetNode(ctx, out.RecreatedAs)
			require.Equal(t, newer.RecreatedAs, node.ParentID)
		})
	}
}

// TestRelayQuarantineResolver_Recreate_RepeatOfARecreatedEvent_IsRefused: a
// republish (reset-peer) can skip the same event again at a new position. A
// second recreate would add a third copy, so it is refused naming the first
// recreate; accept stays available (MTIX-268).
func TestRelayQuarantineResolver_Recreate_RepeatOfARecreatedEvent_IsRefused(t *testing.T) {
	res, st, _ := newResolver(t)
	ctx := context.Background()
	first := relayRowSpec(t, st, relaySpec{op: model.OpCreateNode, nodeID: "PROJ-1", rs: 4})
	again := relayRowSpec(t, st, relaySpec{op: model.OpCreateNode, nodeID: "PROJ-1", epoch: 2, rs: 1})
	out, err := res.Resolve(ctx, service.RelayResolveRequest{Handle: first, Mode: service.RelayResolveRecreate, Actor: "a", Reason: "r"})
	require.NoError(t, err)

	_, err = res.Resolve(ctx, service.RelayResolveRequest{Handle: again, Mode: service.RelayResolveRecreate, Actor: "a", Reason: "r"})
	require.ErrorIs(t, err, model.ErrInvalidInput)
	require.Contains(t, err.Error(), "already recreated as "+out.RecreatedAs)
	require.Contains(t, err.Error(), `settle this one with mtix sync quarantine resolve --handle `+again+` --accept --reason "<why>"`)
	require.Contains(t, err.Error(), `event "e-PROJ-1"`, "the event id is quoted for the terminal")
	n, _ := st.GetNode(ctx, "PROJ-2")
	require.Nil(t, n, "no third copy")

	_, err = res.Resolve(ctx, service.RelayResolveRequest{Handle: again, Mode: service.RelayResolveAccept, Actor: "a", Reason: "r"})
	require.NoError(t, err, "accept is still allowed")
}
