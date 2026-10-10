// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests MCP refusal boundaries through real SQLite services, docs callbacks
// and the sync capability's public error categorization and privacy contract.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/sync/workflow"
)

func TestToolRefusal_MalformedInputs_ReturnParseErrorWithoutResult(t *testing.T) {
	env := newBehaviorTools(t)
	for _, name := range []string{
		"mtix_list", "mtix_show", "mtix_create", "mtix_update", "mtix_briefing",
		"mtix_context", "mtix_prompt", "mtix_annotate", "mtix_resolve_annotation",
		"mtix_claim", "mtix_unclaim", "mtix_done", "mtix_defer", "mtix_cancel", "mtix_reopen",
		"mtix_session_start", "mtix_session_end", "mtix_session_summary", "mtix_agent_heartbeat", "mtix_agent_state", "mtix_agent_work",
		"mtix_progress", "mtix_inbox", "mtix_inbox_wait", "mtix_inbox_ack", "mtix_dep_show",
	} {
		t.Run(name, func(t *testing.T) {
			result, err := env.reg.Call(t.Context(), name, json.RawMessage(`{"broken":`))
			require.Error(t, err)
			require.Contains(t, err.Error(), "parse")
			require.Nil(t, result)
		})
	}
}

func TestToolRefusal_MissingEntities_ReturnNotFoundWithoutResult(t *testing.T) {
	env := newBehaviorTools(t)
	for _, name := range []string{"mtix_show", "mtix_context", "mtix_progress", "mtix_agent_state", "mtix_agent_work", "mtix_session_summary"} {
		t.Run(name, func(t *testing.T) {
			result, err := env.reg.Call(t.Context(), name, json.RawMessage(`{"id":"TEST-9999","agent_id":"absent-worker"}`))
			require.ErrorIs(t, err, model.ErrNotFound)
			require.Nil(t, result)
		})
	}
}

func TestToolRefusal_MissingMutationTarget_LeavesExistingNodeUnchanged(t *testing.T) {
	env := newBehaviorTools(t)
	node := behaviorNode(t, env, "Untouched refusal distractor", "")
	args := json.RawMessage(`{"id":"TEST-9999","agent_id":"test-worker","author":"test-author","text":"Rejected mutation","description":"Rejected description","annotation_id":"absent-annotation","reason":"Rejected workflow"}`)
	for _, name := range []string{"mtix_update", "mtix_prompt", "mtix_annotate", "mtix_resolve_annotation", "mtix_claim", "mtix_unclaim", "mtix_defer", "mtix_cancel", "mtix_reopen"} {
		t.Run(name, func(t *testing.T) {
			result, err := env.reg.Call(t.Context(), name, args)
			require.ErrorIs(t, err, model.ErrNotFound)
			require.Nil(t, result)
			after, err := env.store.GetNode(t.Context(), node.ID)
			require.NoError(t, err)
			require.Equal(t, node.Description, after.Description)
			require.Equal(t, node.Prompt, after.Prompt)
			require.Equal(t, node.Status, after.Status)
			require.Empty(t, after.Annotations)
		})
	}
}

func TestToolRefusal_InvalidTransition_PreservesOriginalNode(t *testing.T) {
	env := newBehaviorTools(t)
	node := behaviorNode(t, env, "Open transition target", "")
	result, err := env.reg.Call(t.Context(), "mtix_done", json.RawMessage(fmt.Sprintf(`{"id":%q}`, node.ID)))
	require.ErrorIs(t, err, model.ErrInvalidTransition)
	require.Nil(t, result)
	after, err := env.store.GetNode(t.Context(), node.ID)
	require.NoError(t, err)
	require.Equal(t, node.Status, after.Status)
	require.Equal(t, node.Progress, after.Progress)
	require.Nil(t, after.ClosedAt)
}

func TestToolRefusal_ClosedOwnedStore_PropagatesFailures(t *testing.T) {
	env := newBehaviorTools(t)
	node := behaviorNode(t, env, "Closed store target", "")
	require.NoError(t, env.agents.EnsureAgent(t.Context(), "test-worker", "TEST"))
	require.NoError(t, env.store.Close())
	for _, tt := range []struct{ name, args string }{
		{"mtix_list", `{}`}, {"mtix_briefing", `{}`}, {"mtix_stats", `{}`}, {"mtix_stale", `{}`}, {"mtix_orphans", `{}`},
		{"mtix_ready", `{}`}, {"mtix_blocked", `{}`}, {"mtix_search", `{"query":"target"}`},
		{"mtix_show", fmt.Sprintf(`{"id":%q}`, node.ID)},
		{"mtix_context", fmt.Sprintf(`{"id":%q}`, node.ID)},
		{"mtix_progress", fmt.Sprintf(`{"id":%q}`, node.ID)},
		{"mtix_dep_show", fmt.Sprintf(`{"id":%q}`, node.ID)},
		{"mtix_session_start", `{"agent_id":"test-worker","project":"TEST"}`},
		{"mtix_session_end", `{"agent_id":"test-worker"}`},
		{"mtix_session_summary", `{"agent_id":"test-worker"}`},
		{"mtix_agent_state", `{"agent_id":"test-worker","state":"working"}`},
		{"mtix_agent_heartbeat", `{"agent_id":"test-worker"}`},
		{"mtix_agent_state", `{"agent_id":"test-worker"}`},
		{"mtix_agent_work", `{"agent_id":"test-worker"}`},
		{"mtix_inbox", `{"agent":"test-worker"}`},
		{"mtix_inbox_ack", `{"agent":"test-worker","seq":1}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			result, err := env.reg.Call(t.Context(), tt.name, json.RawMessage(tt.args))
			require.Error(t, err)
			require.Contains(t, err.Error(), "closed")
			require.Nil(t, result)
		})
	}
}

func TestToolRefusal_CancelledInboxWait_ReturnsImmediatelyWithoutResult(t *testing.T) {
	env := newBehaviorTools(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err := env.reg.Call(ctx, "mtix_inbox_wait", json.RawMessage(`{"agent":"test-worker","timeout_seconds":60}`))
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, result)
}

func TestToolDocs_Generator_ReceivesForceAndReturnsExactSummary(t *testing.T) {
	for _, force := range []bool{false, true} {
		t.Run(fmt.Sprint(force), func(t *testing.T) {
			reg := NewToolRegistry()
			calls := 0
			RegisterDocsTools(reg, func(got bool) (string, error) {
				calls++
				require.Equal(t, force, got)
				return "Generated owned test docs", nil
			})
			require.Equal(t, "Generated owned test docs", behaviorCall(t, reg, "mtix_docs_generate", fmt.Sprintf(`{"force":%t}`, force)))
			require.Equal(t, 1, calls)
		})
	}
}

func TestToolDocs_GeneratorFailure_PreservesErrorAndNoSuccess(t *testing.T) {
	reg := NewToolRegistry()
	failure := errors.New("owned generator refused")
	calls := 0
	RegisterDocsTools(reg, func(force bool) (string, error) {
		calls++
		require.True(t, force)
		return "must not be returned", failure
	})
	result, err := reg.Call(t.Context(), "mtix_docs_generate", json.RawMessage(`{"force":true}`))
	require.ErrorIs(t, err, failure)
	require.EqualError(t, err, "docs generate: owned generator refused")
	require.Nil(t, result)
	require.Equal(t, 1, calls)
}

type failingBehaviorSync struct {
	err   error
	dir   string
	calls int
}

func (s *failingBehaviorSync) DetectState(_ context.Context, dir string) (workflow.Report, error) {
	s.dir = dir
	s.calls++
	return workflow.Report{}, s.err
}

func TestToolPrivacy_SyncFailure_ReturnsCategoryWithoutSensitiveMarkers(t *testing.T) {
	const path = "/owned-test/private-path"
	const dsn = "postgres://fake-user:fake-password@invalid.example/test"
	for _, tt := range []struct{ tag, category string }{
		{"machine_hash", "could not read sync metadata"},
		{"consecutive_errors", "could not read sync metadata"},
		{"sync_events", "could not count sync events"},
		{"applied_events", "could not count sync events"},
		{"conflicts", "could not count sync events"},
		{"unknown", "internal error"},
	} {
		t.Run(tt.tag, func(t *testing.T) {
			svc := &failingBehaviorSync{err: fmt.Errorf("%s: %s %s", tt.tag, path, dsn)}
			reg := NewToolRegistry()
			RegisterSyncWorkflowTool(reg, svc, path)
			result, err := reg.Call(t.Context(), "mtix_sync_workflow", json.RawMessage(`{}`))
			require.NoError(t, err)
			require.NotNil(t, result)
			require.True(t, result.IsError)
			require.Len(t, result.Content, 1)
			require.Equal(t, "text", result.Content[0].Type)
			require.Equal(t, "sync state detection failed: "+tt.category, result.Content[0].Text)
			for _, marker := range []string{path, dsn, "fake-password", "invalid.example"} {
				require.NotContains(t, result.Content[0].Text, marker)
			}
			require.Equal(t, path, svc.dir)
			require.Equal(t, 1, svc.calls)
		})
	}
}
