// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"fmt"
	"strings"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// recreateParent maps the payload's parent id for a recreate (MTIX-268). That id
// comes from the author's numbering, which is the numbering that collided: when
// the parent was itself a create from the same peer that relay ingest skipped,
// the id names a different local task (or none). A recreated parent supplies its
// new id; an open or accepted one refuses, naming the parent's handle. Any other
// parent id is used as it is.
func recreateParent(row sqlite.RelayQuarantineRow, parentID string, rows []sqlite.RelayQuarantineRow) (string, error) {
	if parentID == "" {
		return "", nil
	}
	keys := make([]string, len(rows))
	for i, r := range rows {
		keys[i] = RelayQuarantineKeyString(r.RelayQuarantineKey)
	}
	handles := QuarantineHandles(keys)
	found, foundHandle, others := findSkippedParent(row, parentID, rows, handles)
	if len(others) > 0 {
		return "", fmt.Errorf("the parent %s of this record is ambiguous: another peer's create of %s was also skipped "+
			"(%s), so the id may name a different task than the one this peer meant: settle this record with "+
			"mtix sync quarantine resolve --handle <this handle> --accept --reason \"<why>\" and recreate it by hand "+
			"under the right parent if it is still wanted: %w", parentID, parentID, strings.Join(others, "; "), model.ErrInvalidInput)
	}
	switch {
	case found == nil:
		return parentID, nil
	case found.State == sqlite.RelayQuarantineOpen:
		return "", fmt.Errorf("the parent %s was itself skipped from peer %s (rs %d) and is not recreated yet, "+
			"so this record would attach to whatever local task holds %s: resolve the parent first with "+
			"mtix sync quarantine resolve --handle %s --recreate --reason \"<why>\", then recreate this record: %w",
			parentID, row.PeerID, found.RS, parentID, foundHandle, model.ErrInvalidInput)
	case found.RecreatedAs == "":
		return "", fmt.Errorf("the parent %s of this record was accepted and is not a task here (handle %s): "+
			"settle this record with mtix sync quarantine resolve --handle <this handle> --accept --reason \"<why>\": %w",
			parentID, foundHandle, model.ErrInvalidInput)
	}
	return found.RecreatedAs, nil
}

// findSkippedParent scans the relay rows for skipped creates of the parent id:
// the newest one from the same peer (by publisher epoch, then relay sequence),
// and a description of every one from any other peer.
func findSkippedParent(row sqlite.RelayQuarantineRow, parentID string, rows []sqlite.RelayQuarantineRow,
	handles []string) (found *sqlite.RelayQuarantineRow, foundHandle string, others []string) {
	for i := range rows {
		r := rows[i]
		if r.OpType != string(model.OpCreateNode) || r.NodeID != parentID || r.RelayQuarantineKey == row.RelayQuarantineKey {
			continue
		}
		if r.PeerID != row.PeerID {
			others = append(others, fmt.Sprintf("peer %s rs %d (%s, handle %s)", r.PeerID, r.RS, r.State, handles[i]))
			continue
		}
		if found == nil || r.PubEpoch > found.PubEpoch || (r.PubEpoch == found.PubEpoch && r.RS > found.RS) {
			found, foundHandle = &rows[i], handles[i]
		}
	}
	return found, foundHandle, others
}
