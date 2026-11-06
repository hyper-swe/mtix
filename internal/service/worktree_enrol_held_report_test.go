// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
)

// heldFinding confirms a finding whose rows the hub already holds.
func heldFinding(t *testing.T, local, bounded, full string) *service.BoardFinding {
	t.Helper()
	f, err := service.BoardHubFinding([]byte(local), []byte(bounded))
	require.NoError(t, err)
	require.NotNil(t, f)
	require.NoError(t, f.ConfirmAgainstHub([]byte(local), []byte(full)))
	return f
}

// The written report lists, for a row the hub already holds, the fields in
// which the store differs (names only); step (2) sends the operator to it
// (MTIX-112.17.16.18, ruling r3 point 2, ruling r4 point 4).
func TestEnrolWorktree_HeldRowWithAChangedField_ReportListsHeldFields(t *testing.T) {
	w := newEnrolWorld(t)
	writeBoard(t, w, `{"nodes":[]}`)
	w.boardFinding = heldFinding(t,
		`{"nodes":[{"id":"TEST-1","title":"mine","description":"mine too"},{"id":"TEST-2"}]}`,
		`{"nodes":[{"id":"TEST-2"}]}`,
		`{"nodes":[{"id":"TEST-1","title":"hub","description":"hub too"},{"id":"TEST-2"}]}`)

	_, err := w.run()

	var refusal *service.WorktreeBoardError
	require.True(t, errors.As(err, &refusal))
	var report struct {
		Held map[string][]string `json:"held_fields"`
	}
	body, readErr := os.ReadFile(refusal.Report)
	require.NoError(t, readErr)
	require.NoError(t, json.Unmarshal(body, &report))
	assert.Equal(t, map[string][]string{"TEST-1": {"description", "title"}}, report.Held)
	assert.NotContains(t, string(body), "mine", "ids and field names only")
	assert.NotContains(t, string(body), "hub too")
}

// A node row and a dependency row held on one id name the node once.
func TestWorktreeBoardError_NodeAndDependencyHeldOnOneID_NamesItOnce(t *testing.T) {
	local := `{"nodes":[{"id":"TEST-1"},{"id":"TEST-2"}],"dependencies":[{"from_id":"TEST-1","to_id":"TEST-2","dep_type":"blocks"}]}`
	bounded := `{"nodes":[{"id":"TEST-2"}]}`
	msg := confirmed(t, local, bounded, local).Error()
	assert.Contains(t, msg, "the hub already holds TEST-1:")
	assert.NotContains(t, msg, "TEST-1, TEST-1")
	assert.Equal(t, 1, strings.Count(msg, "the hub already holds TEST-1"), msg)
}

// The named field list is bounded like the id list before it: ten, then a count.
func TestWorktreeBoardError_ManyHeldRows_BoundsTheNamedList(t *testing.T) {
	var local, full []string
	for i := 1; i <= 15; i++ {
		local = append(local, fmt.Sprintf(`{"id":"TEST-%02d","title":"mine"}`, i))
		full = append(full, fmt.Sprintf(`{"id":"TEST-%02d","title":"hub"}`, i))
	}
	doc := func(rows []string) string {
		return `{"nodes":[` + strings.Join(append([]string{`{"id":"TEST-99"}`}, rows...), ",") + `]}`
	}
	msg := confirmed(t, doc(local), `{"nodes":[{"id":"TEST-99"}]}`, doc(full)).Error()
	assert.Contains(t, msg, "TEST-10: title")
	assert.NotContains(t, msg, "TEST-11: title", "the named list stops at ten")
	assert.Contains(t, msg, "and 5 more")
}

// A whole-row name such as (dependencies) is not a field the value differs in.
func TestWorktreeBoardError_HeldNodeWithAStoreOnlyDependency_DoesNotNameTheRowAsAField(t *testing.T) {
	local := `{"nodes":[{"id":"TEST-1","title":"mine"},{"id":"TEST-2"}],"dependencies":[{"from_id":"TEST-1","to_id":"TEST-2","dep_type":"blocks"}]}`
	bounded := `{"nodes":[{"id":"TEST-2"}]}`
	full := `{"nodes":[{"id":"TEST-1","title":"hub"},{"id":"TEST-2"}],"dependencies":[]}`
	e := confirmed(t, local, bounded, full)
	assert.Equal(t, map[string][]string{"TEST-1": {"title"}}, e.HeldFields())
	assert.NotContains(t, e.Error(), "(dependencies)")
}

// Rows that hold different values and rows that hold the same ones are worded
// apart on one line.
func TestWorktreeBoardError_HeldRowsMixed_SaysWhichHoldTheSameValues(t *testing.T) {
	local := `{"nodes":[{"id":"TEST-1","title":"mine"},{"id":"TEST-3","title":"same"},{"id":"TEST-2"}]}`
	bounded := `{"nodes":[{"id":"TEST-2"}]}`
	full := `{"nodes":[{"id":"TEST-1","title":"hub"},{"id":"TEST-3","title":"same"},{"id":"TEST-2"}]}`
	msg := confirmed(t, local, bounded, full).Error()
	assert.Contains(t, msg, "the value differs there in TEST-1: title")
	assert.Contains(t, msg, "; TEST-3 hold(s) the same values")
	assert.NotContains(t, msg, "(with the same values;")
}
