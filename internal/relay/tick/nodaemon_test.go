// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// The FR-21 §6.6 no-daemon scenario. A peer that cannot host a daemon —
// a turn-driven agent seat, a cron-locked appliance, a courier laptop —
// is a first-class deployment, not a degraded one. It converges on its
// next tick, and the delivery rungs it lacks are ABSENT rather than
// broken: an inbox still fills, and nothing reports an error merely
// because no process was sitting there to be woken.

package tick_test

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/relay/keyring"
	"github.com/hyper-swe/mtix/internal/relay/tick"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

// joinRelay attaches a second store to an existing relay, sharing its
// key, and returns that peer's stack.
func joinRelay(t *testing.T, host *relayRig, peerID string) (*sqlite.Store, *tick.Stack) {
	t.Helper()
	st, err := sqlite.New(filepath.Join(t.TempDir(), "joiner.db"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	ring, err := keyring.Load(host.keysDir)
	require.NoError(t, err)
	epoch, key, err := ring.Current()
	require.NoError(t, err)
	keys := filepath.Join(t.TempDir(), "keys-"+peerID)
	require.NoError(t, keyring.Write(keys, epoch, key))

	stack, err := tick.Open(tick.StackConfig{
		Store: st, RelayDir: host.relayDir, KeysDir: keys,
		PeerID: peerID, Logger: slog.Default(),
	})
	require.NoError(t, err)
	return st, stack
}

// TestRelayTick_TickOnlyPeerConvergesOnNextTick is the scenario's core
// claim. A peer running no daemon and no timer — exactly one pass, when
// something invokes it — takes in everything published since its last
// pass and gets its own work out. Convergence is a property of the
// pass, not of a process staying resident.
func TestRelayTick_TickOnlyPeerConvergesOnNextTick(t *testing.T) {
	ctx := context.Background()
	host := newRelay(t, true)
	host.journal(t, 2)

	hostStack, err := tick.Open(host.config())
	require.NoError(t, err)
	_, err = hostStack.Pass(ctx)
	require.NoError(t, err)

	// The tick-only seat wakes up for the first time.
	seatStore, seat := joinRelay(t, host, peerB)
	res, err := seat.Pass(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, res.Ingest.Applied)

	node, err := seatStore.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	require.Equal(t, "PROJ-1", node.Title)

	// It goes away. Work happens elsewhere while nothing of it is
	// running.
	host.journal(t, 3)
	_, err = hostStack.Pass(ctx)
	require.NoError(t, err)

	// It comes back for exactly one more pass and is caught up.
	res, err = seat.Pass(ctx)
	require.NoError(t, err)
	require.Equal(t, 3, res.Ingest.Applied,
		"one pass takes in everything published since the last one")

	for _, id := range []string{"PROJ-1", "PROJ-2", "PROJ-3", "PROJ-4", "PROJ-5"} {
		_, err := seatStore.GetNode(ctx, id)
		require.NoError(t, err, "%s should have arrived", id)
	}
}

// TestRelayTick_InboxDeliveryNeedsNoDaemon is the rung that must work
// without a resident process. Relay-arrived events are journal rows, so
// the shipped ledger fires their hooks in the same pass — an addressed
// wake lands in the inbox of a seat that has no daemon at all, which is
// the whole delivery story for a courier or a turn-driven agent.
func TestRelayTick_InboxDeliveryNeedsNoDaemon(t *testing.T) {
	ctx := context.Background()
	host := newRelay(t, true)

	hooksDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(hooksDir, "hooks.yaml"), []byte(`
hooks:
  - name: wake-worker
    match:
      events: [status.changed]
      status-to: [done]
      to-agent: worker
    deliver: [inbox]
`), 0o600))

	seatStore, seat := joinRelay(t, host, peerB)

	// The seat holds some unrelated local history, so this is ordinary
	// traffic rather than a first attach — whose bootstrap floor
	// suppresses wakes by design.
	require.NoError(t, seatStore.CreateNode(ctx, &model.Node{
		ID: "PROJ-99", Project: "PROJ", Depth: 0, Seq: 99, Title: "seat's own work",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeStory, Creator: "t",
		ContentHash: model.ComputeContentHash("seat's own work", "", "", "", nil),
		CreatedAt:   fixedTime, UpdatedAt: fixedTime,
	}))

	// The host creates the work and drives it to done. Everything about
	// this node reaches the seat over the folder and nowhere else.
	host.journal(t, 1)
	require.NoError(t, host.store.TransitionStatus(ctx, "PROJ-1", model.StatusInProgress, "picked up", "t"))
	require.NoError(t, host.store.TransitionStatus(ctx, "PROJ-1", model.StatusDone, "finished", "t"))
	hostStack, err := tick.Open(host.config())
	require.NoError(t, err)
	_, err = hostStack.Pass(ctx)
	require.NoError(t, err)

	// One pass on the seat, then dispatch — which is what `relay tick`
	// does, with no daemon anywhere.
	res, err := seat.Pass(ctx)
	require.NoError(t, err)
	require.Equal(t, 3, res.Ingest.Applied)
	require.Zero(t, res.Ingest.Quarantined, "the host's own node arrives cleanly")
	service.NewHooksDispatcher(seatStore, hooksDir, slog.Default()).Dispatch(ctx)

	entries, err := seatStore.ReadHookLog(ctx, 100)
	require.NoError(t, err)
	delivered := 0
	for _, e := range entries {
		if e.Hook == "wake-worker" && e.Outcome == "delivered" {
			delivered++
		}
	}
	require.Positive(t, delivered,
		"an addressed wake must reach the inbox of a seat with no daemon")
}

// TestRelayTick_ExecWakeAbsenceIsNotAnError is the scenario's other
// half, and the one that decides whether a tick-only peer reads as
// healthy. Such a peer has no process to spawn into, so the exec rung
// is simply not present — and a pass must NOT report that as a failure.
//
// Getting this wrong would make every courier and every turn-driven
// seat look permanently broken in status and doctor, which is how a
// real problem gets lost among the noise.
func TestRelayTick_ExecWakeAbsenceIsNotAnError(t *testing.T) {
	ctx := context.Background()
	host := newRelay(t, true)
	host.journal(t, 2)

	hostStack, err := tick.Open(host.config())
	require.NoError(t, err)
	_, err = hostStack.Pass(ctx)
	require.NoError(t, err)

	seatStore, seat := joinRelay(t, host, peerB)

	// No hooks.yaml at all: nothing to exec into, nothing configured.
	res, err := seat.Pass(ctx)
	require.NoError(t, err, "a pass with no wake rungs configured is a clean pass")
	require.Equal(t, 2, res.Ingest.Applied)
	require.Empty(t, res.Ingest.Stalls, "an absent rung is not a stall")
	require.Zero(t, res.Ingest.Quarantined)
	require.Zero(t, res.Ingest.AuthFailures)

	// Dispatching with no configuration is equally quiet.
	require.NotPanics(t, func() {
		service.NewHooksDispatcher(seatStore, t.TempDir(), slog.Default()).Dispatch(ctx)
	})

	// And the events still arrived — absence of a wake rung costs
	// latency, never delivery.
	_, err = seatStore.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
}
