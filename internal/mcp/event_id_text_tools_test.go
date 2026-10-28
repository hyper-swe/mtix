// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// hostileSyncID is a sync event id with a terminal escape, a newline and a
// bidirectional override. A pulled create event becomes a node uid and a
// pulled comment event becomes an annotation id (ADR-003), so both carry it.
const hostileSyncID = "evt\x1b[31mred\x1b[0m\nINJECTED-\u202eg-\u00e9\u4e16"

// hostileAnnotationID is the id of a pulled comment event.
const hostileAnnotationID = hostileSyncID + "-note"

// applyHostileEvent applies one real pulled event through the store.
func applyHostileEvent(t *testing.T, env behaviorTools, id string, op model.OpType, payload any, lamport int64) {
	t.Helper()
	pl, err := model.EncodePayload(payload)
	require.NoError(t, err)
	ev := &model.SyncEvent{
		EventID: id, ProjectPrefix: "TEST", NodeID: "TEST-1", OpType: op,
		Payload: pl, WallClockTS: lamport, LamportClock: lamport,
		VectorClock: model.VectorClock{"remote": lamport},
		AuthorID:    "remote", AuthorMachineHash: "0123456789abcdef", SyncStatus: model.SyncStatusApplied,
	}
	require.NoError(t, env.store.WithTx(context.Background(), func(tx *sql.Tx) error {
		return sqlite.IdempotentApply(context.Background(), tx, ev)
	}))
}

// seedHostileSyncNode applies a pulled create event and a pulled comment
// event with hostile ids through the real apply path. The create event id
// becomes the node uid and the comment event id the annotation id
// (ADR-003). It returns the node id.
func seedHostileSyncNode(t *testing.T, env behaviorTools) string {
	t.Helper()
	applyHostileEvent(t, env, hostileSyncID, model.OpCreateNode, &model.CreateNodePayload{Title: "Synced target"}, 1)
	applyHostileEvent(t, env, hostileAnnotationID, model.OpComment,
		&model.CommentPayload{AuthorID: "remote", Body: "pulled comment"}, 2)
	return "TEST-1"
}

// assertSafeEventText holds a tool result to the surface contract: JSON
// (every id JSON-encoded, the original value intact) or printable ASCII
// text that never repeats an id raw.
func assertSafeEventText(t *testing.T, text string) {
	t.Helper()
	if json.Valid([]byte(text)) {
		return
	}
	assert.NotContains(t, text, hostileSyncID)
	assert.NotContains(t, text, hostileAnnotationID)
	for i := 0; i < len(text); i++ {
		if (text[i] < 0x20 && text[i] != '\n') || text[i] > 0x7e {
			t.Fatalf("raw byte 0x%02x at %d in tool text %q", text[i], i, text)
		}
	}
}

// TestTools_HostileAnnotationIDAndUID_TextIsSafe drives the real service and
// store with an annotation id and a node uid that are pulled event ids, and
// checks every tool that can print either one.
func TestTools_HostileAnnotationIDAndUID_TextIsSafe(t *testing.T) {
	env := newBehaviorTools(t)
	id := seedHostileSyncNode(t, env)
	calls := []struct{ tool, args string }{
		{"mtix_resolve_annotation", jsonArgs(t, "id", id, "annotation_id", hostileAnnotationID, "author", "a")},
		{"mtix_show", jsonArgs(t, "id", id)},
		{"mtix_show", jsonArgs(t, "id", hostileSyncID)},
		{"mtix_context", jsonArgs(t, "id", id)},
		{"mtix_progress", jsonArgs(t, "id", id)},
		{"mtix_dep_show", jsonArgs(t, "id", id)},
		{"mtix_list", `{}`},
		{"mtix_search", `{"query":"Synced"}`},
		{"mtix_briefing", `{}`},
	}
	for _, c := range calls {
		t.Run(c.tool, func(t *testing.T) {
			res, err := env.reg.Call(context.Background(), c.tool, json.RawMessage(c.args))
			require.NoError(t, err)
			require.NotNil(t, res)
			require.Len(t, res.Content, 1)
			assertSafeEventText(t, res.Content[0].Text)
		})
	}
}

// TestResolveAnnotation_HostileID_TextIsQuotedAndRoundTrips pins the exact
// success text and shows the stored annotation is unchanged.
func TestResolveAnnotation_HostileID_TextIsQuotedAndRoundTrips(t *testing.T) {
	env := newBehaviorTools(t)
	id := seedHostileSyncNode(t, env)
	out := behaviorCall(t, env.reg, "mtix_resolve_annotation",
		jsonArgs(t, "id", id, "annotation_id", hostileAnnotationID, "author", "a"))
	assert.Equal(t, "Annotation "+format.EventID(hostileAnnotationID)+" resolved on "+id, out)
	assert.Contains(t, out, `\x1b[31m`)
	assert.Contains(t, out, `\u202e`)
	assert.Contains(t, out, `\u00e9`)
	assert.Contains(t, out, `\u4e16`)
	n, err := env.store.GetNode(context.Background(), id)
	require.NoError(t, err)
	require.Len(t, n.Annotations, 1)
	assert.Equal(t, hostileAnnotationID, n.Annotations[0].ID)
	assert.True(t, n.Annotations[0].Resolved)
}

// TestShow_HostileUID_JSONKeepsTheExactValue shows the JSON field carries the
// raw uid, so the render-only rule leaves the wire contract alone.
func TestShow_HostileUID_JSONKeepsTheExactValue(t *testing.T) {
	env := newBehaviorTools(t)
	id := seedHostileSyncNode(t, env)
	out := behaviorCall(t, env.reg, "mtix_show", jsonArgs(t, "id", id))
	var got struct {
		UID         string `json:"uid"`
		Annotations []struct {
			ID string `json:"id"`
		} `json:"annotations"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	assert.Equal(t, hostileSyncID, got.UID)
	require.Len(t, got.Annotations, 1)
	assert.Equal(t, hostileAnnotationID, got.Annotations[0].ID)
}

// TestResolveNodeRef_UnknownHostileUID_ErrorIsPrintableASCII covers the
// uid lookup miss that echoes the reference.
func TestResolveNodeRef_UnknownHostileUID_ErrorIsPrintableASCII(t *testing.T) {
	env := newBehaviorTools(t)
	_, err := resolveNodeRef(context.Background(), env.nodes, hostileSyncID)
	require.Error(t, err)
	assert.False(t, strings.ContainsAny(err.Error(), "\x1b\u202e"))
	assertSafeEventText(t, err.Error())
}

// TestResolveAnnotation_UnknownHostileID_ErrorIsPrintableASCII covers the
// miss path. It passes only with the store error rendered (MTIX-187.4.3).
func TestResolveAnnotation_UnknownHostileID_ErrorIsPrintableASCII(t *testing.T) {
	env := newBehaviorTools(t)
	node := behaviorNode(t, env, "No such annotation", "")
	res, err := env.reg.Call(context.Background(), "mtix_resolve_annotation",
		json.RawMessage(jsonArgs(t, "id", node.ID, "annotation_id", hostileAnnotationID, "author", "a")))
	if err != nil {
		assertSafeEventText(t, err.Error())
		return
	}
	require.True(t, res.IsError)
	assertSafeEventText(t, res.Content[0].Text)
}

// jsonArgs builds a tool argument object from key, value string pairs, with
// JSON escaping so hostile values survive.
func jsonArgs(t *testing.T, kv ...string) string {
	t.Helper()
	m := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	b, err := json.Marshal(m)
	require.NoError(t, err)
	return string(b)
}

// TestInbox_HostilePulledCommentEventID_NeverAppearsInEnvelope applies a
// pulled comment addressed to an agent, with a hostile event id, and reads
// the real inbox tool: the envelope names the task, sender and body, never
// the event id, whether raw or quoted.
func TestInbox_HostilePulledCommentEventID_NeverAppearsInEnvelope(t *testing.T) {
	env := newBehaviorTools(t)
	applyHostileEvent(t, env, hostileSyncID, model.OpCreateNode, &model.CreateNodePayload{Title: "Synced"}, 1)
	applyHostileEvent(t, env, hostileAnnotationID, model.OpComment,
		&model.CommentPayload{AuthorID: "remote", Body: "please review", To: "worker-1"}, 2)

	res, err := env.reg.Call(context.Background(), "mtix_inbox", json.RawMessage(`{"agent":"worker-1"}`))
	require.NoError(t, err)
	require.False(t, res.IsError, res.Content[0].Text)
	text := res.Content[0].Text
	assert.Contains(t, text, "please review")
	assert.NotContains(t, text, hostileAnnotationID)
	assert.NotContains(t, text, "INJECTED")
	assert.NotContains(t, text, `\x1b[31m`)
	assertSafeEventText(t, text)
}
