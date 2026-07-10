// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// newTestPromptService creates a PromptService backed by real SQLite for integration tests.
func newTestPromptService(t *testing.T) (
	*service.PromptService, *service.NodeService, *sqlite.Store, *recordingBroadcaster,
) {
	t.Helper()

	s, err := sqlite.New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	bc := newRecordingBroadcaster()
	cfg := &service.StaticConfig{AutoClaimEnabled: false}
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	clock := fixedClock(now)
	logger := slog.Default()

	nodeSvc := service.NewNodeService(s, bc, cfg, logger, clock)
	promptSvc := service.NewPromptService(s, bc, logger, clock)

	return promptSvc, nodeSvc, s, bc
}

// TestUpdatePrompt_ChangesPromptField verifies prompt update.
func TestUpdatePrompt_ChangesPromptField(t *testing.T) {
	promptSvc, nodeSvc, s, _ := newTestPromptService(t)
	ctx := context.Background()

	node, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Prompt Test", Prompt: "Original", Creator: "admin",
	})
	require.NoError(t, err)

	err = promptSvc.UpdatePrompt(ctx, node.ID, "Updated prompt", "editor@test.com")
	require.NoError(t, err)

	got, err := s.GetNode(ctx, node.ID)
	require.NoError(t, err)
	assert.Equal(t, "Updated prompt", got.Prompt)
}

// TestUpdatePrompt_RecomputesContentHash verifies hash update.
func TestUpdatePrompt_RecomputesContentHash(t *testing.T) {
	promptSvc, nodeSvc, s, _ := newTestPromptService(t)
	ctx := context.Background()

	node, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Hash Test", Prompt: "Original", Creator: "admin",
	})
	require.NoError(t, err)

	// Get original hash.
	original, err := s.GetNode(ctx, node.ID)
	require.NoError(t, err)
	oldHash := original.ContentHash

	err = promptSvc.UpdatePrompt(ctx, node.ID, "Different prompt", "editor@test.com")
	require.NoError(t, err)

	got, err := s.GetNode(ctx, node.ID)
	require.NoError(t, err)
	assert.NotEqual(t, oldHash, got.ContentHash)
	assert.NotEmpty(t, got.ContentHash)
}

// TestUpdatePrompt_CreatesActivityEntry verifies activity recording.
func TestUpdatePrompt_CreatesActivityEntry(t *testing.T) {
	promptSvc, nodeSvc, _, bc := newTestPromptService(t)
	ctx := context.Background()

	node, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Activity Test", Prompt: "Original", Creator: "admin",
	})
	require.NoError(t, err)

	bc.Reset()
	err = promptSvc.UpdatePrompt(ctx, node.ID, "Updated", "editor@test.com")
	require.NoError(t, err)

	events := bc.Events()
	require.GreaterOrEqual(t, len(events), 1)

	var found bool
	for _, e := range events {
		if e.Type == service.EventNodeUpdated {
			found = true
			break
		}
	}
	assert.True(t, found, "should broadcast node.updated event")
}

// TestAddAnnotation_GeneratesULID verifies ULID generation.
func TestAddAnnotation_GeneratesULID(t *testing.T) {
	promptSvc, nodeSvc, s, _ := newTestPromptService(t)
	ctx := context.Background()

	node, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Annotation Test", Creator: "admin",
	})
	require.NoError(t, err)

	err = promptSvc.AddAnnotation(ctx, node.ID, "Review this section", "reviewer@test.com", "")
	require.NoError(t, err)

	got, err := s.GetNode(ctx, node.ID)
	require.NoError(t, err)
	require.Len(t, got.Annotations, 1)
	assert.Len(t, got.Annotations[0].ID, 26, "ULID should be 26 characters")
	assert.Equal(t, "Review this section", got.Annotations[0].Text)
	assert.Equal(t, "reviewer@test.com", got.Annotations[0].Author)
	assert.False(t, got.Annotations[0].Resolved)
}

// TestAddAnnotation_AppendsToArray verifies annotations append.
func TestAddAnnotation_AppendsToArray(t *testing.T) {
	promptSvc, nodeSvc, s, _ := newTestPromptService(t)
	ctx := context.Background()

	node, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Multi-Annotation", Creator: "admin",
	})
	require.NoError(t, err)

	err = promptSvc.AddAnnotation(ctx, node.ID, "First annotation", "reviewer1@test.com", "")
	require.NoError(t, err)

	err = promptSvc.AddAnnotation(ctx, node.ID, "Second annotation", "reviewer2@test.com", "")
	require.NoError(t, err)

	got, err := s.GetNode(ctx, node.ID)
	require.NoError(t, err)
	require.Len(t, got.Annotations, 2)
	assert.Equal(t, "First annotation", got.Annotations[0].Text)
	assert.Equal(t, "Second annotation", got.Annotations[1].Text)
}

// TestAddAnnotation_AtTokenSetsAddressee verifies an @<agent> token in the text
// sets the addressee (FR-19.1) and the comment lands in that agent's inbox.
func TestAddAnnotation_AtTokenSetsAddressee(t *testing.T) {
	promptSvc, nodeSvc, s, _ := newTestPromptService(t)
	ctx := context.Background()

	node, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Mention Test", Creator: "admin",
	})
	require.NoError(t, err)

	err = promptSvc.AddAnnotation(ctx, node.ID, "please review @worker-7 thanks", "reviewer", "")
	require.NoError(t, err)

	// Token stays visible in the stored text — it is not stripped.
	got, err := s.GetNode(ctx, node.ID)
	require.NoError(t, err)
	require.Len(t, got.Annotations, 1)
	assert.Equal(t, "please review @worker-7 thanks", got.Annotations[0].Text)
	assert.Equal(t, "worker-7", got.Annotations[0].Addressee)

	// The comment is delivered to the mentioned agent's inbox.
	inbox, err := s.InboxList(ctx, "worker-7")
	require.NoError(t, err)
	require.Len(t, inbox, 1)
	assert.Equal(t, "please review @worker-7 thanks", inbox[0].Body)
}

// TestAddAnnotation_ExplicitAddresseeOverridesAtToken verifies the explicit
// addressee arg takes precedence over an @<agent> token in the text.
func TestAddAnnotation_ExplicitAddresseeOverridesAtToken(t *testing.T) {
	promptSvc, nodeSvc, s, _ := newTestPromptService(t)
	ctx := context.Background()

	node, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Override Test", Creator: "admin",
	})
	require.NoError(t, err)

	err = promptSvc.AddAnnotation(ctx, node.ID, "ping @token-agent", "reviewer", "explicit-agent")
	require.NoError(t, err)

	got, err := s.GetNode(ctx, node.ID)
	require.NoError(t, err)
	require.Len(t, got.Annotations, 1)
	assert.Equal(t, "explicit-agent", got.Annotations[0].Addressee)

	inbox, err := s.InboxList(ctx, "explicit-agent")
	require.NoError(t, err)
	require.Len(t, inbox, 1)

	// The @token agent gets nothing — the explicit arg won.
	tokenInbox, err := s.InboxList(ctx, "token-agent")
	require.NoError(t, err)
	assert.Empty(t, tokenInbox)
}

// TestAddAnnotation_NoAddressee verifies a plain comment (no @token, no arg)
// sets no addressee and lands in nobody's inbox.
func TestAddAnnotation_NoAddressee(t *testing.T) {
	promptSvc, nodeSvc, s, _ := newTestPromptService(t)
	ctx := context.Background()

	node, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Plain Comment", Creator: "admin",
	})
	require.NoError(t, err)

	err = promptSvc.AddAnnotation(ctx, node.ID, "an email like foo@bar is not a mention", "reviewer", "")
	require.NoError(t, err)

	got, err := s.GetNode(ctx, node.ID)
	require.NoError(t, err)
	require.Len(t, got.Annotations, 1)
	assert.Empty(t, got.Annotations[0].Addressee)

	inbox, err := s.InboxList(ctx, "bar")
	require.NoError(t, err)
	assert.Empty(t, inbox)
}

// TestParseAddressee covers the @<agent> token grammar and boundary rules.
func TestParseAddressee(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{"leading", "@alice do this", "alice"},
		{"mid-sentence", "hey @bob-2 look", "bob-2"},
		{"first of many", "@first then @second", "first"},
		{"trailing-punct", "ruling for @carol.", "carol"},
		{"in-parens", "(@dave) please", "dave"},
		{"email-not-mention", "mail me at foo@bar.com", ""},
		{"uppercase-rejected", "@Bob is not valid", ""},
		{"no-mention", "just a plain comment", ""},
		{"underscore-id", "@build_bot go", "build_bot"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, service.ParseAddressee(tc.text))
		})
	}
}

// TestResolveAnnotation_SetsResolvedTrue verifies resolve.
func TestResolveAnnotation_SetsResolvedTrue(t *testing.T) {
	promptSvc, nodeSvc, s, _ := newTestPromptService(t)
	ctx := context.Background()

	node, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Resolve Test", Creator: "admin",
	})
	require.NoError(t, err)

	err = promptSvc.AddAnnotation(ctx, node.ID, "Needs review", "reviewer@test.com", "")
	require.NoError(t, err)

	// Get annotation ID.
	got, err := s.GetNode(ctx, node.ID)
	require.NoError(t, err)
	require.Len(t, got.Annotations, 1)
	annotID := got.Annotations[0].ID

	err = promptSvc.ResolveAnnotation(ctx, node.ID, annotID, true)
	require.NoError(t, err)

	got, err = s.GetNode(ctx, node.ID)
	require.NoError(t, err)
	assert.True(t, got.Annotations[0].Resolved)
}

// TestResolveAnnotation_UnresolveSetsResolvedFalse verifies unresolve.
func TestResolveAnnotation_UnresolveSetsResolvedFalse(t *testing.T) {
	promptSvc, nodeSvc, s, _ := newTestPromptService(t)
	ctx := context.Background()

	node, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Unresolve Test", Creator: "admin",
	})
	require.NoError(t, err)

	err = promptSvc.AddAnnotation(ctx, node.ID, "Needs review", "reviewer@test.com", "")
	require.NoError(t, err)

	got, err := s.GetNode(ctx, node.ID)
	require.NoError(t, err)
	annotID := got.Annotations[0].ID

	// Resolve then unresolve.
	require.NoError(t, promptSvc.ResolveAnnotation(ctx, node.ID, annotID, true))
	require.NoError(t, promptSvc.ResolveAnnotation(ctx, node.ID, annotID, false))

	got, err = s.GetNode(ctx, node.ID)
	require.NoError(t, err)
	assert.False(t, got.Annotations[0].Resolved)
}

// TestResolveAnnotation_NonExistentID_ReturnsNotFound verifies error.
func TestResolveAnnotation_NonExistentID_ReturnsNotFound(t *testing.T) {
	promptSvc, nodeSvc, _, _ := newTestPromptService(t)
	ctx := context.Background()

	node, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Missing Annotation", Creator: "admin",
	})
	require.NoError(t, err)

	err = promptSvc.ResolveAnnotation(ctx, node.ID, "NONEXISTENT", true)
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// TestUpdatePrompt_NonExistentNode_ReturnsError verifies error.
func TestUpdatePrompt_NonExistentNode_ReturnsError(t *testing.T) {
	promptSvc, _, _, _ := newTestPromptService(t)
	ctx := context.Background()

	err := promptSvc.UpdatePrompt(ctx, "NONEXISTENT", "text", "author")
	assert.Error(t, err)
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// TestAddAnnotation_NonExistentNode_ReturnsError verifies error.
func TestAddAnnotation_NonExistentNode_ReturnsError(t *testing.T) {
	promptSvc, _, _, _ := newTestPromptService(t)
	ctx := context.Background()

	err := promptSvc.AddAnnotation(ctx, "NONEXISTENT", "note", "author", "")
	assert.Error(t, err)
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// TestResolveAnnotation_NonExistentNode_ReturnsError verifies error.
func TestResolveAnnotation_NonExistentNode_ReturnsError(t *testing.T) {
	promptSvc, _, _, _ := newTestPromptService(t)
	ctx := context.Background()

	err := promptSvc.ResolveAnnotation(ctx, "NONEXISTENT", "ann-id", true)
	assert.Error(t, err)
	assert.ErrorIs(t, err, model.ErrNotFound)
}
