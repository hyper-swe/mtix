// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package ingest

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/relay/segment"
	"github.com/hyper-swe/mtix/internal/sync/validator"
)

// skippedTaskCause says whether a record names a task whose creation this same
// peer published and ingest skipped (MTIX-273). The record's task id then comes
// from the peer's own numbering, the numbering that collided with a local task,
// so applying the record would change the local task that holds the same id: a
// child would attach under it, an edit or a status change would land on it. The
// answer is a refusal cause, or nil when the record can go to the apply path.
//
// The lookup covers every state of the skipped creation (open, accepted and
// recreated): a recreated parent has a new id here, but its children are mapped
// only by resolving each one, never on ingest. A lookup failure is not a verdict:
// it is returned as err and the poll stops with the cursor before the record.
func (in *Ingestor) skippedTaskCause(ctx context.Context, peerID string, e *model.SyncEvent) (cause, err error) {
	other, refErr := validator.OtherNodeRef(e)
	if refErr != nil {
		return nil, fmt.Errorf("read the tasks a relay record names: %w", refErr)
	}
	// The other task first: a child names its skipped parent in its payload.
	for _, id := range []string{other, e.NodeID} {
		if id == "" {
			continue
		}
		rs, held, lookupErr := in.cfg.Store.RelayQuarantinedCreate(ctx, peerID, id)
		if lookupErr != nil {
			return nil, lookupErr
		}
		if held {
			return skippedTaskReason(e, id, rs, id == other && other != ""), nil
		}
	}
	return nil, nil
}

// skippedTaskReason is the stored reason, in user terms: it names the task id and
// the skipped record, and says what applying the record would have done.
func skippedTaskReason(e *model.SyncEvent, id string, rs uint64, isParent bool) error {
	subject := "the task it changes"
	if isParent && e.OpType == model.OpCreateNode {
		subject = "its parent task"
	} else if isParent {
		subject = "the task it depends on"
	}
	return fmt.Errorf("event %s: %s %s was skipped (relay record %d from this peer, see sync quarantine list), "+
		"so this record was skipped too: applying it could change a different local task that has the same number: %w",
		format.EventID(e.EventID), subject, format.EventID(id), rs, model.ErrConflict)
}

// holdForSkippedTask runs skippedTaskCause and acts on the answer. handled is
// false when the record can go to the apply path. When handled is true the
// record must not be applied and stop is applyRecord's result: true after a
// lookup failure (the cursor stays), or refuse's result for a held record.
func (in *Ingestor) holdForSkippedTask(ctx context.Context, stats *Stats, peerID string, header segment.Header, rec segment.Record, e *model.SyncEvent) (handled, stop bool) {
	cause, err := in.skippedTaskCause(ctx, peerID, e)
	if err != nil {
		in.log.Error("relay ingest could not check a record against skipped tasks",
			slog.String("peer", peerID), slog.Uint64("rs", rec.RS), slog.Any("error", err))
		stats.fatal = fmt.Errorf("check relay record %s rs %d against skipped tasks: %w", peerID, rec.RS, err)
		return true, true
	}
	if cause == nil {
		return false, false
	}
	return true, in.refuse(ctx, stats, peerID, header, rec, e, cause)
}
