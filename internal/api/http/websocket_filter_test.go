// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
)

// TestWSHub_BroadcastEvent_SubtreeBoundary verifies actual client queues obey
// both the dotted subtree boundary and event whitelist (FR-7.5a).
func TestWSHub_BroadcastEvent_SubtreeBoundary(t *testing.T) {
	tests := []struct {
		name      string
		nodeID    string
		eventType service.EventType
		want      bool
	}{
		{"exact node", "PROJ-1", service.EventNodeCreated, true},
		{"child", "PROJ-1.2", service.EventNodeCreated, true},
		{"deep descendant", "PROJ-1.2.3", service.EventNodeCreated, true},
		{"lookalike sibling", "PROJ-10", service.EventNodeCreated, false},
		{"lookalike sibling child", "PROJ-10.2", service.EventNodeCreated, false},
		{"unrelated node", "PROJ-2", service.EventNodeCreated, false},
		{"exact node wrong event", "PROJ-1", service.EventNodeUpdated, false},
		{"child wrong event", "PROJ-1.2", service.EventNodeUpdated, false},
		{"sibling wrong event", "PROJ-10", service.EventNodeUpdated, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hub := NewWSHub(slog.Default())
			client := &wsClient{
				filter: &subscriptionFilter{Under: "PROJ-1", Events: []string{"node.created"}},
				send:   make(chan []byte, 1),
			}
			// Call the synchronous hub dispatch directly: no timer or goroutine
			// can hide a wrongly delivered event behind a timeout.
			hub.clients[client] = true
			hub.broadcastEvent(service.Event{NodeID: tt.nodeID, Type: tt.eventType})
			require.Equal(t, boolToQueueLength(tt.want), len(client.send))
			if tt.want {
				var event service.Event
				require.NoError(t, json.Unmarshal(<-client.send, &event))
				assert.Equal(t, tt.nodeID, event.NodeID)
				assert.Equal(t, tt.eventType, event.Type)
			}
		})
	}
}

// boolToQueueLength gives the expected number of delivered events.
func boolToQueueLength(delivered bool) int {
	if delivered {
		return 1
	}
	return 0
}
