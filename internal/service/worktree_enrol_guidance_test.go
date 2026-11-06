// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
)

// Refusal guidance and the lost-local-value notice (MTIX-112.17.16.18).

func deps(types ...string) string {
	rows := make([]string, len(types))
	for i, ty := range types {
		rows[i] = fmt.Sprintf(`{"from_id":"TEST-1","to_id":"TEST-%d","dep_type":%q}`, i+2, ty)
	}
	return `{"nodes":[{"id":"TEST-1"}],"dependencies":[` + strings.Join(rows, ",") + `]}`
}

// TestBoardFinding_Digest_SeveralRowsUnderOneKey_IsStableAndTracksEachRow: three
// dependency rows from one node share the id and field a difference is keyed on.
// The digest must not depend on map order, and changing any one row must change
// it (review r5, digest finding).
func TestBoardFinding_Digest_SeveralRowsUnderOneKey_IsStableAndTracksEachRow(t *testing.T) {
	hub := `{"nodes":[{"id":"TEST-1"}]}`
	first := ""
	for i := 0; i < 200; i++ {
		f, err := service.BoardHubFinding([]byte(deps("blocks", "related", "blocks")), []byte(hub))
		require.NoError(t, err)
		if first == "" {
			first = f.Digest()
		}
		require.Equal(t, first, f.Digest(), "run %d", i)
	}
	for i, changed := range [][]string{{"related", "related", "blocks"}, {"blocks", "blocks", "blocks"}, {"blocks", "related", "related"}} {
		f, err := service.BoardHubFinding([]byte(deps(changed...)), []byte(hub))
		require.NoError(t, err)
		assert.NotEqual(t, first, f.Digest(), "changing row %d changes the digest", i)
	}
}

// TestBoardFinding_Digest_SameRowsAgain_IsTheSame: the same rows give the
// same digest.
func TestBoardFinding_Digest_SameRowsAgain_IsTheSame(t *testing.T) {
	hub := `{"nodes":[{"id":"TEST-1"}]}`
	a, err := service.BoardHubFinding([]byte(deps("blocks", "related")), []byte(hub))
	require.NoError(t, err)
	b, err := service.BoardHubFinding([]byte(deps("related", "blocks")), []byte(hub))
	require.NoError(t, err)
	assert.NotEqual(t, a.Digest(), b.Digest(), "rows keyed by their target differ as rows")
	again, err := service.BoardHubFinding([]byte(deps("blocks", "related")), []byte(hub))
	require.NoError(t, err)
	assert.Equal(t, a.Digest(), again.Digest())
}

// printedLine returns the printed text after prefix up to the end of its line.
func printedLine(t *testing.T, msg, prefix string) string {
	t.Helper()
	i := strings.Index(msg, prefix)
	require.GreaterOrEqual(t, i, 0, "%q in %s", prefix, msg)
	rest := msg[i+len(prefix):]
	if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
		rest = rest[:nl]
	}
	return rest
}

// TestWorktreeBoardError_PrintedBackup_HostilePaths_AreOneLiteralWord runs the
// printed backup command through sh with a stand-in mtix that prints its
// arguments: a command substitution, a backtick, a space or a quote in the
// worktree's directory name must neither run nor split the path.
func TestWorktreeBoardError_PrintedBackup_HostilePaths_AreOneLiteralWord(t *testing.T) {
	dirs := t.TempDir()
	for _, name := range []string{"$(touch PWNED)", "`touch PWNED`", "with space", "it's", "a;b", "$HOME"} {
		t.Run(name, func(t *testing.T) {
			report := filepath.Join(dirs, name, ".mtix", "enroll-diff-20261106T010203Z.json")
			l, c := titled("branch", "hub")
			f, err := service.BoardHubFinding([]byte(l), []byte(c))
			require.NoError(t, err)
			msg := (&service.WorktreeBoardError{BoardFinding: *f, Report: report}).Error()
			cmd := printedLine(t, msg, "(1) keep a copy: ")
			require.True(t, strings.HasPrefix(cmd, "mtix backup "), cmd)

			work := t.TempDir()
			script := `mtix() { for a in "$@"; do printf '%s\n' "$a"; done; }; ` + cmd
			out, runErr := exec.CommandContext(context.Background(), "sh", "-c", script).Output() //nolint:gosec // G204: the printed text under test, with a stand-in function
			require.NoError(t, runErr)
			want := filepath.Join(dirs, name, ".mtix", "enrol-copy-20261106T010203Z.db")
			assert.Equal(t, "backup\n"+want+"\n", string(out))
			assert.NoFileExists(t, filepath.Join(work, "PWNED"))
			assert.NoFileExists(t, "PWNED")
		})
	}
}

// TestWorktreeBoardError_CopyPath_IsStampedPerRefusal: mtix backup refuses an
// existing file, so a second refusal must name a new path.
func TestWorktreeBoardError_CopyPath_IsStampedPerRefusal(t *testing.T) {
	l, c := titled("branch", "hub")
	f, err := service.BoardHubFinding([]byte(l), []byte(c))
	require.NoError(t, err)
	a := (&service.WorktreeBoardError{BoardFinding: *f, Report: "/w/.mtix/enroll-diff-20261106T010203Z.json"}).Error()
	b := (&service.WorktreeBoardError{BoardFinding: *f, Report: "/w/.mtix/enroll-diff-20261106T010204Z.json"}).Error()
	assert.Contains(t, a, "enrol-copy-20261106T010203Z.db")
	assert.Contains(t, b, "enrol-copy-20261106T010204Z.db")
}

// TestWorktreeBoardError_ChangedField_NamesOnlyWhatIsKnown: a store that differs
// from the hub may hold local work or may be behind; the text must not claim
// the first (review r5, wording finding).
func TestWorktreeBoardError_ChangedField_NamesOnlyWhatIsKnown(t *testing.T) {
	l, c := titled("branch", "hub")
	msg := board(t, l, c).Error()
	assert.NotContains(t, msg, "local work that is not on the hub")
	assert.Contains(t, msg, "may be local work or may be behind the hub")
	assert.Contains(t, msg, "(3) or, if the difference is meant to be dropped")
}

// TestWorktreeBoardError_StoreOnlyRow_SaysHowToClearARowAndWarnsAboutUpdate: a
// row the hub lacks cannot be cleared by updating it; it is created again, or
// accepted by digest (review r5, whole-row finding).
func TestWorktreeBoardError_StoreOnlyRow_SaysHowToClearARowAndWarnsAboutUpdate(t *testing.T) {
	msg := board(t, `{"nodes":[{"id":"TEST-1"},{"id":"TEST-4"}]}`, `{"nodes":[{"id":"TEST-1"}]}`).Error()
	assert.Contains(t, msg, "a row the hub lacks (TEST-4)")
	assert.Contains(t, msg, "create it again")
	assert.Contains(t, msg, "never mtix update a node the hub lacks")
	assert.Contains(t, msg, "accept-hub")
}

// TestWorktreeBoardError_ChangedFieldOnly_DoesNotMentionRows: the row advice
// is printed only when a whole row differs.
func TestWorktreeBoardError_ChangedFieldOnly_DoesNotMentionRows(t *testing.T) {
	l, c := titled("branch", "hub")
	assert.NotContains(t, board(t, l, c).Error(), "a row the hub lacks")
}

// TestWorktreeBoardError_SnapshotStore_PrintsAcceptWithTheCopyFirst: an
// auto-import store has no journal, so re-entering cannot make a write win.
func TestWorktreeBoardError_SnapshotStore_PrintsAcceptWithTheCopyFirst(t *testing.T) {
	l, c := titled("branch", "hub")
	f, err := service.BoardSnapshotHubFinding([]byte(l), []byte(c))
	require.NoError(t, err)
	msg := (&service.WorktreeBoardError{BoardFinding: *f, Report: "/w/.mtix/enroll-diff-x.json"}).Error()
	assert.NotContains(t, msg, "re-enter")
	assert.NotContains(t, msg, "local work that is not on the hub")
	assert.Less(t, strings.Index(msg, "(1) keep a copy"), strings.Index(msg, "accept-hub "+f.Digest()))
	assert.Contains(t, msg, "tasks.json snapshot")
}

// TestShellQuote_IsExported: the doctor and the refusal share one quoting.
func TestShellQuote_IsExported(t *testing.T) {
	assert.Equal(t, `'a'\''b'`, service.ShellQuote("a'b"))
}

func histWith(rows ...string) string { return historyDoc(rows...) }

// TestCompareBoardDocs_HistoryKeptKeys_EachOneIsCompared: a history row that
// differs only in one kept key must not vanish from the comparison. Every
// dropped key (a clone rewrites it) is tested the other way.
func TestCompareBoardDocs_HistoryKeptKeys_EachOneIsCompared(t *testing.T) {
	base := map[string]string{"id": "h1", "task_uid": "u1", "ts": "t1", "actor": "cli", "origin": "local", "kind": "change", "field": "title", "old": "\\\"a\\\"", "new": "\\\"x\\\"", "event_id": ""}
	build := func(over map[string]string) string {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		for k, v := range over {
			m[k] = v
		}
		parts := []string{}
		for _, k := range []string{"id", "task_uid", "ts", "actor", "origin", "kind", "field", "old", "new", "event_id"} {
			parts = append(parts, fmt.Sprintf(`"%s":"%s"`, k, m[k]))
		}
		return "{" + strings.Join(parts, ",") + "}"
	}
	for _, kept := range []string{"task_uid", "kind", "field", "old", "new"} {
		t.Run("kept "+kept, func(t *testing.T) {
			d, err := service.CompareBoardDocs([]byte(histWith(build(nil))), []byte(histWith(build(map[string]string{kept: "other"}))))
			require.NoError(t, err)
			assert.Equal(t, service.BoardDiff{"TEST-1": {"history"}}, d)
		})
	}
	for _, dropped := range []string{"id", "ts", "origin", "event_id"} {
		t.Run("dropped "+dropped, func(t *testing.T) {
			d, err := service.CompareBoardDocs([]byte(histWith(build(nil))), []byte(histWith(build(map[string]string{dropped: "other"}))))
			require.NoError(t, err)
			assert.Empty(t, d)
		})
	}
}

// TestFindLostLocal covers the field rule: only fields a journal event of this
// store wrote, whose value differs from the full clone, are reported.
func TestFindLostLocal(t *testing.T) {
	aside := `{"nodes":[{"id":"TEST-1","title":"branch","description":"d1"},{"id":"TEST-2","title":"two"}]}`
	clone := `{"nodes":[{"id":"TEST-1","title":"teammate 3","description":"d2"},{"id":"TEST-2","title":"two"}]}`
	own := []service.OwnField{{NodeID: "TEST-1", Field: "title"}, {NodeID: "TEST-2", Field: "title"}}

	got, err := service.FindLostLocal([]byte(aside), []byte(clone), own)

	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, service.BoardDiff{"TEST-1": {"title"}}, got.Diff, "description differs but no event of this store wrote it; TEST-2 matches")

	none, err := service.FindLostLocal([]byte(aside), []byte(aside), own)
	require.NoError(t, err)
	assert.Nil(t, none)
	noOwn, err := service.FindLostLocal([]byte(aside), []byte(clone), nil)
	require.NoError(t, err)
	assert.Nil(t, noOwn, "a difference no event of this store explains is the hub moving on")
	_, err = service.FindLostLocal([]byte("{bad"), []byte(clone), own)
	require.Error(t, err)
}

// TestOwnFieldsOf maps journal events to export field names.
func TestOwnFieldsOf(t *testing.T) {
	ev := func(op, status, payload string) service.OwnEvent {
		return service.OwnEvent{NodeID: "TEST-1", Op: op, Status: status, Payload: payload}
	}
	got := service.OwnFieldsOf([]service.OwnEvent{
		ev("update_field", "pushed", `{"field_name":"title","new_value":"\"x\""}`),
		ev("update_field", "pending", `{"field_name":"description","new_value":"\"y\""}`),
		ev("update_field", "applied", `{"field_name":"priority","new_value":"1"}`),
		ev("transition_status", "pushed", `{}`),
		ev("set_acceptance", "pushed", `{}`),
		ev("set_prompt", "pushed", `{}`),
		ev("comment", "pushed", `{}`),
		ev("create_node", "pushed", `{}`),
	})
	assert.ElementsMatch(t, []service.OwnField{
		{NodeID: "TEST-1", Field: "title"}, {NodeID: "TEST-1", Field: "description"}, {NodeID: "TEST-1", Field: "status"},
		{NodeID: "TEST-1", Field: "acceptance"}, {NodeID: "TEST-1", Field: "prompt"}, {NodeID: "TEST-1", Field: "annotations"},
	}, got, "an applied (mirrored hub) event is not this store's write; create_node writes no single field")
}

// TestEnrolWorktree_LostLocalValue_EnrolsWithANoticeAndADoctorRecord is the
// gate-1 design: the enrolment goes on, the report names the node and field,
// the notice is written, and the accepted record makes doctor warn.
func TestEnrolWorktree_LostLocalValue_EnrolsWithANoticeAndADoctorRecord(t *testing.T) {
	w := newEnrolWorld(t)
	var notice strings.Builder
	w.opts.Notice = &notice
	w.opts.Ports.LostLocal = func(_ context.Context, _, aside string) (*service.LostLocal, error) {
		assert.Equal(t, w.asideSeen, aside)
		return &service.LostLocal{Diff: service.BoardDiff{"TEST-1": {"title"}}}, nil
	}
	w.svc = service.NewWorktreeEnrolment(w.opts)

	res, err := w.run()

	require.NoError(t, err)
	assert.Contains(t, w.calls, "enrol")
	require.NotNil(t, res.LostLocal)
	assert.FileExists(t, res.LostLocalReport)
	body, readErr := os.ReadFile(res.LostLocalReport)
	require.NoError(t, readErr)
	assert.Contains(t, string(body), "TEST-1")
	assert.Contains(t, string(body), "local-value-lost")
	assert.NotContains(t, string(body), "branch", "ids and field names only")
	assert.Contains(t, notice.String(), "TEST-1")
	assert.Contains(t, notice.String(), "title")
	assert.Contains(t, notice.String(), res.AsideDir)
	assert.Equal(t, notice.String(), res.Notice())
	rec, recErr := service.ReadHubAccepted(w.mtix())
	require.NoError(t, recErr)
	require.NotNil(t, rec, "doctor warns until the record is cleared")
	assert.Equal(t, res.LostLocalReport, rec.LostLocalReport)
	assert.Equal(t, res.AsideDir, rec.AsideDir)
	w.assertMarker(t, false)
}

// TestEnrolWorktree_LostLocalCheckFails_RestoresTheOriginal: never a silent
// pass when the check cannot run.
func TestEnrolWorktree_LostLocalCheckFails_RestoresTheOriginal(t *testing.T) {
	w := newEnrolWorld(t)
	before := treeHash(t, w.data)
	w.opts.Ports.LostLocal = func(context.Context, string, string) (*service.LostLocal, error) {
		return nil, fmt.Errorf("boom")
	}
	w.svc = service.NewWorktreeEnrolment(w.opts)

	_, err := w.run()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom")
	assert.Equal(t, before, treeHash(t, w.data), "the original store is back")
	assert.NotContains(t, w.calls, "enrol")
}

// TestEnrolWorktree_NothingLost_NoNoticeNoRecord.
func TestEnrolWorktree_NothingLost_NoNoticeNoRecord(t *testing.T) {
	w := newEnrolWorld(t)
	var notice strings.Builder
	w.opts.Notice = &notice
	w.opts.Ports.LostLocal = func(context.Context, string, string) (*service.LostLocal, error) { return nil, nil }
	w.svc = service.NewWorktreeEnrolment(w.opts)

	res, err := w.run()

	require.NoError(t, err)
	assert.Nil(t, res.LostLocal)
	assert.Empty(t, notice.String())
	assert.Empty(t, res.Notice())
	rec, recErr := service.ReadHubAccepted(w.mtix())
	require.NoError(t, recErr)
	assert.Nil(t, rec)
}

// TestEnrolWorktree_LostLocalAndAcceptHub_KeepBothReports.
func TestEnrolWorktree_LostLocalAndAcceptHub_KeepBothReports(t *testing.T) {
	w := newEnrolWorld(t)
	writeBoard(t, w, `{"nodes":[{"id":"TEST-7"}]}`)
	w.boardFinding = hubDiffers("TEST-7")
	w.cloneBoardFinding = hubDiffers("TEST-7")
	w.opts.Ports.LostLocal = func(context.Context, string, string) (*service.LostLocal, error) {
		return &service.LostLocal{Diff: service.BoardDiff{"TEST-1": {"title"}}}, nil
	}
	w.svc = service.NewWorktreeEnrolment(w.opts)

	res, err := w.runAccepting(hubDiffers("TEST-7").Digest())

	require.NoError(t, err)
	assert.NotEqual(t, res.HubDiffReport, res.LostLocalReport)
	rec, recErr := service.ReadHubAccepted(w.mtix())
	require.NoError(t, recErr)
	assert.Equal(t, res.HubDiffReport, rec.Report)
	assert.Equal(t, res.LostLocalReport, rec.LostLocalReport)
}
