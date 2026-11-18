// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// TestRunDecompose_CreatorIdentity_ClaimsOnlyForParentAssignee verifies FR-11.2a
// for mtix decompose: the children are created as the process identity
// (MTIX_AUTHOR_ID), not a fixed "cli", and are claimed only when that identity
// is the in_progress parent's assignee. The parent's creator ("planner")
// differs from its assignee ("owner").
func TestRunDecompose_CreatorIdentity_ClaimsOnlyForParentAssignee(t *testing.T) {
	tests := []struct {
		name         string
		author       string
		wantStatus   model.Status
		wantAssignee string
	}{
		{"author is the claiming agent", "owner", model.StatusInProgress, "owner"},
		{"different author", "other", model.StatusOpen, ""},
		{"no identity falls back to cli", "", model.StatusOpen, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(sqlite.AuthorIDEnv, tt.author)
			initTestApp(t)
			ctx := context.Background()
			parent, err := app.nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{Project: "TEST", Title: "P", Creator: "planner"})
			require.NoError(t, err)
			require.NoError(t, app.store.ClaimNode(ctx, parent.ID, "owner"))

			require.NoError(t, runDecompose(parent.ID, []string{"A", "B"}))

			for _, id := range []string{parent.ID + ".1", parent.ID + ".2"} {
				got, err := app.store.GetNode(ctx, id)
				require.NoError(t, err)
				assert.Equal(t, tt.wantStatus, got.Status, id)
				assert.Equal(t, tt.wantAssignee, got.Assignee, id)
			}
		})
	}
}
