// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// GetAllTransitions() Tests
// ============================================================================

// TestGetAllTransitions_ReturnsAllValidTransitions verifies the function
// returns all valid transitions with non-empty From and To fields.
func TestGetAllTransitions_ReturnsAllValidTransitions(t *testing.T) {
	transitions := GetAllTransitions()

	// Must return a non-empty slice.
	require.NotEmpty(t, transitions, "GetAllTransitions should return non-empty slice")

	// Expected count: manually verify the validTransitions map.
	// open: 5, in_progress: 6, blocked: 4, done: 2, deferred: 4, cancelled: 2, invalidated: 4
	// Total: 5+6+4+2+4+2+4 = 27
	expectedCount := 27
	assert.Len(t, transitions, expectedCount,
		"should return all %d valid transitions", expectedCount)

	// Verify all entries have non-empty From and To.
	for i, ti := range transitions {
		assert.NotEmpty(t, ti.From, "transition[%d].From should not be empty", i)
		assert.NotEmpty(t, ti.To, "transition[%d].To should not be empty", i)
		// Ensure it's a valid status.
		assert.True(t, isValidStatus(ti.From),
			"transition[%d].From=%q should be valid status", i, ti.From)
		assert.True(t, isValidStatus(ti.To),
			"transition[%d].To=%q should be valid status", i, ti.To)
	}
}

// TestGetAllTransitions_IncludesKnownTransition verifies specific transitions exist.
func TestGetAllTransitions_IncludesKnownTransition(t *testing.T) {
	transitions := GetAllTransitions()

	// Verify open→in_progress exists.
	found := false
	for _, ti := range transitions {
		if ti.From == StatusOpen && ti.To == StatusInProgress {
			found = true
			assert.Equal(t, ConstraintNone, ti.Constraint,
				"open→in_progress should have no constraint")
			break
		}
	}
	assert.True(t, found, "open→in_progress transition should be in the result")

	// Verify done→open with ConstraintRequiresReopen exists.
	found = false
	for _, ti := range transitions {
		if ti.From == StatusDone && ti.To == StatusOpen {
			found = true
			assert.Equal(t, ConstraintRequiresReopen, ti.Constraint,
				"done→open should have ConstraintRequiresReopen")
			break
		}
	}
	assert.True(t, found, "done→open with reopen constraint should be in the result")

	// Verify in_progress→open with ConstraintRequiresUnclaim exists.
	found = false
	for _, ti := range transitions {
		if ti.From == StatusInProgress && ti.To == StatusOpen {
			found = true
			assert.Equal(t, ConstraintRequiresUnclaim, ti.Constraint,
				"in_progress→open should have ConstraintRequiresUnclaim")
			break
		}
	}
	assert.True(t, found, "in_progress→open with unclaim constraint should be in the result")
}

// TestGetAllTransitions_ConstraintValues verifies constraints are correctly populated.
func TestGetAllTransitions_ConstraintValues(t *testing.T) {
	transitions := GetAllTransitions()

	// Count transitions by constraint type.
	constraintCounts := make(map[TransitionConstraint]int)
	for _, ti := range transitions {
		constraintCounts[ti.Constraint]++
	}

	// Verify we have transitions with each constraint type used in validTransitions.
	assert.Greater(t, constraintCounts[ConstraintNone], 0,
		"should have transitions with ConstraintNone")
	assert.Greater(t, constraintCounts[ConstraintAutoOnly], 0,
		"should have transitions with ConstraintAutoOnly")
	assert.Greater(t, constraintCounts[ConstraintRequiresReopen], 0,
		"should have transitions with ConstraintRequiresReopen")
	assert.Greater(t, constraintCounts[ConstraintRequiresUnclaim], 0,
		"should have transitions with ConstraintRequiresUnclaim")
	assert.Greater(t, constraintCounts[ConstraintRequiresClaim], 0,
		"should have transitions with ConstraintRequiresClaim")
	assert.Greater(t, constraintCounts[ConstraintRequiresRestore], 0,
		"should have transitions with ConstraintRequiresRestore")
}

// ============================================================================
// NodeTypeForDepth() Tests
// ============================================================================

// TestNodeTypeForDepth_AllDepths verifies depth→type mapping per FR-1.2.
func TestNodeTypeForDepth_AllDepths(t *testing.T) {
	tests := []struct {
		depth    int
		expected NodeType
	}{
		{0, NodeTypeEpic},
		{1, NodeTypeStory},
		{2, NodeTypeIssue},
		{3, NodeTypeMicro},
		{4, NodeTypeMicro},
		{10, NodeTypeMicro},
		{50, NodeTypeMicro},
		{100, NodeTypeMicro},
		{1000, NodeTypeMicro},
		// Negative depths should also map to micro.
		{-1, NodeTypeMicro},
		{-10, NodeTypeMicro},
	}

	for _, tt := range tests {
		t.Run(string(tt.expected)+"_depth_"+string(rune('0'+byte(tt.depth)%10)), func(t *testing.T) {
			result := NodeTypeForDepth(tt.depth)
			assert.Equal(t, tt.expected, result,
				"NodeTypeForDepth(%d) should return %q", tt.depth, tt.expected)
		})
	}
}

// TestNodeTypeForDepth_Boundaries verifies edge cases.
func TestNodeTypeForDepth_Boundaries(t *testing.T) {
	// Exactly at boundaries.
	assert.Equal(t, NodeTypeEpic, NodeTypeForDepth(0))
	assert.Equal(t, NodeTypeStory, NodeTypeForDepth(1))
	assert.Equal(t, NodeTypeIssue, NodeTypeForDepth(2))

	// Just after boundaries.
	assert.Equal(t, NodeTypeMicro, NodeTypeForDepth(3))
	assert.Equal(t, NodeTypeMicro, NodeTypeForDepth(4))

	// Far beyond.
	assert.Equal(t, NodeTypeMicro, NodeTypeForDepth(999))
}

// ============================================================================
// TransitionConstraintFor() Tests
// ============================================================================

// TestTransitionConstraintFor_ValidTransition_ReturnsConstraint verifies
// known constraints are returned correctly.
func TestTransitionConstraintFor_ValidTransition_ReturnsConstraint(t *testing.T) {
	tests := []struct {
		name       string
		from       Status
		to         Status
		constraint TransitionConstraint
	}{
		// No constraint cases.
		{"open_to_inprogress", StatusOpen, StatusInProgress, ConstraintNone},
		{"open_to_deferred", StatusOpen, StatusDeferred, ConstraintNone},
		{"blocked_to_cancelled", StatusBlocked, StatusCancelled, ConstraintNone},

		// Auto-only constraint cases.
		{"open_to_blocked_auto", StatusOpen, StatusBlocked, ConstraintAutoOnly},
		{"inprogress_to_blocked_auto", StatusInProgress, StatusBlocked, ConstraintAutoOnly},
		{"blocked_to_open_auto", StatusBlocked, StatusOpen, ConstraintAutoOnly},
		{"blocked_to_inprogress_auto", StatusBlocked, StatusInProgress, ConstraintAutoOnly},

		// Requires reopen constraint.
		{"done_to_open_reopen", StatusDone, StatusOpen, ConstraintRequiresReopen},
		{"cancelled_to_open_reopen", StatusCancelled, StatusOpen, ConstraintRequiresReopen},

		// Requires unclaim constraint.
		{"inprogress_to_open_unclaim", StatusInProgress, StatusOpen, ConstraintRequiresUnclaim},

		// Requires claim constraint.
		{"deferred_to_inprogress_claim", StatusDeferred, StatusInProgress, ConstraintRequiresClaim},

		// Requires restore constraint.
		{"invalidated_to_open_restore", StatusInvalidated, StatusOpen, ConstraintRequiresRestore},
		{"invalidated_to_inprogress_restore", StatusInvalidated, StatusInProgress, ConstraintRequiresRestore},
		{"invalidated_to_deferred_restore", StatusInvalidated, StatusDeferred, ConstraintRequiresRestore},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := TransitionConstraintFor(tt.from, tt.to)
			assert.Equal(t, tt.constraint, result,
				"TransitionConstraintFor(%q, %q) should return %q",
				tt.from, tt.to, tt.constraint)
		})
	}
}

// TestTransitionConstraintFor_InvalidTransition_ReturnsNone verifies
// non-existent transitions return ConstraintNone.
func TestTransitionConstraintFor_InvalidTransition_ReturnsNone(t *testing.T) {
	tests := []struct {
		name string
		from Status
		to   Status
	}{
		// Invalid transitions that definitely don't exist.
		{"open_to_done", StatusOpen, StatusDone},
		{"done_to_inprogress", StatusDone, StatusInProgress},
		{"deferred_to_blocked", StatusDeferred, StatusBlocked},
		{"cancelled_to_inprogress", StatusCancelled, StatusInProgress},
		{"blocked_to_deferred", StatusBlocked, StatusDeferred},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := TransitionConstraintFor(tt.from, tt.to)
			assert.Equal(t, ConstraintNone, result,
				"TransitionConstraintFor(%q, %q) should return ConstraintNone for invalid transition",
				tt.from, tt.to)
		})
	}
}

// TestTransitionConstraintFor_InvalidFromStatus_ReturnsNone verifies
// unknown source status returns ConstraintNone.
func TestTransitionConstraintFor_InvalidFromStatus_ReturnsNone(t *testing.T) {
	// Create an invalid status by directly using a string that doesn't exist.
	invalidStatus := Status("nonexistent_status_xyz")

	result := TransitionConstraintFor(invalidStatus, StatusOpen)
	assert.Equal(t, ConstraintNone, result,
		"TransitionConstraintFor with invalid source status should return ConstraintNone")
}

// TestTransitionConstraintFor_SameStatus_ReturnsNone verifies same-status
// transitions return ConstraintNone (they're idempotent, handled elsewhere).
func TestTransitionConstraintFor_SameStatus_ReturnsNone(t *testing.T) {
	for _, status := range AllStatuses() {
		result := TransitionConstraintFor(status, status)
		assert.Equal(t, ConstraintNone, result,
			"TransitionConstraintFor(%q, %q) should return ConstraintNone for idempotent",
			status, status)
	}
}

// ============================================================================
// ActivityEntry.Validate() Tests
// ============================================================================

// TestActivityEntry_Validate_InvalidType_ReturnsError verifies that an
// invalid activity type is rejected.
func TestActivityEntry_Validate_InvalidType_ReturnsError(t *testing.T) {
	entry := &ActivityEntry{
		ID:     "01HQ4Z5V2ABCDEF123456789",
		Type:   ActivityType("garbage_type"),
		Author: "agent-1",
		Text:   "some text",
	}

	err := entry.Validate()
	require.Error(t, err, "validate should fail for invalid activity type")
	assert.ErrorIs(t, err, ErrInvalidInput)
	assert.Contains(t, err.Error(), "invalid activity type")
}

// TestActivityEntry_Validate_InvalidTypeEmptyString_ReturnsError verifies
// that an empty type string is rejected.
func TestActivityEntry_Validate_InvalidTypeEmptyString_ReturnsError(t *testing.T) {
	entry := &ActivityEntry{
		ID:     "01HQ4Z5V2ABCDEF123456789",
		Type:   ActivityType(""),
		Author: "agent-1",
		Text:   "",
	}

	err := entry.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidInput)
	assert.Contains(t, err.Error(), "invalid activity type")
}

// TestActivityEntry_Validate_RequiresTextForComment_ReturnsError verifies
// that a comment type with empty text is rejected.
func TestActivityEntry_Validate_RequiresTextForComment_ReturnsError(t *testing.T) {
	entry := &ActivityEntry{
		ID:     "01HQ4Z5V2ABCDEF123456789",
		Type:   ActivityTypeComment,
		Author: "agent-1",
		Text:   "", // Empty text for a type that requires it
	}

	err := entry.Validate()
	require.Error(t, err, "validate should fail when comment has no text")
	assert.ErrorIs(t, err, ErrInvalidInput)
	assert.Contains(t, err.Error(), "text is required")
}

// TestActivityEntry_Validate_RequiresTextForNote_ReturnsError verifies
// that a note type with empty text is rejected.
func TestActivityEntry_Validate_RequiresTextForNote_ReturnsError(t *testing.T) {
	entry := &ActivityEntry{
		ID:     "01HQ4Z5V2ABCDEF123456789",
		Type:   ActivityTypeNote,
		Author: "agent-1",
		Text:   "",
	}

	err := entry.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidInput)
	assert.Contains(t, err.Error(), "text is required")
}

// TestActivityEntry_Validate_RequiresTextForUnclaim_ReturnsError verifies
// that an unclaim type with empty text is rejected.
func TestActivityEntry_Validate_RequiresTextForUnclaim_ReturnsError(t *testing.T) {
	entry := &ActivityEntry{
		ID:     "01HQ4Z5V2ABCDEF123456789",
		Type:   ActivityTypeUnclaim,
		Author: "agent-1",
		Text:   "",
	}

	err := entry.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidInput)
	assert.Contains(t, err.Error(), "text is required")
}

// TestActivityEntry_Validate_OptionalTextTypes_PassWithoutText verifies
// that types not requiring text pass validation with empty text.
func TestActivityEntry_Validate_OptionalTextTypes_PassWithoutText(t *testing.T) {
	optionalTextTypes := []ActivityType{
		ActivityTypeStatusChange,
		ActivityTypeClaim,
		ActivityTypeSystem,
		ActivityTypePromptEdit,
		ActivityTypeCreated,
		ActivityTypeAnnotation,
	}

	for _, at := range optionalTextTypes {
		t.Run(string(at), func(t *testing.T) {
			entry := &ActivityEntry{
				ID:     "01HQ4Z5V2ABCDEF123456789",
				Type:   at,
				Author: "agent-1",
				Text:   "", // Empty text should be OK for these types
			}

			err := entry.Validate()
			assert.NoError(t, err,
				"activity type %q should validate with empty text", at)
		})
	}
}

// ============================================================================
// ComputeContentHash() Tests
// ============================================================================

// TestComputeContentHash_NilLabels_Succeeds verifies that nil labels
// do not cause errors and produce a valid hash.
func TestComputeContentHash_NilLabels_Succeeds(t *testing.T) {
	hash := ComputeContentHash(
		"Test Title",
		"Test Description",
		"Test Prompt",
		"Test Acceptance",
		nil, // Nil labels
	)

	// Verify it's a valid SHA256 hash (64 hex chars).
	assert.Len(t, hash, 64, "hash should be 64 hex characters")
	// Verify it's valid hex.
	for _, c := range hash {
		assert.True(t,
			(c >= '0' && c <= '9') || (c >= 'a' && c <= 'f'),
			"hash should contain only valid hex characters")
	}
}

// TestComputeContentHash_EmptyLabels_SameAsNil verifies that empty slice
// and nil produce the same hash.
func TestComputeContentHash_EmptyLabels_SameAsNil(t *testing.T) {
	content := struct {
		title      string
		desc       string
		prompt     string
		acceptance string
	}{
		"Title",
		"Description",
		"Prompt",
		"Acceptance",
	}

	hashNil := ComputeContentHash(
		content.title,
		content.desc,
		content.prompt,
		content.acceptance,
		nil,
	)

	hashEmpty := ComputeContentHash(
		content.title,
		content.desc,
		content.prompt,
		content.acceptance,
		[]string{},
	)

	assert.Equal(t, hashNil, hashEmpty,
		"nil and empty label slices should produce identical hashes")
}

// TestComputeContentHash_SingleLabel_ValidHash verifies a single label
// produces a valid hash.
func TestComputeContentHash_SingleLabel_ValidHash(t *testing.T) {
	hash := ComputeContentHash(
		"Title",
		"Description",
		"Prompt",
		"Acceptance",
		[]string{"label1"},
	)

	assert.Len(t, hash, 64)
	assert.NotEmpty(t, hash)
}

// TestComputeContentHash_ManyLabels_ValidHash verifies many labels work.
func TestComputeContentHash_ManyLabels_ValidHash(t *testing.T) {
	labels := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"}
	hash := ComputeContentHash("Title", "Desc", "Prompt", "Acceptance", labels)

	assert.Len(t, hash, 64)
	assert.NotEmpty(t, hash)
}

// TestComputeContentHash_EmptyAllFields_ValidHash verifies all-empty fields
// still produce a valid hash.
func TestComputeContentHash_EmptyAllFields_ValidHash(t *testing.T) {
	hash := ComputeContentHash("", "", "", "", nil)

	assert.Len(t, hash, 64)
	assert.NotEmpty(t, hash)
}

// TestComputeContentHash_LongContent_ValidHash verifies large content works.
func TestComputeContentHash_LongContent_ValidHash(t *testing.T) {
	longText := string(make([]byte, 10000)) // 10KB of zeros
	for i := range longText {
		if i%2 == 0 {
			longText = longText[:i] + "a" + longText[i+1:]
		}
	}

	hash := ComputeContentHash(longText, longText, longText, longText, nil)

	assert.Len(t, hash, 64)
	assert.NotEmpty(t, hash)
}

// TestComputeContentHash_Deterministic_MultipleCallsSameHash verifies
// determinism with all-empty fields.
func TestComputeContentHash_Deterministic_MultipleCallsSameHash(t *testing.T) {
	h1 := ComputeContentHash("", "", "", "", nil)
	h2 := ComputeContentHash("", "", "", "", nil)
	h3 := ComputeContentHash("", "", "", "", []string{})

	assert.Equal(t, h1, h2, "identical calls should produce same hash")
	assert.Equal(t, h2, h3, "nil and empty slices should produce same hash")
}

// ============================================================================
// Helper Functions
// ============================================================================

// isValidStatus returns true if the status exists in AllStatuses.
func isValidStatus(s Status) bool {
	for _, st := range AllStatuses() {
		if st == s {
			return true
		}
	}
	return false
}
