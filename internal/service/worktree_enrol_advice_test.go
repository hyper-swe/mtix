// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A row the hub already holds but the bounded clone lacks can still differ in a
// field. The advice names that field and the report keeps it (MTIX-112.17.16.18,
// ruling r3 point 2).
func TestWorktreeBoardError_HeldRowWithAChangedField_NamesTheField(t *testing.T) {
	local := `{"nodes":[{"id":"TEST-1","title":"mine","description":"mine too"},{"id":"TEST-2"}]}`
	bounded := `{"nodes":[{"id":"TEST-2"}]}`
	full := `{"nodes":[{"id":"TEST-1","title":"hub","description":"hub too"},{"id":"TEST-2"}]}`
	e := confirmed(t, local, bounded, full)
	msg := e.Error()
	assert.Contains(t, msg, "the hub already holds TEST-1")
	assert.Contains(t, msg, "differs there in TEST-1: description, title")
	assert.Equal(t, map[string][]string{"TEST-1": {"description", "title"}}, e.HeldFields())
}

func TestWorktreeBoardError_HeldRowWithNoChangedField_SaysItHoldsTheSame(t *testing.T) {
	local := `{"nodes":[{"id":"TEST-1","title":"same"},{"id":"TEST-2"}]}`
	bounded := `{"nodes":[{"id":"TEST-2"}]}`
	e := confirmed(t, local, bounded, local)
	msg := e.Error()
	assert.Contains(t, msg, "the hub already holds TEST-1")
	assert.Contains(t, msg, "with the same values")
	assert.NotContains(t, msg, "differs there in")
	assert.Empty(t, e.HeldFields())
}

// The digest follows a held field's values, so a value the operator did not
// read cannot be accepted under an old digest.
func TestWorktreeBoardError_HeldFieldValue_ChangesTheDigest(t *testing.T) {
	local := `{"nodes":[{"id":"TEST-1","title":"mine"},{"id":"TEST-2"}]}`
	bounded := `{"nodes":[{"id":"TEST-2"}]}`
	one := confirmed(t, local, bounded, `{"nodes":[{"id":"TEST-1","title":"hub one"},{"id":"TEST-2"}]}`)
	two := confirmed(t, local, bounded, `{"nodes":[{"id":"TEST-1","title":"hub two"},{"id":"TEST-2"}]}`)
	again := confirmed(t, local, bounded, `{"nodes":[{"id":"TEST-1","title":"hub one"},{"id":"TEST-2"}]}`)
	assert.NotEqual(t, one.Digest(), two.Digest())
	assert.Equal(t, one.Digest(), again.Digest())
}

// The dependency advice must not tell the operator to run a command the store
// refuses: the link exists in this store, so mtix dep add says already exists.
func TestWorktreeBoardError_StoreOnlyDependency_DoesNotPrintAnAddThatIsRefused(t *testing.T) {
	local := `{"nodes":[{"id":"TEST-1"},{"id":"TEST-2"}],"dependencies":[{"from_id":"TEST-1","to_id":"TEST-2","dep_type":"blocks"}]}`
	clone := `{"nodes":[{"id":"TEST-1"},{"id":"TEST-2"}]}`
	msg := confirmed(t, local, clone, clone).Error()
	assert.Contains(t, msg, "dependency row(s) from TEST-1")
	assert.Contains(t, msg, "mtix dep add is refused while this store holds the link")
	assert.NotContains(t, msg, "add each again with mtix dep add")
	assert.Contains(t, msg, "then add the link again with mtix dep add")
	assert.NotContains(t, msg, "a row that is created again", "no node row is created again here")
}

func TestWorktreeBoardError_StoreOnlyNode_KeepsTheCreateClause(t *testing.T) {
	msg := confirmed(t, `{"nodes":[{"id":"TEST-1"},{"id":"TEST-4"}]}`, `{"nodes":[{"id":"TEST-1"}]}`, `{"nodes":[{"id":"TEST-1"}]}`).Error()
	assert.Contains(t, msg, "a row that is created again")
}

func TestWorktreeBoardError_OnlyHeldRows_DoNotPrintTheCreatedAgainClause(t *testing.T) {
	local := `{"nodes":[{"id":"TEST-1"},{"id":"TEST-2"}]}`
	bounded := `{"nodes":[{"id":"TEST-1"}]}`
	e := confirmed(t, local, bounded, local)
	require.NotNil(t, e)
	assert.NotContains(t, e.Error(), "a row that is created again")
}
