// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"fmt"
	"math"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
)

func (a *wakeAdmission) reconcileWakeBudget(p model.WakeBudgetProposal) (oplocal.WakeBudgetSource, error) {
	m, snap := a.mirror, p.Snapshot
	if snap.Source == "" || snap.Total < 0 || snap.AckMark < 0 {
		return oplocal.WakeBudgetSource{}, fmt.Errorf("wake accounting snapshot is invalid: %w", model.ErrInvalidInput)
	}
	row, err := wakeBudgetPathBinding(m.Sources[snap.Source], p)
	if err != nil {
		return row, fmt.Errorf("bind wake accounting identity: %w", err)
	}
	id := row.BoardID
	if m.Boards == nil {
		m.Boards = map[string]oplocal.WakeBudgetBoard{}
	}
	board, known := m.Boards[id]
	if !known {
		// Identity-less history is ambiguous: baseline it without charging or refunding.
		board.Total, board.Committed = max(snap.Total, row.Total), max(snap.Total, row.Committed)
		board.Pending = row.Pending
		if board.Total == board.Committed {
			board.Pending = ""
		}
	}
	if err := a.observeWakeRewind(snap.Total, board.Committed); err != nil {
		return row, fmt.Errorf("observe protected wake counter rewind: %w", err)
	}
	if snap.Total > board.Total {
		if err := chargeWakeBudget(m, a.now, snap.Total-board.Total); err != nil {
			return row, fmt.Errorf("reconcile unexplained wake counter advance: %w", err)
		}
		board.Total = snap.Total
	}
	if snap.Total >= board.Total {
		board.Committed, board.Pending = board.Total, ""
	}
	row.Total, row.Committed, row.Pending = board.Total, board.Committed, board.Pending
	row.AckMark = max(row.AckMark, snap.AckMark)
	m.Boards[id], m.Sources[snap.Source] = board, row
	return row, nil
}

func (a *wakeAdmission) observeWakeRewind(total, committed int64) error {
	if total >= committed {
		return nil
	}
	if a.mirror.Rewinds == math.MaxUint64 {
		return fmt.Errorf("wake rewind counter is exhausted: %w", model.ErrInvalidInput)
	}
	a.mirror.Rewinds++
	a.mirror.LastRewindAt, a.rewound = a.now, true
	return nil
}

func wakeBudgetPathBinding(row oplocal.WakeBudgetSource, p model.WakeBudgetProposal) (oplocal.WakeBudgetSource, error) {
	id := p.Snapshot.BoardID
	if id == "" {
		id = row.BoardID
	}
	if id == "" {
		id = p.BoardCandidate
	}
	if id == "" {
		return row, fmt.Errorf("wake board identity is missing: %w", model.ErrInvalidInput)
	}
	if row.BoardID != "" && row.BoardID != id {
		row = oplocal.WakeBudgetSource{BoardID: id}
	}
	row.BoardID = id

	return row, nil
}
