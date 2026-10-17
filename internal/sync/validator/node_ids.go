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

// ValidateEventNodeIDs checks every node id an event names against the
// node id grammar (model.ValidateSyncNodeID, MTIX-187): its node_id, the
// parent_id of a create_node (a root create has none) and the
// depends_on_node_id of a link_dep or unlink_dep, which is required. mtix
// never creates an id outside the grammar, so such an id can only come
// from another client; a replica refuses the event instead of applying it.
// The payload is decoded into the op's own apply struct, the way the apply
// does, so a field of another op cannot hide a bad id; a payload that does
// not decode is refused here (fail closed), never passed.
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
	field, id, err := payloadNodeRef(e)
	if err != nil {
		return err
	}
	if e.OpType == model.OpCreateNode && id == "" {
		return nil
	}
	return checkNodeID(e, field, id)
}

// payloadNodeRef decodes the payload of a create_node, link_dep or
// unlink_dep into the struct its apply uses and returns the payload field
// that names another node with that node's id. A payload that does not
// decode is refused with model.ErrInvalidInput.
func payloadNodeRef(e *model.SyncEvent) (field, id string, err error) {
	switch e.OpType {
	case model.OpCreateNode:
		var p model.CreateNodePayload
		if err = json.Unmarshal(e.Payload, &p); err == nil {
			return "parent_id", p.ParentID, nil
		}
	case model.OpLinkDep:
		var p model.LinkDepPayload
		if err = json.Unmarshal(e.Payload, &p); err == nil {
			return "depends_on_node_id", p.DependsOnNodeID, nil
		}
	case model.OpUnlinkDep:
		var p model.UnlinkDepPayload
		if err = json.Unmarshal(e.Payload, &p); err == nil {
			return "depends_on_node_id", p.DependsOnNodeID, nil
		}
	}
	return "", "", fmt.Errorf("event %s %s: payload does not decode: %w: %w",
		e.EventID, e.OpType, model.ErrInvalidInput, err)
}

// checkNodeID returns the ingest error for id, the value of field in e, when
// it is outside the node id grammar, and nil otherwise.
func checkNodeID(e *model.SyncEvent, field, id string) error {
	if err := model.ValidateSyncNodeID(id); err != nil {
		return fmt.Errorf("%w: event %s %s %q: %w", ErrInvalidNodeID, e.EventID, field, id, err)
	}
	return nil
}
