// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Addressed sends carry process wake context without inferring ledger history.
package service_test

import (
	"bytes"
	"encoding/json"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/stretchr/testify/require"
	"log/slog"
	"os"
	"testing"
	"time"
)

func TestAddressedSend_ProcessWakeHop_IncrementsAndMalformedFallsBack(t *testing.T) {
	for _, tt := range []struct {
		name, env, to string
		want          float64
	}{
		{"root", "", "worker", 0}, {"first wake", "0", "worker", 1}, {"boundary", "31", "worker", 32}, {"deep", "32", "worker", 33},
		{"negative", "-1", "worker", 0}, {"fraction", "1.5", "worker", 0}, {"malformed", "bad", "worker", 0}, {"overflow", "9223372036854775807", "worker", 0},
		{"ordinary comment", "32", "", 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("MTIX_WAKE_HOP", tt.env)
			prompts, nodes, store, _ := newTestPromptService(t)
			node, err := nodes.CreateNode(t.Context(), &service.CreateNodeRequest{Project: "TEST", Title: "hop context", Creator: "sender"})
			require.NoError(t, err)
			require.NoError(t, prompts.AddAnnotation(t.Context(), node.ID, "wake reply", "sender", tt.to))
			events, err := store.ReadPendingEvents(t.Context(), 100)
			require.NoError(t, err)
			var payload map[string]any
			for _, event := range events {
				if event.OpType == model.OpComment {
					require.NoError(t, json.Unmarshal(event.Payload, &payload))
				}
			}
			require.NotNil(t, payload)
			if tt.want == 0 {
				require.NotContains(t, payload, "hop")
			} else {
				require.Equal(t, tt.want, payload["hop"])
			}
			require.NoError(t, nodes.AppendAnnotation(t.Context(), node.ID, model.Annotation{ID: "second", Author: "sender", Text: "second", Addressee: tt.to, CreatedAt: time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)}))
			events, err = store.ReadPendingEvents(t.Context(), 100)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(events[len(events)-1].Payload, &payload))
			if tt.want == 0 {
				require.NotContains(t, payload, "hop")
			} else {
				require.Equal(t, tt.want, payload["hop"])
			}
		})
	}
}

func TestAddressedSend_AbsentContextAndMalformedWarning_PreserveSend(t *testing.T) {
	t.Setenv("MTIX_WAKE_HOP", "")
	require.NoError(t, os.Unsetenv("MTIX_WAKE_HOP"))
	_, nodes, store, _ := newTestPromptService(t)
	node, err := nodes.CreateNode(t.Context(), &service.CreateNodeRequest{Project: "TEST", Title: "root send", Creator: "sender"})
	require.NoError(t, err)
	var logs bytes.Buffer
	prompts := service.NewPromptService(store, &service.NoopBroadcaster{}, slog.New(slog.NewTextHandler(&logs, nil)), fixedClock(node.CreatedAt))
	require.NoError(t, prompts.AddAnnotation(t.Context(), node.ID, "root", "sender", "worker"))
	require.Empty(t, logs.String())
	t.Setenv("MTIX_WAKE_HOP", "malformed-context-value")
	require.NoError(t, prompts.AddAnnotation(t.Context(), node.ID, "fallback", "sender", "worker"))
	require.Contains(t, logs.String(), "invalid MTIX_WAKE_HOP")
	require.NotContains(t, logs.String(), "malformed-context-value")
	events, err := store.InboxList(t.Context(), "worker")
	require.NoError(t, err)
	require.Len(t, events, 2)
}
