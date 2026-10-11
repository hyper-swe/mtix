// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Parallel cases retain owned fixtures and existing behavior assertions.
package sqlite

// MTIX-107.25: encoding failures preserve FR-18.3 mutation/event atomicity.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
)

type payloadFailureCase struct {
	name    string
	op      model.OpType
	want    any
	failAt  int
	prepare func(*testing.T, *Store)
	run     func(*Store) error
}

func newPayloadTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	s.clock = func() time.Time { return now }
	for _, n := range []*model.Node{
		makeTestNode("TEST-1", "", "TEST", "Parent", 0, 1, now),
		makeTestNode("TEST-1.1", "TEST-1", "TEST", "Mutation target", 1, 1, now),
		makeTestNode("TEST-1.1.1", "TEST-1.1", "TEST", "Descendant", 2, 1, now),
		makeTestNode("TEST-1.2", "TEST-1", "TEST", "Other child", 1, 2, now),
		makeTestNode("TEST-2", "", "TEST", "Dependent", 0, 2, now),
	} {
		n.ContentHash = model.ComputeContentHash(n.Title, "", "", "", nil)
		require.NoError(t, s.CreateNode(context.Background(), n))
	}
	require.NoError(t, s.ClaimNode(context.Background(), "TEST-1.1", "old-agent"))
	require.NoError(t, s.ClaimNode(context.Background(), "TEST-1.2", "other-agent"))
	require.NoError(t, s.TransitionStatus(context.Background(), "TEST-1.2", model.StatusDone, "finished", "other-agent"))
	require.NoError(t, s.SetAnnotations(context.Background(), "TEST-1.1", []model.Annotation{{Text: "prior", Author: "old-agent"}}))
	return s
}

func payloadBlockingDependency(t *testing.T, s *Store) {
	t.Helper()
	require.NoError(t, s.AddDependency(context.Background(), &model.Dependency{FromID: "TEST-1.1", ToID: "TEST-2", DepType: model.DepTypeBlocks}))
}

func payloadIndependentDelete(t *testing.T, s *Store) {
	t.Helper()
	node := makeTestNode("TEST-1.1.2", "TEST-1.1", "TEST", "Earlier independent delete", 2, 2, s.clock())
	require.NoError(t, s.CreateNode(context.Background(), node))
	require.NoError(t, s.DeleteNode(context.Background(), node.ID, true, "prior-agent"))
}

func payloadMutationCases() []payloadFailureCase {
	ctx := context.Background()
	return []payloadFailureCase{
		{"delete cascade", model.OpDelete, &model.DeletePayload{}, 1, payloadIndependentDelete, func(s *Store) error { return s.DeleteNode(ctx, "TEST-1.1", true, "test-agent") }},
		{"cancel cascade", model.OpTransitionStatus, &model.TransitionStatusPayload{From: model.StatusInProgress, To: model.StatusCancelled, Reason: "cancelled"}, 1, payloadBlockingDependency, func(s *Store) error { return s.CancelNode(ctx, "TEST-1.1", "cancelled", "test-agent", true) }},
		{"transition", model.OpTransitionStatus, &model.TransitionStatusPayload{From: model.StatusInProgress, To: model.StatusDone, Reason: "finished"}, 1, payloadBlockingDependency, func(s *Store) error {
			return s.TransitionStatus(ctx, "TEST-1.1", model.StatusDone, "finished", "test-agent")
		}},
		{"add dependency", model.OpLinkDep, &model.LinkDepPayload{DependsOnNodeID: "TEST-2", DepType: "blocks"}, 1, nil, func(s *Store) error {
			return s.AddDependency(ctx, &model.Dependency{FromID: "TEST-1.1", ToID: "TEST-2", DepType: model.DepTypeBlocks})
		}},
		{"remove dependency", model.OpUnlinkDep, &model.UnlinkDepPayload{DependsOnNodeID: "TEST-2", DepType: "blocks"}, 1, payloadBlockingDependency, func(s *Store) error { return s.RemoveDependency(ctx, "TEST-1.1", "TEST-2", model.DepTypeBlocks) }},
		{"unclaim", model.OpUnclaim, &model.UnclaimPayload{}, 1, nil, func(s *Store) error { return s.UnclaimNode(ctx, "TEST-1.1", "released", "test-agent") }},
		{"force reclaim", model.OpClaim, &model.ClaimPayload{AgentID: "new-agent", Forced: true}, 1, func(t *testing.T, s *Store) {
			future := s.clock().Add(48 * time.Hour)
			s.clock = func() time.Time { return future }
		}, func(s *Store) error { return s.ForceReclaimNode(ctx, "TEST-1.1", "new-agent", 24*time.Hour) }},
		{"annotations", model.OpComment, &model.CommentPayload{AuthorID: "test-agent", Body: "new annotation"}, 1, nil, func(s *Store) error {
			return s.SetAnnotations(ctx, "TEST-1.1", []model.Annotation{{Text: "prior", Author: "old-agent"}, {Text: "new annotation", Author: "test-agent"}})
		}},
	}
}

func payloadUpdateCases() []payloadFailureCase {
	acceptance, prompt, title, description, assignee := "new acceptance", "new prompt", "new title", "new description", "updated-agent"
	priority, status, state := model.PriorityHigh, model.StatusDeferred, model.AgentStateStuck
	updates := []struct {
		name string
		u    *store.NodeUpdate
		op   model.OpType
		want any
	}{
		{"acceptance", &store.NodeUpdate{Acceptance: &acceptance}, model.OpSetAcceptance, &model.SetAcceptancePayload{AcceptanceText: acceptance}},
		{"prompt", &store.NodeUpdate{Prompt: &prompt}, model.OpSetPrompt, &model.SetPromptPayload{PromptText: prompt}},
		{"title", &store.NodeUpdate{Title: &title}, model.OpUpdateField, &model.UpdateFieldPayload{FieldName: "title", NewValue: json.RawMessage(`"new title"`)}},
		{"description", &store.NodeUpdate{Description: &description}, model.OpUpdateField, &model.UpdateFieldPayload{FieldName: "description", NewValue: json.RawMessage(`"new description"`)}},
		{"priority", &store.NodeUpdate{Priority: &priority}, model.OpUpdateField, &model.UpdateFieldPayload{FieldName: "priority", NewValue: json.RawMessage(`2`)}},
		{"status", &store.NodeUpdate{Status: &status}, model.OpUpdateField, &model.UpdateFieldPayload{FieldName: "status", NewValue: json.RawMessage(`"deferred"`)}},
		{"labels", &store.NodeUpdate{Labels: []string{"new"}}, model.OpUpdateField, &model.UpdateFieldPayload{FieldName: "labels", NewValue: json.RawMessage(`["new"]`)}},
		{"assignee", &store.NodeUpdate{Assignee: &assignee}, model.OpUpdateField, &model.UpdateFieldPayload{FieldName: "assignee", NewValue: json.RawMessage(`"updated-agent"`)}},
		{"agent state", &store.NodeUpdate{AgentState: &state}, model.OpUpdateField, &model.UpdateFieldPayload{FieldName: "agent_state", NewValue: json.RawMessage(`"stuck"`)}},
	}
	var cases []payloadFailureCase
	for _, u := range updates {
		cases = append(cases, payloadFailureCase{"update " + u.name, u.op, u.want, 1, nil, func(s *Store) error { return s.UpdateNode(context.Background(), "TEST-1.1", u.u) }})
	}
	for _, nth := range []int{2, 3} {
		want, op := any(&model.SetPromptPayload{PromptText: prompt}), model.OpSetPrompt
		if nth == 3 {
			want, op = &model.UpdateFieldPayload{FieldName: "title", NewValue: json.RawMessage(`"new title"`)}, model.OpUpdateField
		}
		cases = append(cases, payloadFailureCase{fmt.Sprintf("late update %d", nth), op, want, nth, nil, func(s *Store) error {
			return s.UpdateNode(context.Background(), "TEST-1.1", &store.NodeUpdate{Acceptance: &acceptance, Prompt: &prompt, Title: &title})
		}})
	}
	return cases
}

func payloadSecondaryCases() []payloadFailureCase {
	ctx := context.Background()
	return []payloadFailureCase{
		{"defer", model.OpTransitionStatus, &model.TransitionStatusPayload{From: model.StatusInProgress, To: model.StatusDeferred, Reason: "waiting"}, 1, nil, func(s *Store) error {
			until := s.clock().Add(time.Hour)
			return s.DeferNode(ctx, "TEST-1.1", &until, "waiting", "test-agent")
		}},
		{"wake", model.OpTransitionStatus, &model.TransitionStatusPayload{From: model.StatusDeferred, To: model.StatusOpen, Reason: "Auto-reopened: defer_until has passed"}, 1, func(t *testing.T, s *Store) {
			past := s.clock().Add(-time.Hour)
			require.NoError(t, s.DeferNode(ctx, "TEST-1.1", &past, "waiting", "test-agent"))
		}, func(s *Store) error {
			woken, err := s.WakeDeferredNode(ctx, "TEST-1.1", s.clock())
			if err != nil && woken {
				return fmt.Errorf("wake claimed success after failure: %w", err)
			}
			return err
		}},
	}
}

func allPayloadCases() []payloadFailureCase {
	return append(append(payloadMutationCases(), payloadUpdateCases()...), payloadSecondaryCases()...)
}

func TestPayloadEncoding_Failure_RollsBackEveryMutation(t *testing.T) {
	t.Parallel()
	for _, tc := range allPayloadCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newPayloadTestStore(t)
			if tc.prepare != nil {
				tc.prepare(t, s)
			}
			before := payloadDatabaseSnapshot(t, s)
			commitCount := 0
			s.AddOnCommit(func() { commitCount++ })
			cause := errors.New("injected payload failure")
			calls := 0
			s.encodePayloadFn = func(value any) (json.RawMessage, error) {
				calls++
				if calls == tc.failAt {
					assert.Equal(t, tc.want, value)
					return nil, cause
				}
				return model.EncodePayload(value)
			}
			err := tc.run(s)
			assert.ErrorIs(t, err, cause)
			assert.ErrorContains(t, err, "encode "+string(tc.op)+" payload for TEST-1.1")
			assert.NotEqual(t, cause, err, "encoding cause must be wrapped")
			assert.Equal(t, tc.failAt, calls, "must exercise the intended encoding")
			assert.Equal(t, before, payloadDatabaseSnapshot(t, s), "all mutation and outbox/clock state must roll back")
			assert.Zero(t, commitCount, "failed transactions cannot dispatch hooks or export")
			if errors.Is(err, cause) {
				s.encodePayloadFn = nil
				require.NoError(t, tc.run(s), "a recovered encoder permits retry on the same store")
				assert.Equal(t, 1, commitCount)
				assert.NotEqual(t, before, payloadDatabaseSnapshot(t, s))
			}
		})
	}
}

// Fixed SQL snapshots compare logical state, including nodes' content/activity,
// agents, cascade provenance, journal/local outbox and transactional clocks.
func payloadDatabaseSnapshot(t *testing.T, s *Store) map[string][][]any {
	t.Helper()
	queries := map[string]string{
		"nodes":         `SELECT * FROM nodes ORDER BY id`,
		"dependencies":  `SELECT * FROM dependencies ORDER BY from_id,to_id,dep_type`,
		"agents":        `SELECT * FROM agents ORDER BY agent_id`,
		"cascade":       `SELECT * FROM cascade_deletes ORDER BY node_id`,
		"events/outbox": `SELECT * FROM sync_events ORDER BY event_id`,
		"applied":       `SELECT * FROM applied_events ORDER BY event_id`,
		"hook origins":  `SELECT * FROM hook_event_origin ORDER BY event_id`,
		"clocks":        `SELECT key,value FROM meta WHERE key IN ('meta.sync.lamport','meta.sync.vector_clock','meta.sync.machine_hash') ORDER BY key`,
	}
	out := map[string][][]any{}
	for name, query := range queries {
		out[name] = payloadRows(t, s.readDB, query)
	}
	return out
}

func payloadRows(t *testing.T, db *sql.DB, query string) [][]any {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), query)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	columns, err := rows.Columns()
	require.NoError(t, err)
	var result [][]any
	for rows.Next() {
		values := make([]any, len(columns))
		dest := make([]any, len(columns))
		for i := range values {
			dest[i] = &values[i]
		}
		require.NoError(t, rows.Scan(dest...))
		for i, v := range values {
			if bytes, ok := v.([]byte); ok {
				values[i] = string(bytes)
			}
		}
		result = append(result, values)
	}
	require.NoError(t, rows.Err())
	return result
}

func TestPayloadEncoding_DefaultEncoder_PreservesTypedPayloads(t *testing.T) {
	t.Parallel()
	for _, tc := range allPayloadCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newPayloadTestStore(t)
			if tc.prepare != nil {
				tc.prepare(t, s)
			}
			before := payloadRows(t, s.readDB, `SELECT event_id FROM sync_events ORDER BY event_id`)
			expected, err := model.EncodePayload(tc.want)
			require.NoError(t, err)
			require.NoError(t, tc.run(s))
			var count int
			require.NoError(t, s.readDB.QueryRow(`SELECT COUNT(*) FROM sync_events WHERE op_type = ? AND payload = ?`, string(tc.op), string(expected)).Scan(&count))
			require.Positive(t, count, "expected typed payload must reach the real journal")
			expectedEvents := 1
			if tc.name == "transition" || tc.name == "cancel cascade" || tc.name == "remove dependency" {
				expectedEvents = 2
			}
			if tc.failAt > 1 {
				expectedEvents = 3
			}
			assert.Equal(t, len(before)+expectedEvents, len(payloadRows(t, s.readDB, `SELECT event_id FROM sync_events ORDER BY event_id`)), "complete event sequence must be recorded")
		})
	}
}

func TestPayloadEncoding_IndependentStores_IsolatedConcurrently(t *testing.T) {
	t.Parallel()
	bad, good := newPayloadTestStore(t), newPayloadTestStore(t)
	cause := errors.New("store-local failure")
	bad.encodePayloadFn = func(any) (json.RawMessage, error) { return nil, cause }
	before := payloadDatabaseSnapshot(t, bad)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, s := range []*Store{bad, good} {
		wg.Add(1)
		go func(s *Store) {
			defer wg.Done()
			results <- s.DeleteNode(context.Background(), "TEST-1.1", true, "test-agent")
		}(s)
	}
	wg.Wait()
	close(results)
	failed, succeeded := 0, 0
	for err := range results {
		if errors.Is(err, cause) {
			failed++
		} else {
			require.NoError(t, err)
			succeeded++
		}
	}
	assert.Equal(t, 1, failed)
	assert.Equal(t, 1, succeeded)
	assert.Equal(t, before, payloadDatabaseSnapshot(t, bad))
	n, err := good.GetNode(context.Background(), "TEST-1.1")
	assert.ErrorIs(t, err, model.ErrNotFound)
	assert.Nil(t, n)
}

func TestPayloadEncoding_NoOp_DoesNotEncode(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		run  func(*Store) error
	}{
		{"empty update", func(s *Store) error { return s.UpdateNode(context.Background(), "TEST-1.1", &store.NodeUpdate{}) }},
		{"same status", func(s *Store) error {
			return s.TransitionStatus(context.Background(), "TEST-1.1", model.StatusInProgress, "same", "test-agent")
		}},
		{"wake not due", func(s *Store) error {
			woken, err := s.WakeDeferredNode(context.Background(), "TEST-1.1", s.clock())
			if woken {
				return errors.New("nondeferred node woke")
			}
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newPayloadTestStore(t)
			before := payloadDatabaseSnapshot(t, s)
			s.encodePayloadFn = func(any) (json.RawMessage, error) {
				t.Error("no-op encoded a payload")
				return nil, errors.New("unexpected encoder")
			}
			require.NoError(t, tc.run(s))
			require.Equal(t, before, payloadDatabaseSnapshot(t, s))
		})
	}
}
