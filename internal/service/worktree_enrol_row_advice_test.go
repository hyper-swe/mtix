// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
)

// Row advice words and the lost-value notice (MTIX-112.17.16.18, ruling r2).

func nodesDoc(ids ...string) string {
	rows := make([]string, len(ids))
	for i, id := range ids {
		rows[i] = `{"id":"` + id + `"}`
	}
	return `{"nodes":[` + strings.Join(rows, ",") + `]}`
}

// TestWorktreeBoardError_RowAdvice_NodeRowsSplitByWhatTheHubHolds: of two node
// rows the bounded clone lacks, the full clone holds one. Only the one the hub
// lacks is told to be created again; the other is told not to be.
func TestWorktreeBoardError_RowAdvice_NodeRowsSplitByWhatTheHubHolds(t *testing.T) {
	msg := confirmed(t, nodesDoc("TEST-1", "TEST-4", "TEST-5"), nodesDoc("TEST-1"), nodesDoc("TEST-1", "TEST-5")).Error()

	assert.Contains(t, msg, "the hub lacks TEST-4: create each again")
	assert.NotContains(t, msg, "the hub lacks TEST-4, TEST-5")
	assert.Contains(t, msg, "the hub already holds TEST-5: do not create or add them again")
	assert.NotContains(t, msg, "dependency row")
}

// TestWorktreeBoardError_RowAdvice_NotConfirmed_PrintsNoRowAdvice: until the
// rows are checked against the hub at its head, nothing is said about them.
func TestWorktreeBoardError_RowAdvice_NotConfirmed_PrintsNoRowAdvice(t *testing.T) {
	msg := board(t, nodesDoc("TEST-1", "TEST-4"), nodesDoc("TEST-1")).Error()

	assert.NotContains(t, msg, "the hub lacks")
	assert.NotContains(t, msg, "the hub already holds")
	assert.NotContains(t, msg, "mtix create")
}

// TestWorktreeBoardError_RowAdvice_DependencyRowsAreWordedApart: dependency
// rows from a node the hub holds name the dependency command, not the node.
func TestWorktreeBoardError_RowAdvice_DependencyRowsAreWordedApart(t *testing.T) {
	local, bounded := deps("blocks", "related", "blocks"), `{"nodes":[{"id":"TEST-1"}]}`

	lacking := confirmed(t, local, bounded, bounded).Error()
	held := confirmed(t, local, bounded, local).Error()

	assert.Contains(t, lacking, "the hub lacks dependency row(s) from TEST-1: add each again with mtix dep add")
	assert.NotContains(t, lacking, "the hub lacks TEST-1")
	assert.NotContains(t, lacking, "the hub already holds")
	assert.Contains(t, held, "the hub already holds TEST-1")
	assert.NotContains(t, held, "mtix dep add")
	assert.NotContains(t, held, "the hub lacks")
}

// TestWorktreeBoardError_RowAdvice_NodeAndDependencyKeepSeparateKinds: a store
// only and a hub only dependency under one key must keep the store-only kind
// (review F11); a changed field is never a row.
func TestWorktreeBoardError_RowAdvice_NodeAndDependencyKeepSeparateKinds(t *testing.T) {
	local := `{"nodes":[{"id":"TEST-1"}],"dependencies":[{"from_id":"TEST-1","to_id":"TEST-2","dep_type":"blocks"}]}`
	hub := `{"nodes":[{"id":"TEST-1"}],"dependencies":[{"from_id":"TEST-1","to_id":"TEST-3","dep_type":"blocks"}]}`
	f, err := service.BoardHubFinding([]byte(local), []byte(hub))
	require.NoError(t, err)

	assert.Equal(t, []string{"TEST-1"}, f.StoreOnlyRows(), "the store-only row survives a hub-only row under the same key")
	assert.False(t, f.HubRowsOnly())
	l, c := titled("a", "b")
	changed, err := service.BoardHubFinding([]byte(l), []byte(c))
	require.NoError(t, err)
	assert.Empty(t, changed.StoreOnlyRows(), "a changed field is not a row")
	hubOnly, err := service.BoardHubFinding([]byte(nodesDoc("TEST-1")), []byte(nodesDoc("TEST-1", "TEST-2")))
	require.NoError(t, err)
	assert.Empty(t, hubOnly.StoreOnlyRows(), "a hub-only row is not a store-only row")
}

// TestWorktreeBoardError_RowAdvice_BoundsTheList: more than ten ids are cut with
// a count.
func TestWorktreeBoardError_RowAdvice_BoundsTheList(t *testing.T) {
	var ids []string
	for i := 1; i <= 12; i++ {
		ids = append(ids, fmt.Sprintf("TEST-%02d", i))
	}
	msg := confirmed(t, nodesDoc(ids...), nodesDoc(), nodesDoc()).Error()

	assert.Contains(t, msg, "TEST-10 and 2 more")
	assert.NotContains(t, msg, "TEST-11,")
	eleven := confirmed(t, nodesDoc(ids[:11]...), nodesDoc(), nodesDoc()).Error()
	assert.Contains(t, eleven, "TEST-10 and 1 more", "the boundary: eleven ids are cut at ten")
	ten := confirmed(t, nodesDoc(ids[:10]...), nodesDoc(), nodesDoc()).Error()
	assert.NotContains(t, ten, " more")
	assert.Contains(t, ten, "TEST-10")
}

// TestWorktreeBoardError_ReEnterStep_SaysAHeldValueCannotBeReEntered (review F10).
func TestWorktreeBoardError_ReEnterStep_SaysAHeldValueCannotBeReEntered(t *testing.T) {
	l, c := titled("branch", "hub")
	msg := board(t, l, c).Error()

	assert.Contains(t, msg, "a value the store already holds cannot be re-entered")
	assert.Contains(t, msg, "step (3) is the way for those")
}

// TestWorktreeBoardError_ReportPath_ControlCharactersAreNotPrinted (review F12).
func TestWorktreeBoardError_ReportPath_ControlCharactersAreNotPrinted(t *testing.T) {
	l, c := titled("branch", "hub")
	f, err := service.BoardHubFinding([]byte(l), []byte(c))
	require.NoError(t, err)
	msg := (&service.WorktreeBoardError{BoardFinding: *f, Report: "/w\x1b[31m/.mtix/enroll-diff-x.json"}).Error()

	assert.NotContains(t, printedLine(t, msg, "no content): "), "\x1b", "the displayed path; the quoted command keeps the literal path")
}

func lostResult() *service.WorktreeEnrolResult {
	return &service.WorktreeEnrolResult{AsideDir: "/w/.mtix/data.pre-x", BackupPath: "/w/.mtix/backups/b.db", LostLocalReport: "/w/.mtix/enroll-lost-local-x.json",
		LostLocal: &service.LostLocal{Diff: service.BoardDiff{"TEST-1": {"prompt", "title"}}}}
}

// TestWorktreeResult_Notice_SaysOnlyWhatIsKnown: it names the ids, the fields,
// the old store, the backup and the report, and does not claim the old store
// holds the operator's value (review F3).
func TestWorktreeResult_Notice_SaysOnlyWhatIsKnown(t *testing.T) {
	n := lostResult().Notice()

	assert.Contains(t, n, "TEST-1 (prompt, title)")
	assert.Contains(t, n, "/w/.mtix/data.pre-x")
	assert.Contains(t, n, "/w/.mtix/backups/b.db")
	assert.Contains(t, n, "/w/.mtix/enroll-lost-local-x.json")
	assert.Contains(t, n, "which may be your value or an earlier hub value")
	assert.NotContains(t, n, "Your value is still in the old store")
	assert.Empty(t, (*service.WorktreeEnrolResult)(nil).Notice())
	assert.Empty(t, (&service.WorktreeEnrolResult{}).Notice())
}

// TestWorktreeResult_Notice_BoundsTheNodeList: ten ids are listed, the rest
// counted.
func TestWorktreeResult_Notice_BoundsTheNodeList(t *testing.T) {
	r := lostResult()
	r.LostLocal = &service.LostLocal{Diff: service.BoardDiff{}}
	for i := 1; i <= 11; i++ {
		r.LostLocal.Diff[fmt.Sprintf("TEST-%02d", i)] = []string{"title"}
	}

	n := r.Notice()

	assert.Contains(t, n, "TEST-10 (title)")
	assert.NotContains(t, n, "TEST-11 (title)")
	assert.Contains(t, n, "and 1 more node(s)")
	assert.Contains(t, n, "differ on 11 node(s)")
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

// TestEnrolWorktree_LostLocal_NoticeWriteFails_IsAnErrorAfterEnrolment: the
// notice prints after every check; a write failure is loud, names the report,
// and the enrolment itself stands (review F7).
func TestEnrolWorktree_LostLocal_NoticeWriteFails_IsAnErrorAfterEnrolment(t *testing.T) {
	w := newEnrolWorld(t)
	w.opts.Notice = failingWriter{}
	w.opts.Ports.LostLocal = func(context.Context, string, string) (*service.LostLocal, error) {
		return &service.LostLocal{Diff: service.BoardDiff{"TEST-1": {"title"}}}, nil
	}
	w.svc = service.NewWorktreeEnrolment(w.opts)

	_, err := w.run()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "disk full")
	assert.Contains(t, err.Error(), "enroll-lost-local-")
	assert.Contains(t, w.calls, "enrol")
	rec, recErr := service.ReadHubAccepted(w.mtix())
	require.NoError(t, recErr)
	assert.NotNil(t, rec, "the doctor record is written before the notice")
}

// TestEnrolWorktree_LostLocal_NoticeComesAfterEnrol: when Enroll fails the
// original store is restored and no notice was printed (review F7).
func TestEnrolWorktree_LostLocal_NoticeComesAfterEnrol(t *testing.T) {
	w := newEnrolWorld(t)
	var notice strings.Builder
	w.opts.Notice = &notice
	w.opts.Ports.LostLocal = func(context.Context, string, string) (*service.LostLocal, error) {
		return &service.LostLocal{Diff: service.BoardDiff{"TEST-1": {"title"}}}, nil
	}
	w.fail["enrol"] = errors.New("host refused")
	w.svc = service.NewWorktreeEnrolment(w.opts)

	_, err := w.run()

	require.Error(t, err)
	assert.Empty(t, notice.String(), "never says enrolled before it is")
}

// TestWorktreeEnrolment_LostLocalPortMissing_IsRefused: an unwired lost-value
// check cannot silently skip (review F8).
func TestWorktreeEnrolment_LostLocalPortMissing_IsRefused(t *testing.T) {
	w := newEnrolWorld(t)
	w.opts.Ports.LostLocal = nil
	w.svc = service.NewWorktreeEnrolment(w.opts)

	_, err := w.run()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "ports required")
}
