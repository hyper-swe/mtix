// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// MTIX-46 (option A): sync-apply maintains content_hash. A local UpdateNode
// recomputes it on content change, but the apply handlers skipped it — so a
// synced content edit left a stale hash and synced-created nodes had NULL. Since
// the hash is deterministic over content, recomputing on apply makes replicas
// converge on the SAME content_hash as the emitting replica (fixing export
// byte-divergence and import-merge mis-detection).

func TestApply_CreateNode_SetsContentHash(t *testing.T) {
	s, raw := applyTestStore(t)
	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "alice", 1,
		&model.CreateNodePayload{Title: "x"})))

	got := readNodeColumn(t, raw, "MTIX-1", "content_hash")
	want := model.ComputeContentHash("x", "", "", "", nil)
	assert.NotEmpty(t, got, "a synced-created node must not have a NULL content_hash")
	assert.Equal(t, want, got, "content_hash must match what the emitting replica computed")
}

func TestApply_UpdateField_RecomputesContentHash(t *testing.T) {
	s, raw := applyTestStore(t)
	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "alice", 1,
		&model.CreateNodePayload{Title: "old"})))
	createHash := readNodeColumn(t, raw, "MTIX-1", "content_hash")

	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpUpdateField, "MTIX-1", "alice", 2,
		&model.UpdateFieldPayload{FieldName: "title", NewValue: json.RawMessage(`"new"`)})))

	got := readNodeColumn(t, raw, "MTIX-1", "content_hash")
	want := model.ComputeContentHash("new", "", "", "", nil)
	assert.Equal(t, want, got, "a synced content edit must recompute content_hash")
	assert.NotEqual(t, createHash, got, "the hash changed with the content")
}

func TestApply_SetPrompt_RecomputesContentHash(t *testing.T) {
	s, raw := applyTestStore(t)
	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "alice", 1,
		&model.CreateNodePayload{Title: "t"})))

	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpSetPrompt, "MTIX-1", "alice", 2,
		&model.SetPromptPayload{PromptText: "do the thing"})))

	got := readNodeColumn(t, raw, "MTIX-1", "content_hash")
	want := model.ComputeContentHash("t", "", "do the thing", "", nil)
	assert.Equal(t, want, got, "set_prompt must recompute content_hash")
}

// TestApply_UpdateField_NonContent_HashUnchanged: a non-content field change
// (priority) leaves content_hash equal to the content-derived value (idempotent).
func TestApply_UpdateField_NonContent_HashUnchanged(t *testing.T) {
	s, raw := applyTestStore(t)
	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "alice", 1,
		&model.CreateNodePayload{Title: "t"})))
	before := readNodeColumn(t, raw, "MTIX-1", "content_hash")

	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpUpdateField, "MTIX-1", "alice", 2,
		&model.UpdateFieldPayload{FieldName: "priority", NewValue: json.RawMessage(`5`)})))

	assert.Equal(t, before, readNodeColumn(t, raw, "MTIX-1", "content_hash"),
		"a non-content change must not alter content_hash")
}
