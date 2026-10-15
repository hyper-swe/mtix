// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package validator

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// ErrInvalidNodeID marks an event that names a node id outside the node id
// grammar (MTIX-187). Its text, "invalid node id", starts the reason a
// pull stores for such an event, so `mtix sync status` and `mtix sync
// doctor` can count those events.
var ErrInvalidNodeID = errors.New("invalid node id")

// nodeIDRefs holds the payload fields that name a node: the parent of a
// create_node and the target of a link_dep or unlink_dep.
type nodeIDRefs struct {
	ParentID        string `json:"parent_id"`
	DependsOnNodeID string `json:"depends_on_node_id"`
}

// ValidateEventNodeIDs checks every node id an event names against the
// node id grammar (model.ValidateSyncNodeID, MTIX-187): its node_id, the
// parent_id of a create_node (a root create has none) and the
// depends_on_node_id of a link_dep or unlink_dep, which is required. mtix
// never creates an id outside the grammar, so such an id can only come
// from another client; a replica refuses the event instead of applying it.
// A payload that does not decode is left to the apply, which refuses it.
//
// Ingest runs it (ValidateIngest); push does not, so a replica that already
// holds such a task can still push its own changes to it. The error wraps
// ErrInvalidNodeID and model.ErrInvalidInput and names the field and id.
func ValidateEventNodeIDs(e *model.SyncEvent) error {
	if err := checkNodeID(e, "node_id", e.NodeID); err != nil {
		return err
	}
	if e.OpType != model.OpCreateNode && e.OpType != model.OpLinkDep && e.OpType != model.OpUnlinkDep {
		return nil
	}
	var refs nodeIDRefs
	if err := json.Unmarshal(e.Payload, &refs); err != nil {
		return nil //nolint:nilerr // a payload that does not decode is refused by the apply
	}
	if e.OpType == model.OpCreateNode {
		if refs.ParentID == "" {
			return nil
		}
		return checkNodeID(e, "parent_id", refs.ParentID)
	}
	return checkNodeID(e, "depends_on_node_id", refs.DependsOnNodeID)
}

// checkNodeID returns the ingest error for id, the value of field in e, when
// it is outside the node id grammar, and nil otherwise.
func checkNodeID(e *model.SyncEvent, field, id string) error {
	if err := model.ValidateSyncNodeID(id); err != nil {
		return fmt.Errorf("%w: event %s %s %q: %w", ErrInvalidNodeID, e.EventID, field, id, err)
	}
	return nil
}
