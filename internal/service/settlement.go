// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// Background settlement of provisional nodes (ADR-003 §4 / MTIX-30.3).
//
// A node created while a hub is unreachable stays PROVISIONAL — its display_path
// carries a uid segment and is collision-free by construction (ADR-003 §4). The
// SettlementService is the background driver that, on each sync and on shutdown,
// drains the store's pending-settlement worklist and asks the hub registry (via a
// Settler) to confirm a clean number for each node, renumbering it on confirm
// and leaving it provisional when the hub is unreachable.
//
// This is the "eager background settlement" half of MTIX-30.3: the create call
// itself never blocks on the network (NodeService triggers settlement off the
// request goroutine), and an offline create re-attempts here on the next sync.
//
// INVARIANT (ADR-003 §4): claim-confirm happens-before a node is settled. The
// confirm-then-renumber ordering lives in sqlite.SettleNode; this service only
// schedules and aggregates it, so the invariant holds for every path that
// reaches the store through here.
//
// Liveness, not security (ADR-003 §9): a broken or unreachable hub can at worst
// leave nodes provisional and force a retry; it never loses a node, because the
// canonical node is always in the local store and a failed settlement is a no-op.

// SettlementStore is the slice of the store the settlement engine needs (ADR-003
// §4). Narrowing it to these three methods keeps the service decoupled from the
// full Store and makes the drain loop unit-testable.
type SettlementStore interface {
	// PendingSettlements lists, shallow-first, the uids of provisional nodes
	// ready to settle (ancestors already settled), per ADR-003 §4/§5.
	PendingSettlements(ctx context.Context) ([]string, error)
	// SettleNode confirms a clean number for one node and renumbers it, or
	// returns sqlite.ErrHubUnreachable when the hub is down (ADR-003 §4).
	SettleNode(ctx context.Context, settler sqlite.Settler, uid string) (string, error)
}

// SettlementService drives background settlement of provisional nodes against
// the hub registry (ADR-003 §4). It is safe for concurrent use by the caller; a
// single drain pass (SettlePending) is internally sequential so nodes settle
// parent-before-child.
type SettlementService struct {
	store     SettlementStore
	settler   sqlite.Settler
	reachable func() bool
	logger    *slog.Logger
}

// NewSettlementService creates a SettlementService (ADR-003 §4). A nil settler
// (no hub configured) makes every operation a safe no-op — the single-user case
// has no hub to settle against and keeps its clean local numbers. reachable is a
// CHEAP, NON-BLOCKING probe of hub connectivity the create path consults to
// decide settled-vs-provisional; a nil reachable is treated as "unreachable".
func NewSettlementService(
	store SettlementStore, settler sqlite.Settler, reachable func() bool, logger *slog.Logger,
) *SettlementService {
	if reachable == nil {
		reachable = func() bool { return false }
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &SettlementService{
		store:     store,
		settler:   settler,
		reachable: reachable,
		logger:    logger,
	}
}

// Enabled reports whether a hub Settler is configured. When false, all
// settlement is a no-op and creation keeps clean local numbers (ADR-003 §4).
func (svc *SettlementService) Enabled() bool {
	return svc != nil && svc.settler != nil
}

// Reachable reports the hub's current connectivity via the cheap, non-blocking
// probe (ADR-003 §4). The create path uses it to decide whether a new child is
// born clean (online) or provisional (offline). It is false when no hub is
// configured.
func (svc *SettlementService) Reachable() bool {
	return svc.Enabled() && svc.reachable()
}

// SettlePending drains the store's pending-settlement worklist once, settling
// every ready provisional node it can (ADR-003 §4 next-sync settlement). It
// returns the number settled this pass and the number still provisional
// afterwards.
//
// On an unreachable hub it stops early — there is no point trying further nodes
// this pass — and reports the rest as remaining for the next sync (ADR-003 §4
// offline fallback). A per-node ancestor-not-settled outcome is skipped (it
// becomes ready once its ancestor settles, ADR-003 §5); any other per-node error
// is logged and skipped so one bad node never wedges the whole drain (ADR-003
// §6.1/F-1, block-scope discipline). A nil/disabled settler is a no-op.
func (svc *SettlementService) SettlePending(ctx context.Context) (settled, remaining int, err error) {
	if !svc.Enabled() {
		return 0, 0, nil
	}

	pending, err := svc.store.PendingSettlements(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("list pending settlements: %w", err)
	}

	for i, uid := range pending {
		_, settleErr := svc.store.SettleNode(ctx, svc.settler, uid)
		switch {
		case settleErr == nil:
			settled++
		case errors.Is(settleErr, sqlite.ErrHubUnreachable):
			// Hub went down mid-pass: leave this and every later node for the
			// next sync (ADR-003 §4 offline fallback).
			remaining = len(pending) - i
			svc.logger.Info("settlement_deferred_hub_unreachable",
				"event", "settlement_deferred_hub_unreachable",
				"settled", settled, "remaining", remaining)
			return settled, remaining, nil
		case errors.Is(settleErr, sqlite.ErrAncestorUnsettled):
			// Not ready this pass; becomes ready once its ancestor settles.
			remaining++
		default:
			// One node's failure must not wedge the drain (ADR-003 §6.1/F-1).
			remaining++
			svc.logger.Warn("settlement_node_failed",
				"event", "settlement_node_failed", "uid", uid, "error", settleErr)
		}
	}
	return settled, remaining, nil
}
