// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/testutil"
)

// newInboxTestStore opens a real on-disk SQLite store (with migrations) so the
// inbox tools query the same event journal the annotate tool writes to.
func newInboxTestStore(t *testing.T) *sqlite.Store {
	t.Helper()
	testutil.OperatorState(t)
	dbPath := filepath.Join(t.TempDir(), "inbox.db")
	s, err := sqlite.New(dbPath, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	return s
}

func inboxSeedNode(t *testing.T, s *sqlite.Store, id string) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, s.CreateNode(context.Background(), &model.Node{
		ID: id, Project: "PROJ", Depth: 0, Seq: 1, Title: "Task",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeStory, Creator: "test-agent",
		CreatedAt: now, UpdatedAt: now,
	}))
}

// inboxEnvelopeResult reads the tool's text contract, not a raw event array.
func inboxEnvelopeResult(t *testing.T, res *ToolsCallResult) string {
	t.Helper()
	require.NotNil(t, res)
	require.False(t, res.IsError)
	require.Len(t, res.Content, 1)
	return res.Content[0].Text
}

// TestInboxTools_AnnotateTo_ListAck: mtix_annotate with `to` lands in the
// addressee's inbox; mtix_inbox lists it and mtix_inbox_ack clears it (FR-19.5).
func TestInboxTools_AnnotateTo_ListAck(t *testing.T) {
	s := newInboxTestStore(t)
	inboxSeedNode(t, s, "PROJ-1")

	reg := NewToolRegistry()
	promptSvc := service.NewPromptService(s, nil, slog.Default(), fixedClock)
	RegisterContextTools(reg, newTestContextService(), promptSvc)
	RegisterInboxTools(reg, testInboxService(s))

	ctx := context.Background()

	// Address a comment at agent "opus" via the annotate tool.
	_, err := reg.Call(ctx, "mtix_annotate", json.RawMessage(
		`{"id":"PROJ-1","text":"ruling: proceed","author":"worker","to":"opus"}`))
	require.NoError(t, err)

	// It lands in opus's inbox — assert directly via the store foundation.
	direct, err := s.InboxList(ctx, "opus")
	require.NoError(t, err)
	require.Len(t, direct, 1)
	require.Equal(t, "ruling: proceed", direct[0].Body)

	// mtix_inbox surfaces the same event.
	res, err := reg.Call(ctx, "mtix_inbox", json.RawMessage(`{"agent":"opus"}`))
	require.NoError(t, err)
	text := inboxEnvelopeResult(t, res)
	assert.Contains(t, text, `task: "PROJ-1"`)
	assert.Contains(t, text, `body: "ruling: proceed"`)
	assert.Contains(t, text, "as claimed, not verified")

	// A different agent sees nothing.
	other, err := reg.Call(ctx, "mtix_inbox", json.RawMessage(`{"agent":"someone-else"}`))
	require.NoError(t, err)
	assert.Equal(t, "[]", inboxEnvelopeResult(t, other))

	// Ack advances the cursor; the inbox then clears.
	seqArg, _ := json.Marshal(map[string]any{"agent": "opus", "seq": direct[0].Seq})
	_, err = reg.Call(ctx, "mtix_inbox_ack", seqArg)
	require.NoError(t, err)

	after, err := reg.Call(ctx, "mtix_inbox", json.RawMessage(`{"agent":"opus"}`))
	require.NoError(t, err)
	assert.Equal(t, "[]", inboxEnvelopeResult(t, after))
}

// TestInboxTools_Annotate_WithoutTo_NoInbox: an ordinary comment (no `to`) does
// not land in any inbox.
func TestInboxTools_Annotate_WithoutTo_NoInbox(t *testing.T) {
	s := newInboxTestStore(t)
	inboxSeedNode(t, s, "PROJ-1")

	reg := NewToolRegistry()
	promptSvc := service.NewPromptService(s, nil, slog.Default(), fixedClock)
	RegisterContextTools(reg, newTestContextService(), promptSvc)

	_, err := reg.Call(context.Background(), "mtix_annotate", json.RawMessage(
		`{"id":"PROJ-1","text":"just a note","author":"worker"}`))
	require.NoError(t, err)

	got, err := s.InboxList(context.Background(), "opus")
	require.NoError(t, err)
	assert.Empty(t, got)
}

// TestInboxWaitTool_TimesOutEmpty: an empty inbox returns [] (not null) within
// the requested window — the signal for the caller to re-invoke.
func TestInboxWaitTool_TimesOutEmpty(t *testing.T) {
	s := newInboxTestStore(t)

	reg := NewToolRegistry()
	RegisterInboxTools(reg, testInboxService(s))

	start := time.Now()
	res, err := reg.Call(context.Background(), "mtix_inbox_wait",
		json.RawMessage(`{"agent":"nobody","timeout_seconds":1}`))
	require.NoError(t, err)
	assert.GreaterOrEqual(t, time.Since(start), 900*time.Millisecond)

	require.Len(t, res.Content, 1)
	assert.Equal(t, "[]", res.Content[0].Text) // empty array, never null
	assert.Equal(t, "[]", inboxEnvelopeResult(t, res))
}

// TestInboxWaitTool_ReturnsAddressedEvent: a comment already waiting is returned
// immediately by mtix_inbox_wait.
func TestInboxWaitTool_ReturnsAddressedEvent(t *testing.T) {
	s := newInboxTestStore(t)
	inboxSeedNode(t, s, "PROJ-1")

	reg := NewToolRegistry()
	promptSvc := service.NewPromptService(s, nil, slog.Default(), fixedClock)
	RegisterContextTools(reg, newTestContextService(), promptSvc)
	RegisterInboxTools(reg, testInboxService(s))

	ctx := context.Background()
	_, err := reg.Call(ctx, "mtix_annotate", json.RawMessage(
		`{"id":"PROJ-1","text":"wake up","author":"worker","to":"opus"}`))
	require.NoError(t, err)

	res, err := reg.Call(ctx, "mtix_inbox_wait",
		json.RawMessage(`{"agent":"opus","timeout_seconds":5}`))
	require.NoError(t, err)
	text := inboxEnvelopeResult(t, res)
	assert.Contains(t, text, `body: "wake up"`)
	assert.Contains(t, text, "ack: mtix inbox ack")
}

// TestRegisterInboxTools_RegistersExpectedTools verifies inbox tool registration.
func TestRegisterInboxTools_RegistersExpectedTools(t *testing.T) {
	reg := NewToolRegistry()
	RegisterInboxTools(reg, testInboxService(newInboxTestStore(t)))

	expectedNames := []string{"mtix_inbox", "mtix_inbox_wait", "mtix_inbox_ack"}
	assert.Equal(t, len(expectedNames), reg.Count())

	names := make(map[string]bool)
	for _, def := range reg.List() {
		names[def.Name] = true
	}
	for _, name := range expectedNames {
		assert.True(t, names[name], "expected tool %q to be registered", name)
	}
}
