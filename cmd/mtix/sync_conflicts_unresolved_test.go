// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// Unresolved-conflict rule (MTIX-95.7): an lww row is unresolved until a
// later manual row exists for the same (node, field) pair. mtix sync
// status counts it, its >50 banner uses the same count, and mtix sync
// conflicts list shows exactly those rows by default.

// conflictSeed is one row to insert into the local sync_conflicts table.
type conflictSeed struct {
	node, field, resolution string
}

// seedConflictRow inserts one sync_conflicts row and returns its id.
func seedConflictRow(t *testing.T, s conflictSeed) int64 {
	t.Helper()
	ctx := context.Background()
	var id int64
	require.NoError(t, app.store.WithTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO sync_conflicts
			  (event_id_winner, event_id_loser, node_id, field_name, resolution, resolved_at, resolved_by)
			VALUES (?, ?, ?, ?, ?, ?, NULL)`,
			"evt-winner-"+s.node, "evt-loser-"+s.node, s.node, nullIfEmpty(s.field), s.resolution,
			"2026-09-25T00:00:00Z")
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	}))
	return id
}

// statusJSON runs mtix sync status --json and returns its keys.
func statusJSON(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	app.jsonOutput = true
	defer func() { app.jsonOutput = false }()
	var stdout, stderr bytes.Buffer
	require.NoError(t, runSyncStatus(context.Background(), &stdout, &stderr), stderr.String())
	var got map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &got), stdout.String())
	return got
}

// statusText runs mtix sync status and returns its table.
func statusText(t *testing.T) string {
	t.Helper()
	app.jsonOutput = false
	var stdout, stderr bytes.Buffer
	require.NoError(t, runSyncStatus(context.Background(), &stdout, &stderr), stderr.String())
	return stdout.String()
}

// listConflicts runs mtix sync conflicts list (--json), with --all when
// all is set, and returns the conflict ids in order and each row's
// unresolved flag.
func listConflicts(t *testing.T, all bool) ([]int64, []bool) {
	t.Helper()
	if all {
		return listConflictsWith(t, "--all")
	}
	return listConflictsWith(t)
}

// listConflictsWith runs mtix sync conflicts list --json with flags and
// returns the conflict ids in order and each row's unresolved flag.
func listConflictsWith(t *testing.T, flags ...string) ([]int64, []bool) {
	t.Helper()
	app.jsonOutput = true
	defer func() { app.jsonOutput = false }()
	argv := append([]string{"sync", "conflicts", "list"}, flags...)
	stdout, stderr, err := execSyncCLI(context.Background(), argv)
	require.NoError(t, err, stderr)
	var rows []struct {
		ConflictID int64 `json:"conflict_id"`
		Unresolved *bool `json:"unresolved"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &rows), stdout)
	ids, unresolved := []int64{}, []bool{}
	for _, r := range rows {
		require.NotNilf(t, r.Unresolved, "row %d carries its unresolved flag", r.ConflictID)
		ids, unresolved = append(ids, r.ConflictID), append(unresolved, *r.Unresolved)
	}
	return ids, unresolved
}

// resolveConflict records a manual resolution for id.
func resolveConflict(t *testing.T, id int64, action string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	require.NoError(t, runSyncConflictsResolve(context.Background(), &stdout, &stderr,
		strconv.FormatInt(id, 10), action), stderr.String())
}

// TestSyncStatus_ResolvedConflictNotCounted: N lww conflicts on distinct
// pairs count as N unresolved (with the banner above 50); resolving each
// takes the counter to 0 and clears the banner. The table and --json no
// longer carry a 'conflicted' count (MTIX-95.7).
func TestSyncStatus_ResolvedConflictNotCounted(t *testing.T) {
	for _, n := range []int{1, 3, 50, 51} {
		t.Run(fmt.Sprintf("%d conflicts", n), func(t *testing.T) {
			initTestApp(t)
			ids := make([]int64, 0, n)
			for i := 0; i < n; i++ {
				ids = append(ids, seedConflictRow(t, conflictSeed{fmt.Sprintf("TEST-%d", i+1), "title", "lww"}))
			}

			got := statusJSON(t)
			require.JSONEq(t, strconv.Itoa(n), string(got["open_conflicts"]))
			require.JSONEq(t, strconv.FormatBool(n > 50), string(got["high_conflict"]))
			require.NotContains(t, got, "conflicted", "the never-set conflicted count is gone from --json")
			text := statusText(t)
			require.Regexp(t, regexp.MustCompile(`(?m)^unresolved conflicts\s+`+strconv.Itoa(n)+`$`), text)
			require.NotRegexp(t, regexp.MustCompile(`(?m)^conflicted\s`), text, "no conflicted row in the table")
			if n > 50 {
				require.Contains(t, text, fmt.Sprintf("WARN: %d unresolved conflicts", n))
			} else {
				require.NotContains(t, text, "WARN")
			}

			for _, id := range ids {
				resolveConflict(t, id, "acknowledge")
			}
			got = statusJSON(t)
			require.JSONEq(t, "0", string(got["open_conflicts"]), "resolving every conflict takes the counter to 0")
			require.JSONEq(t, "false", string(got["high_conflict"]))
			text = statusText(t)
			require.Regexp(t, regexp.MustCompile(`(?m)^unresolved conflicts\s+0$`), text)
			require.NotContains(t, text, "WARN", "no banner once every conflict is resolved")
		})
	}
}

// TestSyncConflicts_UnresolvedRule_CountAndListAgree covers the pair rule
// row by row: status counts exactly the rows conflicts list shows by
// default, and --all lists every row with its flag (MTIX-95.7).
func TestSyncConflicts_UnresolvedRule_CountAndListAgree(t *testing.T) {
	tests := []struct {
		name           string
		rows           []conflictSeed
		wantUnresolved []int // indexes into rows
	}{
		{"a later manual row resolves every earlier lww row of the pair",
			[]conflictSeed{{"TEST-1", "title", "lww"}, {"TEST-1", "title", "lww"}, {"TEST-1", "title", "manual"}}, nil},
		{"a manual row for another field resolves nothing",
			[]conflictSeed{{"TEST-1", "title", "lww"}, {"TEST-1", "description", "manual"}}, []int{0}},
		{"a manual row for another node resolves nothing",
			[]conflictSeed{{"TEST-1", "title", "lww"}, {"TEST-2", "title", "manual"}}, []int{0}},
		{"a later lww row reopens a resolved pair",
			[]conflictSeed{{"TEST-1", "title", "lww"}, {"TEST-1", "title", "manual"}, {"TEST-1", "title", "lww"}}, []int{2}},
		{"an earlier manual row does not resolve a later lww row",
			[]conflictSeed{{"TEST-1", "title", "manual"}, {"TEST-1", "title", "lww"}}, []int{1}},
		{"rows without a field pair with each other",
			[]conflictSeed{{"TEST-1", "", "lww"}, {"TEST-1", "", "manual"}}, nil},
		{"a row without a field does not pair with a named field",
			[]conflictSeed{{"TEST-1", "", "lww"}, {"TEST-1", "title", "manual"}}, []int{0}},
		{"tombstone rows are not unresolved conflicts",
			[]conflictSeed{{"TEST-1", "", "tombstone"}}, nil},
		{"no rows", nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			ids := make([]int64, 0, len(tt.rows))
			for _, r := range tt.rows {
				ids = append(ids, seedConflictRow(t, r))
			}
			wantIDs := []int64{}
			wantFlags := make([]bool, len(ids))
			for _, i := range tt.wantUnresolved {
				wantIDs = append(wantIDs, ids[i])
				wantFlags[i] = true
			}

			gotIDs, gotFlags := listConflicts(t, false)
			require.Equal(t, wantIDs, gotIDs, "default list shows exactly the unresolved rows")
			for _, f := range gotFlags {
				require.True(t, f)
			}
			require.JSONEq(t, strconv.Itoa(len(wantIDs)), string(statusJSON(t)["open_conflicts"]),
				"status counts exactly the rows the default list shows")

			allIDs, allFlags := listConflicts(t, true)
			require.Equal(t, ids, append([]int64{}, allIDs...), "--all lists every row")
			require.Equal(t, wantFlags, allFlags, "--all marks each row's state")
		})
	}
}

// TestSyncConflictsList_Text_DefaultAndAll: the text form lists only the
// unresolved rows by default, says so when there are none, and lists
// every row with --all (MTIX-95.7).
func TestSyncConflictsList_Text_DefaultAndAll(t *testing.T) {
	initTestApp(t)
	first := seedConflictRow(t, conflictSeed{"TEST-1", "title", "lww"})
	resolveConflict(t, first, "keep-local")

	stdout, stderr, err := execSyncCLI(context.Background(), []string{"sync", "conflicts", "list"})
	require.NoError(t, err, stderr)
	require.Contains(t, stdout, "no unresolved conflicts")
	require.NotContains(t, stdout, fmt.Sprintf("[%d]", first))

	stdout, stderr, err = execSyncCLI(context.Background(), []string{"sync", "conflicts", "list", "--all"})
	require.NoError(t, err, stderr)
	require.Contains(t, stdout, fmt.Sprintf("[%d] TEST-1", first))
	require.Contains(t, stdout, "resolution=manual")

	reopened := seedConflictRow(t, conflictSeed{"TEST-1", "title", "lww"})
	stdout, stderr, err = execSyncCLI(context.Background(), []string{"sync", "conflicts", "list"})
	require.NoError(t, err, stderr)
	require.Contains(t, stdout, fmt.Sprintf("[%d] TEST-1", reopened), "a later lww row reopens the pair")
	require.NotContains(t, stdout, fmt.Sprintf("[%d]", first))
}

// resolveNoStateChangeMessage is the statement resolve prints in text and
// --json (MTIX-95.7).
const resolveNoStateChangeMessage = "decision recorded; node state not changed"

// TestConflictsResolve_OutputStatesNoStateChange: resolve records the
// decision and says, in text and in --json, that it changed no node
// state; the node is indeed unchanged (MTIX-95.7, Commandment 11).
func TestConflictsResolve_OutputStatesNoStateChange(t *testing.T) {
	for _, jsonOut := range []bool{false, true} {
		t.Run(map[bool]string{false: "text", true: "json"}[jsonOut], func(t *testing.T) {
			initTestApp(t)
			require.NoError(t, runCreate("contested", "", "", 3, "", "", "", "", ""))
			before := nodeSnapshot(t, "TEST-1")
			id := seedConflictRow(t, conflictSeed{"TEST-1", "title", "lww"})

			app.jsonOutput = jsonOut
			defer func() { app.jsonOutput = false }()
			var stdout, stderr bytes.Buffer
			require.NoError(t, runSyncConflictsResolve(context.Background(), &stdout, &stderr,
				strconv.FormatInt(id, 10), "keep-remote"))

			if jsonOut {
				var got struct {
					ConflictID       int64  `json:"conflict_id"`
					NodeID           string `json:"node_id"`
					FieldName        string `json:"field_name"`
					Action           string `json:"action"`
					DecisionRecorded *bool  `json:"decision_recorded"`
					NodeStateChanged *bool  `json:"node_state_changed"`
					Message          string `json:"message"`
				}
				require.NoError(t, json.Unmarshal(stdout.Bytes(), &got), stdout.String())
				require.Equal(t, id, got.ConflictID)
				require.Equal(t, "TEST-1", got.NodeID)
				require.Equal(t, "title", got.FieldName)
				require.Equal(t, "keep-remote", got.Action)
				require.NotNil(t, got.DecisionRecorded)
				require.True(t, *got.DecisionRecorded)
				require.NotNil(t, got.NodeStateChanged)
				require.False(t, *got.NodeStateChanged)
				require.Equal(t, resolveNoStateChangeMessage, got.Message)
			} else {
				require.Contains(t, stdout.String(), resolveNoStateChangeMessage)
				require.Contains(t, stdout.String(), "recorded manual resolution")
			}
			require.Equal(t, before, nodeSnapshot(t, "TEST-1"), "resolve changes no node state")
			require.Equal(t, 1, conflictRowsFor(t, "TEST-1", "manual"), "the decision is recorded")
		})
	}
}

// TestConflictsResolve_ManualRow_ReturnsInvalidInput: a manual row is a
// recorded decision, not a conflict, so resolving it is invalid input and
// records nothing (MTIX-95.7).
func TestConflictsResolve_ManualRow_ReturnsInvalidInput(t *testing.T) {
	initTestApp(t)
	lww := seedConflictRow(t, conflictSeed{"TEST-1", "title", "lww"})
	resolveConflict(t, lww, "acknowledge")
	var manual int64
	require.NoError(t, app.store.QueryRow(context.Background(),
		`SELECT conflict_id FROM sync_conflicts WHERE resolution = 'manual'`).Scan(&manual))

	var stdout, stderr bytes.Buffer
	err := runSyncConflictsResolve(context.Background(), &stdout, &stderr,
		strconv.FormatInt(manual, 10), "keep-local")
	require.ErrorIs(t, err, model.ErrInvalidInput)
	require.Contains(t, err.Error(), "manual")
	require.Equal(t, 1, conflictRowsFor(t, "TEST-1", "manual"), "nothing is recorded")
	require.Empty(t, stdout.String())
}

// nodeSnapshot returns the stored columns of one node that a state change
// would touch.
func nodeSnapshot(t *testing.T, id string) string {
	t.Helper()
	var title, status, updated, hash string
	require.NoError(t, app.store.QueryRow(context.Background(),
		`SELECT title, status, updated_at, COALESCE(content_hash, '') FROM nodes WHERE id = ?`, id,
	).Scan(&title, &status, &updated, &hash))
	return title + "|" + status + "|" + updated + "|" + hash
}

// conflictRowsFor counts the sync_conflicts rows of node with resolution.
func conflictRowsFor(t *testing.T, node, resolution string) int {
	t.Helper()
	var n int
	require.NoError(t, app.store.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM sync_conflicts WHERE node_id = ? AND resolution = ?`, node, resolution).Scan(&n))
	return n
}

// TestPrintConflictsTable_AllRows_BannerCountsUnresolvedOnly: with --all
// the banner still counts only the unresolved rows, and an empty table
// says what was listed (MTIX-95.7).
func TestPrintConflictsTable_AllRows_BannerCountsUnresolvedOnly(t *testing.T) {
	rowsWith := func(total, unresolved int) []ConflictRow {
		rows := make([]ConflictRow, total)
		for i := range rows {
			rows[i] = ConflictRow{ConflictID: int64(i + 1), NodeID: "TEST-1", Resolution: "lww", Unresolved: i < unresolved}
		}
		return rows
	}
	tests := []struct {
		name       string
		rows       []ConflictRow
		all        bool
		wantBanner string
		wantText   string
	}{
		{"60 rows, 10 unresolved: no banner", rowsWith(60, 10), true, "", "unresolved=false"},
		{"60 rows, exactly 50 unresolved: no banner", rowsWith(60, 50), true, "", "unresolved=true"},
		{"60 rows, 51 unresolved: banner counts 51", rowsWith(60, 51), true, "51 unresolved conflicts", "unresolved=true"},
		{"no unresolved rows", nil, false, "", "no unresolved conflicts"},
		{"no rows at all", nil, true, "", "no conflicts recorded"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			require.NoError(t, printConflictsTable(&buf, tt.rows, tt.all))
			out := buf.String()
			require.Contains(t, out, tt.wantText)
			if tt.wantBanner == "" {
				require.NotContains(t, out, "--batch")
			} else {
				require.Contains(t, out, tt.wantBanner)
			}
		})
	}
}

// TestSyncConflictsList_NodeFilter_ScopesListNotStatus: --node and its
// alias --batch list only the named node's rows, unresolved by default and
// every row with --all; sync status still counts every node's unresolved
// conflicts (MTIX-95.7).
func TestSyncConflictsList_NodeFilter_ScopesListNotStatus(t *testing.T) {
	seeds := []conflictSeed{
		{"TEST-1", "title", "lww"},       // 0: unresolved
		{"TEST-2", "title", "lww"},       // 1: resolved by 2
		{"TEST-2", "title", "manual"},    // 2
		{"TEST-2", "description", "lww"}, // 3: unresolved
	}
	tests := []struct {
		name  string
		flags []string
		want  []int // indexes into seeds
	}{
		{"--node, unresolved only", []string{"--node", "TEST-2"}, []int{3}},
		{"--node with --all", []string{"--node", "TEST-2", "--all"}, []int{1, 2, 3}},
		{"--batch, unresolved only", []string{"--batch", "TEST-1"}, []int{0}},
		{"--batch with --all", []string{"--batch", "TEST-2", "--all"}, []int{1, 2, 3}},
		{"a node without rows", []string{"--node", "TEST-9", "--all"}, nil},
		{"no filter", nil, []int{0, 3}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			ids := make([]int64, 0, len(seeds))
			for _, sd := range seeds {
				ids = append(ids, seedConflictRow(t, sd))
			}
			want := []int64{}
			for _, i := range tt.want {
				want = append(want, ids[i])
			}
			got, _ := listConflictsWith(t, tt.flags...)
			require.Equal(t, want, got)
			require.JSONEq(t, "2", string(statusJSON(t)["open_conflicts"]),
				"status counts both nodes' unresolved conflicts, whatever the list filter")
		})
	}
}

// conflictOp is one step of a resolve scenario: insert seed, or, when
// resolve is set, resolve the conflict inserted by that step (1-based).
type conflictOp struct {
	seed    conflictSeed
	resolve int
}

// TestConflictsResolve_TargetWithLaterRow_ReturnsInvalidInputNamingNewest:
// a resolve whose conflict already has a later manual or lww row for the
// same node and field is refused as invalid input, names the newest
// conflict of that node and field, and records nothing; the newest
// conflict is the one to resolve (MTIX-95.7).
func TestConflictsResolve_TargetWithLaterRow_ReturnsInvalidInputNamingNewest(t *testing.T) {
	lww := func(node, field string) conflictOp { return conflictOp{seed: conflictSeed{node, field, "lww"}} }
	resolve := func(step int) conflictOp { return conflictOp{resolve: step} }
	tests := []struct {
		name       string
		ops        []conflictOp
		target     int    // step whose conflict is resolved last
		wantErr    string // "" when the resolve is accepted
		wantNewest int    // step of the conflict the error names
		wantOpen   []int  // steps still unresolved afterwards
	}{
		{"a repeated resolve", []conflictOp{lww("TEST-1", "title"), resolve(1)}, 1,
			"already resolved", 1, nil},
		{"a stale resolve after the pair reopened", []conflictOp{lww("TEST-1", "title"), resolve(1), lww("TEST-1", "title")}, 1,
			"resolve conflict_id %d instead", 3, []int{3}},
		{"an older conflict whose newest conflict is resolved",
			[]conflictOp{lww("TEST-1", "title"), lww("TEST-1", "title"), resolve(2)}, 1,
			"already resolved", 2, nil},
		{"the older of two open conflicts", []conflictOp{lww("TEST-1", "title"), lww("TEST-1", "title")}, 1,
			"resolve conflict_id %d instead", 2, []int{1, 2}},
		{"the older of two open conflicts without a field", []conflictOp{lww("TEST-1", ""), lww("TEST-1", "")}, 1,
			"resolve conflict_id %d instead", 2, []int{1, 2}},
		{"the newest conflict resolves the pair", []conflictOp{lww("TEST-1", "title"), lww("TEST-1", "title")}, 2,
			"", 0, nil},
		{"a later conflict on another field does not block", []conflictOp{lww("TEST-1", "title"), lww("TEST-1", "description")}, 1,
			"", 0, []int{2}},
		{"a later conflict on another node does not block", []conflictOp{lww("TEST-1", "title"), lww("TEST-2", "title")}, 1,
			"", 0, []int{2}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			ids := make([]int64, len(tt.ops)+1)
			for i, op := range tt.ops {
				if op.resolve > 0 {
					resolveConflict(t, ids[op.resolve], "acknowledge")
					continue
				}
				ids[i+1] = seedConflictRow(t, op.seed)
			}
			manualBefore := conflictRowsFor(t, "TEST-1", "manual")

			var stdout, stderr bytes.Buffer
			err := runSyncConflictsResolve(context.Background(), &stdout, &stderr,
				strconv.FormatInt(ids[tt.target], 10), "keep-local")
			if tt.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, model.ErrInvalidInput)
				wantErr := tt.wantErr
				if strings.Contains(wantErr, "%d") {
					wantErr = fmt.Sprintf(wantErr, ids[tt.wantNewest])
				}
				require.Contains(t, err.Error(), wantErr)
				require.Contains(t, err.Error(), fmt.Sprintf("conflict_id %d", ids[tt.wantNewest]))
				require.Equal(t, manualBefore, conflictRowsFor(t, "TEST-1", "manual"), "nothing is recorded")
				require.Empty(t, stdout.String())
			}
			wantOpen := []int64{}
			for _, step := range tt.wantOpen {
				wantOpen = append(wantOpen, ids[step])
			}
			got, _ := listConflicts(t, false)
			require.Equal(t, wantOpen, got)
		})
	}
}

// laterConflictSQL inserts a later lww conflict on TEST-1's title.
const laterConflictSQL = `
	INSERT INTO sync_conflicts
	  (event_id_winner, event_id_loser, node_id, field_name, resolution, resolved_at, resolved_by)
	VALUES ('evt-winner-later', 'evt-loser-later', 'TEST-1', 'title', 'lww', '2026-09-25T00:00:01Z', NULL)`

// otherConnection opens a second connection to the project database, as
// another process (a pull, for example) would, with a short busy timeout.
func otherConnection(t *testing.T) *sql.DB {
	t.Helper()
	dbPath := filepath.Join(app.mtixDir, "data", "mtix.db")
	require.FileExists(t, dbPath)
	other, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(200)&_txlock=immediate")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, other.Close()) })
	return other
}

// TestResolveConflictRow_ConcurrentLaterConflict_CannotCommitBetweenCheckAndInsert:
// resolve checks for a later conflict and inserts its manual row in one
// write transaction. Another connection (a pull, for example) cannot
// commit a later lww conflict just before the check or between the check
// and the insert: the resolve holds the write lock throughout, so that
// write waits, commits after the manual row, and stays unresolved
// (MTIX-95.7).
func TestResolveConflictRow_ConcurrentLaterConflict_CannotCommitBetweenCheckAndInsert(t *testing.T) {
	tests := []struct {
		name  string
		seams func(probe func(*sql.Tx) error) resolveSeams
	}{
		{"just before the check", func(probe func(*sql.Tx) error) resolveSeams { return resolveSeams{beforeCheck: probe} }},
		{"between the check and the insert", func(probe func(*sql.Tx) error) resolveSeams { return resolveSeams{afterCheck: probe} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			ctx := context.Background()
			id := seedConflictRow(t, conflictSeed{"TEST-1", "title", "lww"})
			target, err := lookupConflict(ctx, app.store, id)
			require.NoError(t, err)
			other := otherConnection(t)

			var windowErr error
			ran := false
			err = resolveConflictRow(ctx, app.store, target, "acknowledge", tt.seams(func(*sql.Tx) error {
				ran = true
				_, windowErr = other.ExecContext(ctx, laterConflictSQL)
				return nil
			}))
			require.True(t, ran, "the seam runs inside the resolve")
			require.NoError(t, err)
			require.Error(t, windowErr, "no other writer commits inside the resolve")
			require.Regexp(t, `(?i)busy|locked`, windowErr.Error(), "the other writer waits for the resolve's write lock")

			_, err = other.ExecContext(ctx, laterConflictSQL)
			require.NoError(t, err, "the later conflict commits once the resolve has")
			open, _ := listConflicts(t, false)
			require.Len(t, open, 1, "the later conflict is unresolved: the decision did not close it")
			require.NotEqual(t, id, open[0])
		})
	}
}

// TestResolveConflictRow_CheckReadsTheWriteTransaction: the check runs on
// the resolve's own write transaction, so a later conflict written on that
// transaction just before the check is seen and the resolve is refused;
// nothing is committed (MTIX-95.7).
func TestResolveConflictRow_CheckReadsTheWriteTransaction(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	id := seedConflictRow(t, conflictSeed{"TEST-1", "title", "lww"})
	target, err := lookupConflict(ctx, app.store, id)
	require.NoError(t, err)

	err = resolveConflictRow(ctx, app.store, target, "acknowledge", resolveSeams{
		beforeCheck: func(tx *sql.Tx) error {
			_, execErr := tx.ExecContext(ctx, laterConflictSQL)
			return execErr
		},
	})
	require.ErrorIs(t, err, model.ErrInvalidInput, "the check sees the later conflict on its own transaction")
	require.Equal(t, 0, conflictRowsFor(t, "TEST-1", "manual"), "no decision is recorded")
	open, _ := listConflicts(t, false)
	require.Equal(t, []int64{id}, open, "the refused transaction commits nothing")
}
