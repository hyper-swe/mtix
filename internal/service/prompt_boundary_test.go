// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
)

// Preservation control: empty and repeated prompts still complete and emit one
// event per successful request. These are not claimed as new functional REDs.
func TestUpdatePrompt_EmptyAndRepeatedPrompt_PreservesEvents(t *testing.T) {
	for _, text := range []string{"", "Original"} {
		t.Run("prompt="+text, func(t *testing.T) {
			svc, nodes, s, bc := newTestPromptService(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			n, err := nodes.CreateNode(ctx, &service.CreateNodeRequest{
				Project: "PROJ", Title: "Prompt boundary", Prompt: "Original", Creator: "admin",
			})
			require.NoError(t, err)
			for request := 0; request < 2; request++ {
				before, readErr := s.GetNode(ctx, n.ID)
				require.NoError(t, readErr)
				bc.Reset()
				require.NoError(t, svc.UpdatePrompt(ctx, n.ID, text, "editor"))
				after, readErr := s.GetNode(ctx, n.ID)
				require.NoError(t, readErr)
				assert.Equal(t, text, after.Prompt)
				assert.Equal(t, after.ComputeHash(), after.ContentHash)
				events := bc.Events()
				require.Len(t, events, 1)
				assert.Equal(t, service.EventNodeUpdated, events[0].Type)
				assert.Equal(t, n.ID, events[0].NodeID)
				assert.Equal(t, "editor", events[0].Author)
				assert.Equal(t, time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC), events[0].Timestamp)
				metadata := map[string]string{}
				require.NoError(t, json.Unmarshal(events[0].Data, &metadata))
				assert.Equal(t, before.ContentHash, metadata["old_prompt_hash"])
				assert.Equal(t, after.ContentHash, metadata["new_prompt_hash"])
			}
		})
	}
}
