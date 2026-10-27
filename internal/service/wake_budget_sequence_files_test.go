// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"maps"
	"os"
	"path/filepath"

	"github.com/stretchr/testify/require"
)

func (s *budgetSequence) path(name string) string {
	if s.paths[name] == "" {
		s.paths[name] = filepath.Join(s.t.TempDir(), "wake.db")
	}
	return s.paths[name]
}

func (s *budgetSequence) move(name string) {
	require.NotContains(s.t, s.boards, name)
	b := s.boards[s.active]
	require.NoError(s.t, b.board.store.Close())
	target := s.path(name)
	require.NoError(s.t, os.Rename(b.board.path, target))
	b.board.path = target
	b.board.open(s.t)
	delete(s.boards, s.active)
	s.boards[name], s.active = b, name
}

func (s *budgetSequence) snapshot(name string) {
	b := s.boards[s.active]
	require.NoError(s.t, b.board.store.Close())
	raw, err := os.ReadFile(b.board.path)
	require.NoError(s.t, err)
	b.board.open(s.t)
	s.snapshots[name] = budgetSequenceSnapshot{raw, maps.Clone(b.claims), maps.Clone(b.acked), b.lastAck}
}

func (s *budgetSequence) restore(name string) {
	require.Contains(s.t, s.snapshots, name)
	b, snapshot := s.boards[s.active], s.snapshots[name]
	require.NoError(s.t, b.board.store.Close())
	require.NoError(s.t, os.WriteFile(b.board.path, snapshot.raw, 0o600))
	b.board.open(s.t)
	b.claims, b.acked, b.lastAck = maps.Clone(snapshot.claims), maps.Clone(snapshot.acked), snapshot.lastAck
}

func (s *budgetSequence) copy(name string) {
	require.NotContains(s.t, s.boards, name)
	b := s.boards[s.active]
	require.NoError(s.t, b.board.store.Close())
	raw, err := os.ReadFile(b.board.path)
	require.NoError(s.t, err)
	b.board.open(s.t)
	clone := &wakeBudgetBoard{fixture: s.f, path: s.path(name), keys: b.board.keys}
	require.NoError(s.t, os.WriteFile(clone.path, raw, 0o600))
	clone.open(s.t)
	s.t.Cleanup(func() { require.NoError(s.t, clone.store.Close()) })
	s.boards[name] = &budgetSequenceBoard{clone, maps.Clone(b.claims), maps.Clone(b.acked), b.lastAck, b.lineage}
}
