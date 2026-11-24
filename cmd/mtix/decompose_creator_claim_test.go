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

// Create and decompose distinguish a configured author from CLI audit attribution (FR-11.2a).
func TestRunDecompose_CreatorIdentity_ClaimsOnlyForParentAssignee(t *testing.T) {
	tests := []struct {
		name         string
		author       string
		parentOwner  string
		wantCreator  string
		wantStatus   model.Status
		wantAssignee string
	}{
		{"author is the claiming agent", "owner", "owner", "owner", model.StatusInProgress, "owner"},
		{"different author", "other", "owner", "other", model.StatusOpen, ""},
		{"no identity falls back to cli", "", "owner", "cli", model.StatusOpen, ""},
		{"fallback equals parent assignee", "", "cli", "cli", model.StatusOpen, ""},
		{"configured cli owns parent", "cli", "cli", "cli", model.StatusInProgress, "cli"},
		{"unclaimed parent", "owner", "", "owner", model.StatusOpen, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(sqlite.AuthorIDEnv, tt.author)
			initTestApp(t)
			ctx := context.Background()
			// Use a fixed sync identity so claim tests need no host network interfaces.
			_, seedErr := app.store.WriteDB().ExecContext(t.Context(), "UPDATE meta SET value = ? WHERE key = ?", "0123456789abcdef", "meta.sync.machine_hash")
			require.NoError(t, seedErr)
			parent, err := app.nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{Project: "TEST", Title: "P", Creator: "planner"})
			require.NoError(t, err)
			if tt.parentOwner != "" {
				require.NoError(t, app.store.ClaimNode(ctx, parent.ID, tt.parentOwner))
			}
			require.NoError(t, runCreate("Single", parent.ID, "", 3, "", "", "", "", ""))

			require.NoError(t, runDecompose(parent.ID, []string{"A", "B"}))

			for _, id := range []string{parent.ID + ".1", parent.ID + ".2", parent.ID + ".3"} {
				got, err := app.store.GetNode(ctx, id)
				require.NoError(t, err)
				assert.Equal(t, tt.wantCreator, got.Creator, id)
				assert.Equal(t, tt.wantStatus, got.Status, id)
				assert.Equal(t, tt.wantAssignee, got.Assignee, id)
			}
		})
	}
}
