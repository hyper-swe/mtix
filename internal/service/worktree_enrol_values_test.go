// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
)

func titled(a, b string) (string, string) {
	return `{"nodes":[{"id":"TEST-1","title":"` + a + `"}]}`, `{"nodes":[{"id":"TEST-1","title":"` + b + `"}]}`
}

// TestBoardHubFinding_DigestBindsTheValuesNotJustTheNames: changing a differing
// value changes the digest, so an accept of the report the operator read cannot
// carry over a value they did not read (ruling r5 point 6).
func TestBoardHubFinding_DigestBindsTheValuesNotJustTheNames(t *testing.T) {
	l1, c1 := titled("branch title", "hub title")
	l2, _ := titled("another branch title", "hub title")
	_, c3 := titled("branch title", "another hub title")

	f1, err := service.BoardHubFinding([]byte(l1), []byte(c1))
	require.NoError(t, err)
	same, err := service.BoardHubFinding([]byte(l1), []byte(c1))
	require.NoError(t, err)
	local, err := service.BoardHubFinding([]byte(l2), []byte(c1))
	require.NoError(t, err)
	hub, err := service.BoardHubFinding([]byte(l1), []byte(c3))
	require.NoError(t, err)

	assert.Equal(t, f1.Digest(), same.Digest(), "the same difference has one digest")
	assert.NotEqual(t, f1.Digest(), local.Digest(), "a changed local value changes the digest")
	assert.NotEqual(t, f1.Digest(), hub.Digest(), "a changed hub value changes the digest")
	assert.Equal(t, f1.Diff, local.Diff, "the ids and field names are the same")
}

func TestBoardHubFinding_ReportCarriesNoValues(t *testing.T) {
	l, c := titled("SECRET-LOCAL", "SECRET-HUB")
	f, err := service.BoardHubFinding([]byte(l), []byte(c))
	require.NoError(t, err)
	body, err := json.Marshal(f.Diff)
	require.NoError(t, err)
	assert.NotContains(t, string(body)+f.Digest(), "SECRET")
}

func TestBoardHubFinding_HubRowsOnly_ShapeIsClassified(t *testing.T) {
	one := `{"nodes":[{"id":"TEST-1","title":"a"}]}`
	two := `{"nodes":[{"id":"TEST-1","title":"a"},{"id":"TEST-2","title":"b"}]}`
	other := `{"nodes":[{"id":"TEST-1","title":"a"},{"id":"TEST-3","title":"c"}]}`
	edited := `{"nodes":[{"id":"TEST-1","title":"edited"},{"id":"TEST-2","title":"b"}]}`
	tests := []struct {
		name        string
		local, hub  string
		hubRowsOnly bool
	}{
		{"the store lacks a hub row", one, two, true},
		{"the store holds a row the hub lacks", two, one, false},
		{"both sides hold a row the other lacks", other, two, false},
		{"a field differs and a hub row is missing", one, edited, false},
		{"a field differs", `{"nodes":[{"id":"TEST-1","title":"x"}]}`, one, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, err := service.BoardHubFinding([]byte(tt.local), []byte(tt.hub))
			require.NoError(t, err)
			require.NotNil(t, f)
			assert.Equal(t, tt.hubRowsOnly, f.HubRowsOnly())
		})
	}
	file, err := service.BoardFileFinding([]byte(one), []byte(two))
	require.NoError(t, err)
	assert.False(t, file.HubRowsOnly(), "a file difference is never a hub-rows-only difference")
}

func board(t *testing.T, local, hub string) *service.WorktreeBoardError {
	t.Helper()
	f, err := service.BoardHubFinding([]byte(local), []byte(hub))
	require.NoError(t, err)
	require.NotNil(t, f)
	return &service.WorktreeBoardError{BoardFinding: *f, Report: "/w/.mtix/enroll-diff-x.json"}
}

// confirmed builds a refusal and checks its rows against the full hub clone.
func confirmed(t *testing.T, local, bounded, full string) *service.WorktreeBoardError {
	t.Helper()
	f, err := service.BoardHubFinding([]byte(local), []byte(bounded))
	require.NoError(t, err)
	require.NotNil(t, f)
	require.NoError(t, f.ConfirmAgainstHub([]byte(local), []byte(full)))
	return &service.WorktreeBoardError{BoardFinding: *f, Report: "/w/.mtix/enroll-diff-x.json"}
}

// TestWorktreeBoardError_LocalDifference_NeverPrintsAPullAsTheFix: the printed
// fixes come in the order keep a copy, re-enter as writes, accept; a pull and
// sync --fix are named only to say not to run them, and push is named only
// after the change has been re-entered (ruling r5 points 1, 2 and 3).
func TestWorktreeBoardError_LocalDifference_NeverPrintsAPullAsTheFix(t *testing.T) {
	l, c := titled("branch title", "hub title")
	msg := board(t, l, c).Error()

	assert.NotContains(t, msg, "fix: run mtix sync pull")
	assert.NotContains(t, msg, "then mtix sync --fix")
	assert.Contains(t, msg, "Do not run mtix sync pull")
	assert.Contains(t, msg, "overwrites a change that is not journaled")
	copyAt := strings.Index(msg, "(1) keep a copy: mtix backup '/w/.mtix/enrol-copy-x.db'")
	reenter := strings.Index(msg, "(2) re-enter each change")
	accept := strings.Index(msg, "(3) or, if the difference is meant to be dropped, enroll again with accept-hub ")
	require.Positive(t, copyAt)
	assert.Less(t, copyAt, reenter)
	assert.Less(t, reenter, accept)
	assert.Less(t, strings.Index(msg, "(2)"), strings.Index(msg, "mtix sync push"), "push comes only after the change is re-entered")
	assert.Contains(t, msg, board(t, l, c).Digest())
}

func TestWorktreeBoardError_StoreOnlyRow_IsALocalDifference(t *testing.T) {
	msg := board(t, `{"nodes":[{"id":"TEST-1"},{"id":"TEST-2"}]}`, `{"nodes":[{"id":"TEST-1"}]}`).Error()
	assert.NotContains(t, msg, "fix: run mtix sync pull")
	assert.Contains(t, msg, "(1) keep a copy")
}

func TestWorktreeBoardError_HubRowsOnly_PrintsThePullThenSyncFix(t *testing.T) {
	msg := board(t, `{"nodes":[{"id":"TEST-1"}]}`, `{"nodes":[{"id":"TEST-1"},{"id":"TEST-2"}]}`).Error()
	assert.Contains(t, msg, "run mtix sync pull, then mtix sync --fix")
	assert.NotContains(t, msg, "mtix sync push", "nothing is pending, so push cannot act and is not printed")
	assert.NotContains(t, msg, "Do not run mtix sync pull")
}

// TestBoardSnapshotHubFinding: a store from a tasks.json snapshot is compared
// with the full hub clone; rows only the hub holds are not a difference (the
// enrolment replaces the store by that clone), everything else is (point 8).
func TestBoardSnapshotHubFinding(t *testing.T) {
	one := `{"nodes":[{"id":"TEST-1","title":"a"}]}`
	two := `{"nodes":[{"id":"TEST-1","title":"a"},{"id":"TEST-2","title":"b"}]}`
	edited := `{"nodes":[{"id":"TEST-1","title":"edited"}]}`
	tests := []struct {
		name       string
		local, hub string
		wantNil    bool
	}{
		{"equal", one, one, true},
		{"the hub holds more rows", one, two, true},
		{"the store holds a row the hub lacks", two, one, false},
		{"a field differs", edited, one, false},
		{"a field differs and the hub holds more rows", edited, two, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, err := service.BoardSnapshotHubFinding([]byte(tt.local), []byte(tt.hub))
			require.NoError(t, err)
			assert.Equal(t, tt.wantNil, f == nil)
			if f != nil {
				assert.False(t, f.HubRowsOnly(), "what remains is local")
			}
		})
	}
	mixed, err := service.BoardSnapshotHubFinding([]byte(edited), []byte(two))
	require.NoError(t, err)
	assert.Equal(t, service.BoardDiff{"TEST-1": {"title"}}, mixed.Diff, "a hub row is not reported in snapshot mode")
	_, err = service.BoardSnapshotHubFinding([]byte("{bad"), []byte(one))
	require.Error(t, err)
}

const histRow = `{"id":"%s","task_uid":"u1","ts":"%s","actor":"%s","origin":"%s","kind":"change","field":"title","new":"\"x\"","event_id":"%s"}`

func historyDoc(rows ...string) string {
	return `{"nodes":[{"id":"TEST-1","uid":"u1","history":[` + strings.Join(rows, ",") + `]}]}`
}

func row(id, ts, actor, origin, event string) string {
	out := histRow
	for _, v := range []string{id, ts, actor, origin, event} {
		out = strings.Replace(out, "%s", v, 1)
	}
	return out
}

// TestCompareBoardDocs_History_NormalizesOnlyWhatACloneRewrites: a clone writes
// its own row id, clock reading, origin, event id and (for a comment) actor, in
// its own order; those do not differ. A row the other side lacks does, in both
// directions, and so does a changed value (ruling r5 point 4).
func TestCompareBoardDocs_History_NormalizesOnlyWhatACloneRewrites(t *testing.T) {
	a1, a2 := row("h1", "t1", "cli", "local", ""), strings.Replace(row("h2", "t2", "cli", "local", ""), `"field":"title"`, `"field":"status"`, 1)
	b1, b2 := row("g9", "t9", "lane", "hub", "e1"), strings.Replace(row("g8", "t8", "lane", "hub", "e2"), `"field":"title"`, `"field":"status"`, 1)

	same, err := service.CompareBoardDocs([]byte(historyDoc(a1, a2)), []byte(historyDoc(b2, b1)))
	require.NoError(t, err)
	assert.Empty(t, same, "rewritten ids, clocks, origins, actors and order are not differences")

	storeOnly, err := service.CompareBoardDocs([]byte(historyDoc(a1, a2)), []byte(historyDoc(b1)))
	require.NoError(t, err)
	assert.Equal(t, service.BoardDiff{"TEST-1": {"history"}}, storeOnly)
	hubOnly, err := service.CompareBoardDocs([]byte(historyDoc(a1)), []byte(historyDoc(b1, b2)))
	require.NoError(t, err)
	assert.Equal(t, service.BoardDiff{"TEST-1": {"history"}}, hubOnly)
	dup, err := service.CompareBoardDocs([]byte(historyDoc(a1, a1)), []byte(historyDoc(b1)))
	require.NoError(t, err)
	assert.Equal(t, service.BoardDiff{"TEST-1": {"history"}}, dup, "a repeated row is a row the other side lacks")
	changed, err := service.CompareBoardDocs([]byte(historyDoc(a1)), []byte(historyDoc(strings.Replace(b1, `\"x\"`, `\"y\"`, 1))))
	require.NoError(t, err)
	assert.Equal(t, service.BoardDiff{"TEST-1": {"history"}}, changed, "a changed value is a difference")
}

func TestCompareBoardDocs_TopLevelHistory_StoreOnlyRowsCount(t *testing.T) {
	r := row("h1", "t1", "cli", "local", "")
	with := `{"nodes":[],"history":[` + r + `]}`
	without := `{"nodes":[]}`
	diff, err := service.CompareBoardDocs([]byte(with), []byte(without))
	require.NoError(t, err)
	assert.NotEmpty(t, diff, "an orphan audit row only the store holds is a difference")
	same, err := service.CompareBoardDocs([]byte(with), []byte(`{"nodes":[],"history":[`+row("z", "t2", "x", "hub", "e")+`]}`))
	require.NoError(t, err)
	assert.Empty(t, same)
}

// TestBoardFileFinding_MtixVersionIsCompared: the file rule has no exclusions,
// and the build's version in the export is one of the fields it compares.
func TestBoardFileFinding_MtixVersionIsCompared(t *testing.T) {
	other := edit(t, `"mtix_version":"x"`, `"mtix_version":"y"`)
	f, err := service.BoardFileFinding([]byte(other), []byte(baseBoard))
	require.NoError(t, err)
	require.NotNil(t, f)
	assert.Equal(t, []string{"(export)"}, f.IDs())
	assert.Equal(t, []string{"mtix_version"}, f.Diff["(export)"])
}

// TestBoardFileFinding_HistoryIsComparedInFull: the file and the store are the
// same replica, so a history row's id, clock reading or actor is a difference.
func TestBoardFileFinding_HistoryIsComparedInFull(t *testing.T) {
	a := historyDoc(row("h1", "t1", "cli", "local", ""))
	for name, b := range map[string]string{
		"id":     historyDoc(row("h2", "t1", "cli", "local", "")),
		"ts":     historyDoc(row("h1", "t2", "cli", "local", "")),
		"actor":  historyDoc(row("h1", "t1", "lane", "local", "")),
		"origin": historyDoc(row("h1", "t1", "cli", "hub", "")),
	} {
		t.Run(name, func(t *testing.T) {
			f, err := service.BoardFileFinding([]byte(a), []byte(b))
			require.NoError(t, err)
			require.NotNil(t, f)
			assert.Equal(t, service.BoardDiff{"TEST-1": {"history"}}, f.Diff)
		})
	}
}
