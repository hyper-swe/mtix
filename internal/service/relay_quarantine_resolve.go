// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// Resolution modes of `mtix sync quarantine resolve` (MTIX-268).
const (
	RelayResolveAccept   = "accept"
	RelayResolveRecreate = "recreate"

	// relayResolveReasonMax bounds the audit reason, as acknowledge does.
	relayResolveReasonMax = 500
)

// RelayQuarantineResolver settles records relay ingest skipped (MTIX-268).
type RelayQuarantineResolver struct {
	store *sqlite.Store
	nodes *NodeService
	clock func() time.Time
}

// NewRelayQuarantineResolver builds the resolver; clock is the injected time source.
func NewRelayQuarantineResolver(st *sqlite.Store, nodes *NodeService, clock func() time.Time) *RelayQuarantineResolver {
	return &RelayQuarantineResolver{store: st, nodes: nodes, clock: clock}
}

// RelayResolveRequest names one relay row and how to settle it.
type RelayResolveRequest struct {
	Handle string
	Mode   string
	Actor  string
	Reason string
}

// RelayResolveResult says what was settled.
type RelayResolveResult struct {
	Handle      string `json:"handle"`
	PeerID      string `json:"peer_id"`
	RS          uint64 `json:"rs"`
	NodeID      string `json:"node_id"`
	Resolution  string `json:"resolution"`
	RecreatedAs string `json:"recreated_as,omitempty"`
	Actor       string `json:"actor"`
	ResolvedAt  string `json:"resolved_at"`
	Reason      string `json:"reason"`
}

// Resolve settles one open relay row. Accept records the audit receipt and keeps
// the record retained and unapplied. Recreate makes a NEW task from a skipped
// create_node record through NodeService, and marks the row resolved in the same
// transaction as the create: both happen or neither does. Recreate is refused for
// any other op (MTIX-268).
func (r *RelayQuarantineResolver) Resolve(ctx context.Context, req RelayResolveRequest) (*RelayResolveResult, error) {
	if err := validateRelayResolve(req); err != nil {
		return nil, err
	}
	rows, err := r.store.ListRelayQuarantine(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve relay quarantine: %w", err)
	}
	row, err := resolveRelayHandle(rows, req.Handle)
	if err != nil {
		return nil, err
	}
	if row.State != sqlite.RelayQuarantineOpen {
		return nil, fmt.Errorf("relay record %s rs %d is already resolved (%s by %s): %w",
			row.PeerID, row.RS, row.Resolution, row.ResolvedBy, model.ErrInvalidInput)
	}
	res := sqlite.RelayResolution{Actor: req.Actor, Reason: strings.TrimSpace(req.Reason), At: r.clock().UTC()}
	if req.Mode == RelayResolveAccept {
		res.Resolution = sqlite.RelayResolutionAccepted
		err = r.settle(ctx, row, res)
	} else {
		res.Resolution = sqlite.RelayResolutionRecreate
		res.RecreatedAs, err = r.recreate(ctx, row, res, rows)
	}
	if err != nil {
		return nil, err
	}
	return relayResolveResult(row, res), nil
}

func validateRelayResolve(req RelayResolveRequest) error {
	if req.Mode != RelayResolveAccept && req.Mode != RelayResolveRecreate {
		return fmt.Errorf("resolve mode must be --accept or --recreate: %w", model.ErrInvalidInput)
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" || len(reason) > relayResolveReasonMax {
		return fmt.Errorf("--reason is required and at most %d bytes: %w", relayResolveReasonMax, model.ErrInvalidInput)
	}
	if strings.TrimSpace(req.Actor) == "" {
		return fmt.Errorf("resolve needs an author id: %w", model.ErrInvalidInput)
	}
	return nil
}

// settle marks the row in its own transaction.
func (r *RelayQuarantineResolver) settle(ctx context.Context, row sqlite.RelayQuarantineRow, res sqlite.RelayResolution) error {
	return r.store.WithTx(ctx, func(tx *sql.Tx) error {
		return markRelayResolved(ctx, tx, row, res)
	})
}

// markRelayResolved settles the row and fails when it was not open any more.
func markRelayResolved(ctx context.Context, tx *sql.Tx, row sqlite.RelayQuarantineRow, res sqlite.RelayResolution) error {
	settled, err := sqlite.ResolveRelayQuarantineTx(ctx, tx, row.RelayQuarantineKey, res)
	if err != nil {
		return err
	}
	if !settled {
		return fmt.Errorf("relay record %s rs %d was resolved by another command: %w", row.PeerID, row.RS, model.ErrConflict)
	}
	return nil
}

// recreate builds the new task from the skipped create and settles the row in
// the create's transaction. It returns the new task id.
func (r *RelayQuarantineResolver) recreate(ctx context.Context, row sqlite.RelayQuarantineRow, res sqlite.RelayResolution,
	rows []sqlite.RelayQuarantineRow) (string, error) {
	req, err := recreateRequest(row, res.Actor, rows)
	if err != nil {
		return "", err
	}
	node, err := r.nodes.CreateNodeInTx(ctx, req, func(ctx context.Context, tx *sql.Tx, n *model.Node) error {
		if repeatErr := refuseRepeatedRecreate(ctx, tx, row, rows); repeatErr != nil {
			return repeatErr
		}
		res.RecreatedAs = n.ID
		return markRelayResolved(ctx, tx, row, res)
	})
	if err != nil {
		return "", fmt.Errorf("recreate relay record %s rs %d: %w", row.PeerID, row.RS, err)
	}
	return node.ID, nil
}

// recreateRequest turns a skipped task creation into a create request that
// keeps its content; the id is allocated fresh.
func recreateRequest(row sqlite.RelayQuarantineRow, actor string, rows []sqlite.RelayQuarantineRow) (*CreateNodeRequest, error) {
	if row.OpType != string(model.OpCreateNode) {
		return nil, fmt.Errorf("--recreate applies only to a skipped task creation, and this record is a %q operation: "+
			"read it with 'mtix sync quarantine list --json', then settle it with --accept: %w",
			row.OpType, model.ErrInvalidInput)
	}
	var ev model.SyncEvent
	var p model.CreateNodePayload
	if err := json.Unmarshal(row.Payload, &ev); err != nil {
		return nil, fmt.Errorf("decode skipped record: %w", model.ErrInvalidInput)
	}
	if err := json.Unmarshal(ev.Payload, &p); err != nil || ev.ProjectPrefix == "" {
		return nil, fmt.Errorf("skipped record has no usable create payload: %w", model.ErrInvalidInput)
	}
	creator := p.Creator
	if creator == "" {
		creator = actor
	}
	parent, err := recreateParent(row, p.ParentID, rows)
	if err != nil {
		return nil, err
	}
	return &CreateNodeRequest{
		IssueType: p.IssueType, ParentID: parent, Project: ev.ProjectPrefix, Title: p.Title,
		Description: p.Description, Prompt: p.Prompt, Acceptance: p.Acceptance,
		Labels: p.Labels, Priority: p.Priority, Creator: creator,
	}, nil
}

func relayResolveResult(row sqlite.RelayQuarantineRow, res sqlite.RelayResolution) *RelayResolveResult {
	return &RelayResolveResult{
		Handle: QuarantineHandle(RelayQuarantineKeyString(row.RelayQuarantineKey)),
		PeerID: row.PeerID, RS: row.RS, NodeID: row.NodeID, Resolution: res.Resolution,
		RecreatedAs: res.RecreatedAs, Actor: res.Actor,
		ResolvedAt: res.At.UTC().Format(time.RFC3339), Reason: res.Reason,
	}
}

// refuseRepeatedRecreate stops a second recreate of the same event, inside the
// create's transaction (so a concurrent recreate cannot slip past): a republish
// (reset-peer) can make ingest skip the same event again at a new position, and
// recreating it would add another copy. The error rolls the new task back; the
// message names the first recreate and the exact accept command.
func refuseRepeatedRecreate(ctx context.Context, tx *sql.Tx, row sqlite.RelayQuarantineRow, rows []sqlite.RelayQuarantineRow) error {
	prior, err := sqlite.RelayEventRecreatedTx(ctx, tx, row.RelayQuarantineKey, row.EventID)
	if err != nil || prior == nil {
		return err
	}
	return fmt.Errorf("this record repeats event %s, which relay record %s rs %d already recreated as %s: "+
		"a second recreate would add another copy; settle this one with %s: %w",
		format.EventID(row.EventID), prior.PeerID, prior.RS, prior.RecreatedAs,
		relayAcceptCommand(relayHandleOf(rows, row.RelayQuarantineKey)), model.ErrInvalidInput)
}
