// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"crypto/sha1"
	"database/sql"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/sync/clock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func permissionInstructionIndex(name string) []byte {
	data := make([]byte, 12)
	copy(data, "DIRC")
	binary.BigEndian.PutUint32(data[4:8], 2)
	binary.BigEndian.PutUint32(data[8:12], 1)
	entry := make([]byte, 62)
	binary.BigEndian.PutUint16(entry[60:62], uint16(len(name)))
	entry = append(entry, []byte(name)...)
	entry = append(entry, 0)
	for len(entry)%8 != 0 {
		entry = append(entry, 0)
	}
	data = append(data, entry...)
	checksum := sha1.Sum(data) //nolint:gosec // Git's index format requires SHA-1.
	return append(data, checksum[:]...)
}

func permissionForeignEvent(t *testing.T, f *wakePermissionFixture, op model.OpType, payload any, sequence int64) {
	t.Helper()
	encoded, err := model.EncodePayload(payload)
	require.NoError(t, err)
	event := &model.SyncEvent{EventID: clock.MustNewEventID(), ProjectPrefix: "TEST", NodeID: "TEST-1", OpType: op, Payload: encoded,
		AuthorID: "fixture-foreign", AuthorMachineHash: "0123456789abcdef", LamportClock: sequence, VectorClock: model.VectorClock{"fixture-foreign": sequence}, WallClockTS: f.now.UnixMilli(), SyncStatus: model.SyncStatusApplied}
	require.NoError(t, f.store.WithTx(t.Context(), func(tx *sql.Tx) error { return f.store.IdempotentApply(t.Context(), tx, event) }))
}

func TestCandidates_ThreeIndependentFacts_AllUncheckedAndNoPermissionMutation(t *testing.T) {
	f := newWakePermissionFixture(t)
	f.register(t, "first")
	permissionForeignEvent(t, f, model.OpCreateNode, &model.CreateNodePayload{Title: "Synthetic candidates"}, 1)
	permissionForeignEvent(t, f, model.OpComment, &model.CommentPayload{Body: "Synthetic addressed fixture", AuthorID: "fixture-foreign", To: "first"}, 2)
	permissionForeignEvent(t, f, model.OpComment, &model.CommentPayload{Body: "Synthetic addressed fixture", AuthorID: "fixture-foreign", To: "addressed-only"}, 3)
	require.NoError(t, os.Mkdir(filepath.Join(f.board.Checkout, ".git"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(f.board.Checkout, ".git", "index"), permissionInstructionIndex("AGENTS.md"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(f.board.Checkout, "AGENTS.md"), []byte("mtix agent register first\nmtix agent register named-only\n"), 0600))
	facts, err := f.svc.Candidates(t.Context(), f.board)
	require.NoError(t, err)
	assert.Equal(t, []service.PermissionCandidate{
		{AgentID: "addressed-only", AddressedByForeignSenders: true},
		{AgentID: "first", RegisteredOnHost: true, AddressedByForeignSenders: true, NamedInInstructions: true},
		{AgentID: "named-only", NamedInInstructions: true},
	}, facts)
	for _, fact := range facts {
		assert.False(t, fact.Selected)
	}
	grants, err := f.state.AgentGrants()
	require.NoError(t, err)
	assert.Empty(t, grants)
	rows, err := f.state.Confirmations(t.Context())
	require.NoError(t, err)
	assert.Empty(t, rows)
}

func TestCandidates_NonGitAndUntrackedInstructions_FalseNames(t *testing.T) {
	for _, git := range []bool{false, true} {
		t.Run(map[bool]string{false: "non_git", true: "untracked"}[git], func(t *testing.T) {
			f := newWakePermissionFixture(t)
			f.register(t, "first")
			if git {
				require.NoError(t, os.Mkdir(filepath.Join(f.board.Checkout, ".git"), 0700))
				require.NoError(t, os.WriteFile(filepath.Join(f.board.Checkout, ".git", "index"), permissionInstructionIndex("other.md"), 0600))
			}
			require.NoError(t, os.WriteFile(filepath.Join(f.board.Checkout, "AGENTS.md"), []byte("mtix agent register first\n"), 0600))
			facts, err := f.svc.Candidates(t.Context(), f.board)
			require.NoError(t, err)
			assert.Equal(t, []service.PermissionCandidate{{AgentID: "first", RegisteredOnHost: true}}, facts)
		})
	}
}
