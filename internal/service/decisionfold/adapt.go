// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package decisionfold

import (
	"encoding/json"

	"github.com/hyper-swe/mtix/internal/model"
)

// EventFromSync reads a decision event from a sync event. ok is false when
// the event is not a comment that carries a decision object. Only the
// comment payload and the clock fields are read; hub stamps, sync status
// and retention are ignored.
func EventFromSync(se model.SyncEvent) (ev Event, ok bool) {
	if se.OpType != model.OpComment {
		return Event{}, false
	}
	ing, found := model.IngestDecisionFromComment(se.Payload)
	if !found {
		return Event{}, false
	}
	var p model.CommentPayload
	if err := json.Unmarshal(se.Payload, &p); err != nil {
		return Event{}, false
	}
	return Event{
		EventID:       se.EventID,
		AuthorID:      p.AuthorID,
		To:            p.To,
		WallClockTS:   se.WallClockTS,
		Lamport:       se.LamportClock,
		Class:         ing.Class,
		Kind:          ing.Kind,
		Target:        ing.Target,
		RequestKey:    ing.RequestKey,
		PayloadSHA256: ing.PayloadSHA256,
		Decision:      ing.Value,
	}, true
}

// DeleteFromSync reads a delete event. ok is false for any other op type.
// The optional payload fields cascade and path are read when present; a
// payload that does not carry them, or does not parse, gives a delete of
// the envelope node only.
func DeleteFromSync(se model.SyncEvent) (d Delete, ok bool) {
	if se.OpType != model.OpDelete {
		return Delete{}, false
	}
	d = Delete{UID: se.UID, NodeID: se.NodeID}
	var extra struct {
		Cascade bool   `json:"cascade"`
		Path    string `json:"path"`
	}
	if err := json.Unmarshal(se.Payload, &extra); err == nil {
		d.Cascade, d.Path = extra.Cascade, extra.Path
	}
	return d, true
}
