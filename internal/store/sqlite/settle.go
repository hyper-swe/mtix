// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// Local claim + eager settlement engine (ADR-003 §4 / MTIX-30.3).
//
// A node has two identifiers (ADR-003 §2): the display_path (the dot-path id,
// the only thing surfaced) and the durable uid (its create_node event id). A
// node whose trailing number is not yet hub-confirmed is PROVISIONAL — its
// display_path carries a uid segment (model.IsProvisional) and is collision-free
// by construction (ADR-003 §4). A node is SETTLED once it and all ancestors hold
// a hub-confirmed clean (fully-numeric) number.
//
// This file implements the CLIENT side of the claim protocol (ADR-003 §4):
//
//   - ClaimNextSeq      — eager LOCAL claim of the next free sibling number,
//     backed by the same atomic sequence used for normal id generation
//     (FR-2.7). It never touches the network, so the create call never blocks.
//   - SettleNode        — the hub-facing settlement of ONE provisional node:
//     claim a clean candidate, ask the Settler to confirm the claim against the
//     hub registry, and (on confirm) renumber the node to that clean number. On
//     a renumber-required outcome it claims the NEXT free number and retries; on
//     an unreachable hub it leaves the node provisional for the next sync.
//   - PendingSettlements — the worklist a background settler drains on each sync:
//     provisional nodes whose ancestors are all already settled, shallow-first,
//     so a parent settles before its children (a clean child cannot live under an
//     unsettled ancestor, ADR-003 §5).
//
// INVARIANT (ADR-003 §4): a node's claim-confirm happens-before that node is
// first treated as settled. SettleNode enforces this by calling the Settler for
// the CLEAN candidate path and only committing the clean renumber after the
// Settler confirms; an unreachable hub or a transport error leaves the node
// provisional and unchanged. A renumber is therefore never observable as settled
// without a prior confirm.
//
// Threat model (ADR-003 §9, §14): the Settler/registry is a LIVENESS mechanism,
// not a security boundary. A broken or hostile hub can at worst force a retry to
// the next number; it can never lose or corrupt a node, because the canonical
// node always lives in this local store and a failed settlement is a no-op.

// SettleOutcome is the result of asking the hub registry to confirm one claim of
// a clean (project, parent, number) under the claim protocol (ADR-003 §4, §6).
type SettleOutcome int

const (
	// SettleConfirmed means the registry accepted the claim: the candidate
	// number is the node's and it may be committed as settled (ADR-003 §4).
	SettleConfirmed SettleOutcome = iota
	// SettleRenumberRequired means the candidate number is already held by a
	// DIFFERENT node on the hub (first-writer-wins, ADR-003 §6); the claimer
	// must retry the next free number.
	SettleRenumberRequired
	// SettleUnreachable means the hub could not be reached, so the claim is
	// neither confirmed nor rejected; the node stays provisional and re-attempts
	// on the next sync (ADR-003 §4 offline fallback).
	SettleUnreachable
)

// ClaimRequest describes one claim a Settler is asked to confirm against the hub
// registry (ADR-003 §4, §6). It carries the node's durable uid (its stable
// logical identity, ADR-003 §2) and the clean candidate it wants to settle into.
type ClaimRequest struct {
	// UID is the node's durable identity (its create_node event id, ADR-003 §2).
	UID string
	// ProjectPrefix is the FR-2.1a project prefix of the contested namespace.
	ProjectPrefix string
	// ParentID is the settled display_path of the node's parent ("" for a root).
	ParentID string
	// DisplayPath is the clean, fully-numeric candidate id being claimed.
	DisplayPath string
	// Seq is the trailing sibling number of DisplayPath.
	Seq int
}

// Settler confirms a clean-number claim against the hub registry (ADR-003 §4).
// Implementations live in the sync transport; this store-side interface keeps
// the settlement engine decoupled from the transport (and from postgres), so the
// claim/retry/provisional-fallback logic is unit-testable with a double.
//
// ConfirmClaim MUST be safe for concurrent use and MUST classify a transport /
// connectivity failure as SettleUnreachable (with a nil error) rather than
// returning an error, so the engine can apply the offline fallback. A non-nil
// error is reserved for unexpected faults and aborts the settlement, leaving the
// node unchanged (ADR-003 §9).
type Settler interface {
	ConfirmClaim(ctx context.Context, req ClaimRequest) (SettleOutcome, error)
}

// ErrHubUnreachable is returned by SettleNode when the Settler reports the hub
// unreachable, so the node stays provisional and is retried on the next sync
// (ADR-003 §4 offline fallback). It is an expected, non-fatal condition.
var ErrHubUnreachable = errors.New("hub unreachable: node remains provisional")

// ErrAncestorUnsettled is returned by SettleNode when a node cannot settle
// because an ancestor is still provisional. A clean child cannot exist under an
// unsettled ancestor (ADR-003 §5), so the ancestor must settle first.
var ErrAncestorUnsettled = errors.New("ancestor not yet settled")

// ClaimNextSeq atomically claims and returns the next free sibling sequence
// number under parentID within project (ADR-003 §4 eager local claim). It is the
// same atomic, collision-free counter used for normal id generation (FR-2.7), so
// a burst of concurrent sibling claims each receive a distinct number. parentID
// is the parent's display_path ("" for a project root). This is a purely LOCAL
// operation — it never contacts the hub, which is why a create that calls it
// never blocks on the network (ADR-003 §4).
func (s *Store) ClaimNextSeq(ctx context.Context, project, parentID string) (int, error) {
	return s.NextSequence(ctx, sequenceKey(project, parentID))
}

// sequenceKey builds the per-namespace sequence key '{project}:{parent_dotpath}'
// used by NextSequence (FR-2.7); the root namespace has an empty parent.
func sequenceKey(project, parentID string) string {
	return project + ":" + parentID
}

// settleTarget holds the columns SettleNode needs about the node being settled.
type settleTarget struct {
	uid      string
	id       string
	project  string
	parentID string
}

// SettleNode drives the hub-facing settlement of one provisional node, keyed by
// its durable uid (ADR-003 §4 claim protocol). It returns the node's settled
// (clean, numeric) display_path on success.
//
// Flow (ADR-003 §4):
//  1. Resolve the node by uid. An already-settled (clean) node is an idempotent
//     no-op — it confirms nothing and is returned as-is.
//  2. Refuse if any ancestor is still provisional (ErrAncestorUnsettled): a
//     clean child cannot exist under an unsettled ancestor (ADR-003 §5).
//  3. Claim the next free sibling number locally (ClaimNextSeq) and ask the
//     Settler to confirm that CLEAN candidate against the hub registry.
//  4. On SettleConfirmed: renumber the node (and its subtree) to the clean
//     number in a single transaction (RenumberSubtree, ADR-003 §5) and return.
//  5. On SettleRenumberRequired: the number is taken on the hub — claim the next
//     free number and retry (bounded), per first-writer-wins (ADR-003 §6).
//  6. On SettleUnreachable: leave the node provisional and return
//     ErrHubUnreachable for retry on the next sync (ADR-003 §4 offline fallback).
//
// INVARIANT (ADR-003 §4): the claim-confirm (step 3) happens-before the node is
// observable as settled (step 4). The clean renumber is committed only after the
// Settler confirms, so the node is never surfaced as settled without a confirm.
func (s *Store) SettleNode(ctx context.Context, settler Settler, uid string) (string, error) {
	target, err := s.loadSettleTarget(ctx, uid)
	if err != nil {
		return "", err
	}

	// Idempotent: an already-clean node is settled; nothing to confirm.
	if !model.IsProvisional(target.id) {
		return target.id, nil
	}

	// A clean child requires a settled parent (ADR-003 §5).
	if model.IsProvisional(target.parentID) {
		return "", fmt.Errorf("settle %s under %s: %w",
			target.id, target.parentID, ErrAncestorUnsettled)
	}

	return s.settleWithRetry(ctx, settler, target)
}

// maxSettleAttempts bounds the retry-on-taken loop (ADR-003 §4, §6). Each
// attempt claims a fresh, strictly higher number, so contention resolves quickly;
// the bound guards against a pathological or misbehaving hub (ADR-003 §9).
const maxSettleAttempts = 64

// settleWithRetry runs the claim → confirm → renumber loop with retry-on-taken
// (ADR-003 §4, §6). Each iteration claims a NEW number, so a taken number is
// never re-tried and siblings converge to distinct numbers.
func (s *Store) settleWithRetry(
	ctx context.Context, settler Settler, target settleTarget,
) (string, error) {
	for attempt := 0; attempt < maxSettleAttempts; attempt++ {
		seq, err := s.ClaimNextSeq(ctx, target.project, target.parentID)
		if err != nil {
			return "", fmt.Errorf("settle %s: claim seq: %w", target.id, err)
		}
		candidate := model.BuildID(target.project, target.parentID, seq)

		outcome, err := settler.ConfirmClaim(ctx, ClaimRequest{
			UID:           target.uid,
			ProjectPrefix: target.project,
			ParentID:      target.parentID,
			DisplayPath:   candidate,
			Seq:           seq,
		})
		if err != nil {
			return "", fmt.Errorf("settle %s: confirm claim %s: %w", target.id, candidate, err)
		}

		switch outcome {
		case SettleConfirmed:
			// Claim confirmed (happens-before): commit the clean renumber.
			if rErr := s.RenumberSubtree(ctx, target.id, seq); rErr != nil {
				return "", fmt.Errorf("settle %s -> %s: renumber: %w", target.id, candidate, rErr)
			}
			return candidate, nil
		case SettleRenumberRequired:
			// Number taken on the hub (first-writer-wins): retry the next free.
			continue
		case SettleUnreachable:
			return "", fmt.Errorf("settle %s: %w", target.id, ErrHubUnreachable)
		default:
			return "", fmt.Errorf("settle %s: unknown settle outcome %d", target.id, outcome)
		}
	}
	return "", fmt.Errorf("settle %s: gave up after %d taken-number retries: %w",
		target.id, maxSettleAttempts, ErrAlreadyClaimedNumber)
}

// ErrAlreadyClaimedNumber is returned when the retry-on-taken loop exhausts its
// attempt budget — every candidate it claimed was already taken on the hub. This
// is degenerate (a misbehaving or saturated hub, ADR-003 §9) and leaves the node
// provisional and intact for a later retry.
var ErrAlreadyClaimedNumber = errors.New("exhausted claim retries: every candidate number was taken")

// loadSettleTarget resolves a node by its durable uid for settlement, returning
// its current id, project, and parent display_path (ADR-003 §4). Returns
// model.ErrNotFound when no live node carries the uid.
func (s *Store) loadSettleTarget(ctx context.Context, uid string) (settleTarget, error) {
	if uid == "" {
		return settleTarget{}, fmt.Errorf("settle: empty uid: %w", model.ErrNotFound)
	}
	t := settleTarget{uid: uid}
	var parent sql.NullString
	err := s.readDB.QueryRowContext(ctx,
		`SELECT id, project, parent_id FROM nodes WHERE uid = ? AND deleted_at IS NULL`,
		uid).Scan(&t.id, &t.project, &parent)
	if errors.Is(err, sql.ErrNoRows) {
		return settleTarget{}, fmt.Errorf("settle uid %s: %w", uid, model.ErrNotFound)
	}
	if err != nil {
		return settleTarget{}, fmt.Errorf("settle: load node for uid %s: %w", uid, err)
	}
	t.parentID = parent.String
	return t, nil
}

// PendingSettlements returns, shallow-first, the uids of live provisional nodes
// that are READY to settle — i.e. whose ancestors are all already settled (their
// parent_id is clean), so settling each one is legal (a clean child cannot live
// under an unsettled ancestor, ADR-003 §5). A node still under an unsettled
// ancestor is intentionally omitted; it becomes ready once that ancestor
// settles. This is the worklist the background settler drains on each sync
// (ADR-003 §4 next-sync retry). The shallow-first order means a provisional
// parent settles before its provisional children, which then become ready.
func (s *Store) PendingSettlements(ctx context.Context) ([]string, error) {
	rows, err := s.readDB.QueryContext(ctx,
		`SELECT uid, id, parent_id FROM nodes
		 WHERE deleted_at IS NULL AND uid IS NOT NULL AND uid <> ''
		 ORDER BY depth ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("scan pending settlements: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var pending []string
	for rows.Next() {
		var uid, id string
		var parent sql.NullString
		if scanErr := rows.Scan(&uid, &id, &parent); scanErr != nil {
			return nil, fmt.Errorf("scan pending settlement row: %w", scanErr)
		}
		if !model.IsProvisional(id) {
			continue // already settled
		}
		if model.IsProvisional(parent.String) {
			continue // ancestor not settled yet; not ready this pass
		}
		pending = append(pending, uid)
	}
	return pending, rows.Err()
}
