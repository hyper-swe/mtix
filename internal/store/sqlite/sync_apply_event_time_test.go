// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

// Event times at apply (MTIX-95.26).
//
// Every timestamp the apply code derives from an event's wall_clock_ts goes
// through one helper, eventTime (sync_event_time.go): within years 1..9999 it
// is the event's own time, and outside that range it is the apply time. Every
// pulled event therefore applies, the rest of its pull batch applies with it,
// and GetNode and ListNodes read every node afterwards.

// eventTimeWall is one wall_clock_ts outside years 1..9999.
type eventTimeWall struct {
	name string
	ms   int64
}

// wallsOutsideRange lists the wall_clock_ts values outside years 1..9999
// that each test applies: the first millisecond of year 10000 and the
// largest value the envelope can carry.
func wallsOutsideRange() []eventTimeWall {
	return []eventTimeWall{
		{"year 10000", year10000MS},
		{"largest wall_clock_ts", math.MaxInt64},
	}
}

// pulledEvent builds an event of any op type authored on another replica
// (as foreignWorkflowEvent does) with the given wall_clock_ts.
func pulledEvent(t *testing.T, op model.OpType, nodeID string, payload any, lamport, wall int64) *model.SyncEvent {
	t.Helper()
	e := foreignWorkflowEvent(t, nodeID, op, payload, lamport, "")
	e.WallClockTS = wall
	return e
}

// titleUpdate builds an update_field payload that sets the title.
func titleUpdate(t *testing.T, title string) *model.UpdateFieldPayload {
	t.Helper()
	v, err := json.Marshal(title)
	require.NoError(t, err)
	return &model.UpdateFieldPayload{FieldName: "title", NewValue: v}
}

// requireAllNodesReadable checks that ListNodes reads every live node and
// that GetNode reads each of them.
func requireAllNodesReadable(t *testing.T, s *sqlite.Store, raw *sql.DB) {
	t.Helper()
	ctx := context.Background()
	var live int
	// Live node count, to check that ListNodes returned every live node.
	require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM nodes WHERE deleted_at IS NULL`).Scan(&live))
	nodes, _, err := s.ListNodes(ctx, store.NodeFilter{}, store.ListOptions{Limit: 100})
	require.NoError(t, err, "ListNodes reads every node")
	require.Len(t, nodes, live)
	for _, n := range nodes {
		_, err := s.GetNode(ctx, n.ID)
		require.NoError(t, err, "GetNode reads %s", n.ID)
	}
}

// requireCommentAt checks that n carries exactly the annotation of comment
// event c and that the annotation's created_at and the node's updated_at are
// the stamped time: want exactly when non-nil, else a time in [before, after]
// (the apply time).
func requireCommentAt(t *testing.T, n *model.Node, c *model.SyncEvent, want *time.Time, before, after time.Time) {
	t.Helper()
	require.Len(t, n.Annotations, 1)
	a := n.Annotations[0]
	require.Equal(t, c.EventID, a.ID)
	require.Equal(t, "peer-b", a.Author)
	require.Equal(t, "note", a.Text)
	if want != nil {
		require.True(t, want.Equal(a.CreatedAt), "created_at %s is the event time %s", a.CreatedAt, *want)
		require.True(t, want.Truncate(time.Second).Equal(n.UpdatedAt),
			"updated_at %s is the event time in whole seconds", n.UpdatedAt)
		return
	}
	require.False(t, a.CreatedAt.Before(before), "created_at %s is the apply time", a.CreatedAt)
	require.False(t, a.CreatedAt.After(after), "created_at %s is the apply time", a.CreatedAt)
	require.False(t, n.UpdatedAt.Before(before.Truncate(time.Second)), "updated_at %s is the apply time", n.UpdatedAt)
	require.False(t, n.UpdatedAt.After(after), "updated_at %s is the apply time", n.UpdatedAt)
}

// TestApply_CommentEventTime_StoredWithinReadableRange: a pulled comment
// stamps the annotation's created_at and the node's updated_at with the
// event's own time within years 1..9999 and with the apply time outside it.
// The comment applies, the next event in the same pull batch applies, and
// GetNode and ListNodes return the node with its annotation.
func TestApply_CommentEventTime_StoredWithinReadableRange(t *testing.T) {
	inRange := foreignWallClock()
	lastOf9999 := time.Date(9999, 12, 31, 23, 59, 59, 999_000_000, time.UTC)
	tests := []struct {
		name string
		wall int64
		want *time.Time // nil: the apply time
	}{
		{"in range keeps the event time", inRange.UnixMilli(), &inRange},
		{"last millisecond of year 9999 keeps the event time", lastOf9999.UnixMilli(), &lastOf9999},
		{"year 10000 stores the apply time", year10000MS, nil},
		{"largest wall_clock_ts stores the apply time", math.MaxInt64, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s, raw := replicaWithNode(t)
			mustCreateNode(t, s, "MTIX-2", "")
			comment := pulledEvent(t, model.OpComment, "MTIX-1",
				&model.CommentPayload{AuthorID: "peer-b", Body: "note"}, 2, tt.wall)
			next := pulledEvent(t, model.OpUpdateField, "MTIX-2", titleUpdate(t, "after"), 3,
				foreignWallClock().UnixMilli())

			before := time.Now().UTC()
			require.NoError(t, applyEventsInTx(s, []*model.SyncEvent{comment, next}),
				"the comment and the rest of its pull batch apply")
			after := time.Now().UTC()

			n, err := s.GetNode(ctx, "MTIX-1")
			require.NoError(t, err, "GetNode reads the node")
			requireCommentAt(t, n, comment, tt.want, before, after)
			other, err := s.GetNode(ctx, "MTIX-2")
			require.NoError(t, err)
			require.Equal(t, "after", other.Title, "the next event in the pull batch applied")

			nodes, _, err := s.ListNodes(ctx, store.NodeFilter{}, store.ListOptions{Limit: 10})
			require.NoError(t, err, "ListNodes reads every node")
			require.Len(t, nodes, 2)
			for _, listed := range nodes {
				if listed.ID == "MTIX-1" {
					requireCommentAt(t, listed, comment, tt.want, before, after)
				}
			}
			requireAllNodesReadable(t, s, raw)
		})
	}
}

// TestApply_TerminalTransitionEventTime_ClosedAtWithinReadableRange: a
// winning done, cancelled or invalidated stamps closed_at with the event's
// time in whole seconds within years 1..9999, and with the apply time (the
// same value as updated_at) outside it. The node stays readable.
func TestApply_TerminalTransitionEventTime_ClosedAtWithinReadableRange(t *testing.T) {
	walls := []eventTimeWall{{"in range", foreignWallClock().UnixMilli()}}
	walls = append(walls, wallsOutsideRange()...)
	for _, to := range []model.Status{model.StatusDone, model.StatusCancelled, model.StatusInvalidated} {
		for _, w := range walls {
			t.Run(string(to)+", "+w.name, func(t *testing.T) {
				ctx := context.Background()
				s, raw := replicaWithNode(t)
				ev := pulledEvent(t, model.OpTransitionStatus, "MTIX-1",
					transition(model.StatusInProgress, to), 2, w.ms)

				before := time.Now().UTC().Truncate(time.Second)
				pullEvents(t, s, []*model.SyncEvent{ev})
				after := time.Now().UTC()

				n, err := s.GetNode(ctx, "MTIX-1")
				require.NoError(t, err, "GetNode reads the node")
				require.Equal(t, to, n.Status, "the event applies")
				require.NotNil(t, n.ClosedAt)
				row := nodeRow(t, raw, "MTIX-1")
				if w.ms == foreignWallClock().UnixMilli() {
					require.Equal(t, foreignClosedAt, row["closed_at"], "the event's time in whole seconds")
				} else {
					require.Equal(t, row["updated_at"], row["closed_at"], "the apply time, as updated_at")
					require.False(t, n.ClosedAt.Before(before), "closed_at %s is the apply time", n.ClosedAt)
					require.False(t, n.ClosedAt.After(after), "closed_at %s is the apply time", n.ClosedAt)
				}
				requireAllNodesReadable(t, s, raw)
			})
		}
	}
}

// TestApply_UpdateFieldEventTimeOutsideRange_OrderedAndNodeReadable: the
// field LWW order (detectLWWOutcome) compares wall_clock_ts as an integer
// tie-break key, so an update_field whose wall_clock_ts is outside years
// 1..9999 is ordered like any other, applies, and leaves the node readable.
func TestApply_UpdateFieldEventTimeOutsideRange_OrderedAndNodeReadable(t *testing.T) {
	ctx := context.Background()
	inRange := foreignWallClock().UnixMilli()
	// localEdit edits the title locally and returns that event's Lamport clock.
	localEdit := func(t *testing.T, s *sqlite.Store, raw *sql.DB) int64 {
		title := "local"
		require.NoError(t, s.UpdateNode(ctx, "MTIX-1", &store.NodeUpdate{Title: &title}))
		return latestLamport(t, raw, model.OpUpdateField)
	}
	for _, w := range wallsOutsideRange() {
		tests := []struct {
			name string
			// prepare seeds the replica and returns the incoming event's
			// Lamport clock and wall_clock_ts.
			prepare   func(t *testing.T, s *sqlite.Store, raw *sql.DB) (lamport, wall int64)
			wantTitle string
		}{
			{"no earlier event for the field", func(*testing.T, *sqlite.Store, *sql.DB) (int64, int64) {
				return 2, w.ms
			}, "remote"},
			{"wins a Lamport tie on wall_clock_ts", func(t *testing.T, s *sqlite.Store, raw *sql.DB) (int64, int64) {
				return localEdit(t, s, raw), w.ms
			}, "remote"},
			{"loses on a lower Lamport clock", func(t *testing.T, s *sqlite.Store, raw *sql.DB) (int64, int64) {
				return localEdit(t, s, raw) - 1, w.ms
			}, "local"},
			{"a held event wins a Lamport tie over a lower wall_clock_ts",
				func(t *testing.T, s *sqlite.Store, _ *sql.DB) (int64, int64) {
					pullEvents(t, s, []*model.SyncEvent{
						pulledEvent(t, model.OpUpdateField, "MTIX-1", titleUpdate(t, "local"), 5, w.ms)})
					return 5, inRange
				}, "local"},
		}
		for _, tt := range tests {
			t.Run(w.name+", "+tt.name, func(t *testing.T) {
				s, raw := replicaWithNode(t)
				lamport, wall := tt.prepare(t, s, raw)

				pullEvents(t, s, []*model.SyncEvent{
					pulledEvent(t, model.OpUpdateField, "MTIX-1", titleUpdate(t, "remote"), lamport, wall)})

				n, err := s.GetNode(ctx, "MTIX-1")
				require.NoError(t, err, "GetNode reads the node")
				require.Equal(t, tt.wantTitle, n.Title)
				requireAllNodesReadable(t, s, raw)
			})
		}
	}
}

// TestApply_EveryOpTypeWithEventTimeOutsideRange_NodesStayReadable applies
// one event of every op type with a wall_clock_ts outside years 1..9999:
// each applies, and every node stays readable.
func TestApply_EveryOpTypeWithEventTimeOutsideRange_NodesStayReadable(t *testing.T) {
	blocks := &model.LinkDepPayload{DependsOnNodeID: "MTIX-2", DepType: string(model.DepTypeBlocks)}
	tests := []struct {
		op      model.OpType
		nodeID  string
		payload any
	}{
		{model.OpCreateNode, "MTIX-1.1", &model.CreateNodePayload{Title: "child", ParentID: "MTIX-1"}},
		{model.OpUpdateField, "MTIX-1", titleUpdate(t, "renamed")},
		{model.OpSetAcceptance, "MTIX-1", &model.SetAcceptancePayload{AcceptanceText: "accepted"}},
		{model.OpSetPrompt, "MTIX-1", &model.SetPromptPayload{PromptText: "prompted"}},
		{model.OpTransitionStatus, "MTIX-1", transition(model.StatusOpen, model.StatusDone)},
		{model.OpClaim, "MTIX-1", &model.ClaimPayload{AgentID: "agent-b"}},
		{model.OpUnclaim, "MTIX-1", &model.UnclaimPayload{}},
		{model.OpDefer, "MTIX-1", &model.DeferPayload{Reason: "later"}},
		{model.OpComment, "MTIX-1", &model.CommentPayload{AuthorID: "peer-b", Body: "note"}},
		{model.OpLinkDep, "MTIX-1", blocks},
		{model.OpUnlinkDep, "MTIX-1", &model.UnlinkDepPayload{DependsOnNodeID: "MTIX-2", DepType: blocks.DepType}},
		{model.OpDelete, "MTIX-2", &model.DeletePayload{}},
	}
	for _, w := range wallsOutsideRange() {
		for _, tt := range tests {
			t.Run(string(tt.op)+", "+w.name, func(t *testing.T) {
				s, raw := replicaWithNode(t)
				mustCreateNode(t, s, "MTIX-2", "")

				pullEvents(t, s, []*model.SyncEvent{pulledEvent(t, tt.op, tt.nodeID, tt.payload, 5, w.ms)})

				requireAllNodesReadable(t, s, raw)
			})
		}
	}
}

// TestPackageSource_UnixTimeConversions_OnlyInEventTime: the package turns
// Unix time into a time.Time only inside eventTime, so every stored
// timestamp derived from an event's wall_clock_ts goes through that one
// helper. A new conversion must use eventTime or be reviewed here.
func TestPackageSource_UnixTimeConversions_OnlyInEventTime(t *testing.T) {
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	fset := token.NewFileSet()
	var found []string
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		require.NoError(t, err)
		for _, decl := range f.Decls {
			owner := name
			if fn, ok := decl.(*ast.FuncDecl); ok {
				owner = name + ":" + fn.Name.Name
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				if isUnixTimeConversion(n) {
					found = append(found, owner)
				}
				return true
			})
		}
	}
	require.Equal(t, []string{"sync_event_time.go:eventTime"}, found,
		"only eventTime converts Unix time into a time.Time")
}

// isUnixTimeConversion reports whether n calls time.Unix, time.UnixMilli or
// time.UnixMicro.
func isUnixTimeConversion(n ast.Node) bool {
	call, ok := n.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "time" {
		return false
	}
	switch sel.Sel.Name {
	case "Unix", "UnixMilli", "UnixMicro":
		return true
	default:
		return false
	}
}
