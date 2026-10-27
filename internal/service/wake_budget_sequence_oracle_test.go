// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
)

type budgetSequenceBoard struct {
	board   *wakeBudgetBoard
	claims  map[int64]bool
	acked   map[int64]bool
	lastAck int64
	lineage string
}

type budgetSequenceSnapshot struct {
	raw           []byte
	claims, acked map[int64]bool
	lastAck       int64
}

type budgetSequence struct {
	t                *testing.T
	f                *wakeBudgetFixture
	boards           map[string]*budgetSequenceBoard
	paths            map[string]string
	snapshots        map[string]budgetSequenceSnapshot
	active           string
	next, last       int
	charged, unacked int64
	floors           map[string]int64
}

func newBudgetSequence(t *testing.T, f *wakeBudgetFixture) *budgetSequence {
	t.Helper()
	b := f.board(t, 40)
	return &budgetSequence{t: t, f: f, active: "A", last: -1,
		boards: map[string]*budgetSequenceBoard{"A": {board: b, claims: map[int64]bool{}, acked: map[int64]bool{}, lineage: "first"}},
		floors: map[string]int64{}, paths: map[string]string{"A": b.path}, snapshots: map[string]budgetSequenceSnapshot{}}
}

func (s *budgetSequence) step(operation string) {
	kind, name, _ := strings.Cut(operation, ":")
	switch kind {
	case "wake", "error", "duplicate":
		s.attempt(kind)
	case "ack", "ack-future", "ack-repeat":
		s.acknowledge(kind)
	case "move":
		s.move(name)
	case "copy":
		s.copy(name)
	case "snapshot":
		s.snapshot(name)
	case "restore":
		s.restore(name)
	case "switch":
		require.Contains(s.t, s.boards, name)
		s.active = name
	case "new":
		b := s.f.board(s.t, 40)
		s.boards[name] = &budgetSequenceBoard{board: b, claims: map[int64]bool{}, acked: map[int64]bool{}, lineage: name}
		s.paths[name], s.active = b.path, name
	default:
		s.t.Fatalf("unknown sequence operation %s", operation)
	}
}

func (s *budgetSequence) attempt(kind string) {
	b := s.boards[s.active]
	index := s.next
	if kind == "duplicate" {
		index = s.last
	}
	require.GreaterOrEqual(s.t, index, 0)
	key := b.board.keys[index]
	allowed := !b.claims[key.EventSeq] && s.charged < 12 && s.unacked < 5
	var cause error
	if kind == "error" {
		cause = errors.New("sequence transport failed")
	}
	before := s.f.sent.Load()
	err := b.board.dispatcher(s.t, false, cause).Pass(s.t.Context(), b.board.keys[index:index+1])
	if allowed && cause != nil {
		require.ErrorIs(s.t, err, cause)
	} else {
		require.NoError(s.t, err)
	}
	want := before
	if allowed {
		want++
		s.charged++
		s.unacked++
		s.floors[b.lineage]++
		b.claims[key.EventSeq] = true
		s.last = index
		if kind != "duplicate" {
			s.next++
		}
	}
	require.Equal(s.t, want, s.f.sent.Load(), "admission must match the independent attempt/debt oracle")
	rows, err := b.board.store.WakeBudgetCounters(s.t.Context())
	require.NoError(s.t, err)
	if len(rows) > 0 {
		require.Equal(s.t, s.floors[b.lineage], rows[0].Total, "copies and restored paths must share the full lineage counter floor")
	}
}

func (s *budgetSequence) acknowledge(kind string) {
	b := s.boards[s.active]
	index := s.last
	if kind == "ack-future" {
		index = len(b.board.keys) - 1
	}
	require.GreaterOrEqual(s.t, index, 0)
	seq := b.board.keys[index].EventSeq
	if kind == "ack-repeat" {
		seq = b.lastAck
	}
	require.Positive(s.t, seq)
	require.NoError(s.t, service.NewInboxService(b.board.store).InboxAck(s.t.Context(), "budget-worker", seq))
	if !b.acked[seq] {
		s.unacked = 0
		b.acked[seq] = true
	}
	b.lastAck = seq
}

func (s *budgetSequence) assertAccounting() {
	b := s.boards[s.active]
	m, present, err := s.f.state.ReadWakeBudget(b.board.keys[0].Recipient())
	require.NoError(s.t, err)
	if !present {
		require.Zero(s.t, s.charged)
		return
	}
	var used int64
	for _, credit := range m.Credits {
		require.True(s.t, credit.At.After(s.f.now.Add(-time.Hour)))
		used += credit.Count
	}
	require.Equal(s.t, s.charged, used, "no move, restore, ACK or copy may charge history twice or refund allowance")
	require.Equal(s.t, s.unacked, m.Unacked, "only a real admission or newly effective ACK changes debt")
	require.LessOrEqual(s.t, used, int64(12))
}
