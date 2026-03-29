// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
)

// TestNotificationForwarder_ForwardsEvent verifies events are sent as MCP notifications.
func TestNotificationForwarder_ForwardsEvent(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	hub := service.NewHub(logger)

	server := NewServer(&bytes.Buffer{}, &buf, logger, "test")

	nf := NewNotificationForwarder(server, hub, logger)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	nf.Start(ctx)

	// Give the goroutine time to subscribe.
	time.Sleep(10 * time.Millisecond)

	// Broadcast an event.
	err := hub.Broadcast(ctx, service.Event{
		Type:      service.EventNodeCreated,
		NodeID:    "PROJ-1",
		Timestamp: time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC),
		Author:    "test-agent",
	})
	require.NoError(t, err)

	// Give time for forwarding.
	time.Sleep(100 * time.Millisecond)
	nf.Stop()
	cancel() // Cancel context to ensure goroutine exits.
	time.Sleep(50 * time.Millisecond) // Let goroutine drain and exit.

	// Now safe to read — the forwarding goroutine has exited.
	// Lock the server mutex to synchronize with any final write.
	server.mu.Lock()
	output := make([]byte, buf.Len())
	copy(output, buf.Bytes())
	server.mu.Unlock()
	if len(output) == 0 {
		t.Fatal("expected notification output, got empty buffer")
	}

	var notif Request
	err = json.Unmarshal(output, &notif)
	require.NoError(t, err)

	assert.Equal(t, "2.0", notif.JSONRPC)
	assert.Equal(t, "notifications/node.created", notif.Method)
	assert.Nil(t, notif.ID, "notification must not have an ID")

	var payload NotificationPayload
	err = json.Unmarshal(notif.Params, &payload)
	require.NoError(t, err)
	assert.Equal(t, "PROJ-1", payload.NodeID)
	assert.Equal(t, "test-agent", payload.Author)
}

// TestNotificationForwarder_IgnoresUnmappedEvents verifies non-MCP events are filtered.
func TestNotificationForwarder_IgnoresUnmappedEvents(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	hub := service.NewHub(logger)

	server := NewServer(&bytes.Buffer{}, &buf, logger, "test")

	nf := NewNotificationForwarder(server, hub, logger)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	nf.Start(ctx)

	time.Sleep(10 * time.Millisecond)

	// Broadcast an event type not in the MCP map.
	err := hub.Broadcast(ctx, service.Event{
		Type:      service.EventDependencyAdded,
		NodeID:    "PROJ-1",
		Timestamp: time.Now(),
	})
	require.NoError(t, err)

	time.Sleep(50 * time.Millisecond)
	nf.Stop()

	// The subscription filter should prevent this event from reaching the forwarder,
	// so the buffer should be empty.
	assert.Empty(t, buf.Bytes(), "unmapped event should not produce output")
}

// TestNotificationForwarder_StopPreventsForwarding verifies Stop halts the forwarder.
func TestNotificationForwarder_StopPreventsForwarding(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	hub := service.NewHub(logger)

	server := NewServer(&bytes.Buffer{}, &buf, logger, "test")

	nf := NewNotificationForwarder(server, hub, logger)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	nf.Start(ctx)

	time.Sleep(10 * time.Millisecond)
	nf.Stop()
	time.Sleep(10 * time.Millisecond)

	// Broadcast after stop — should not produce output.
	err := hub.Broadcast(ctx, service.Event{
		Type:      service.EventNodeCreated,
		NodeID:    "PROJ-2",
		Timestamp: time.Now(),
	})
	require.NoError(t, err)

	time.Sleep(50 * time.Millisecond)
	assert.Empty(t, buf.Bytes(), "stopped forwarder should not produce output")
}

// TestMCPNotificationTypes_AllMapped verifies all expected notification types are mapped.
func TestMCPNotificationTypes_AllMapped(t *testing.T) {
	expectedEvents := []service.EventType{
		service.EventNodeCreated,
		service.EventNodeUpdated,
		service.EventNodeDeleted,
		service.EventProgressChanged,
		service.EventNodesInvalidated,
		service.EventAgentStateChanged,
		service.EventAgentStuck,
		service.EventStatusChanged,
		service.EventNodeClaimed,
		service.EventNodeCancelled,
	}

	for _, et := range expectedEvents {
		method, ok := mcpNotificationTypes[et]
		assert.True(t, ok, "event type %s should be mapped", et)
		assert.NotEmpty(t, method, "method for %s should not be empty", et)
	}

	assert.Len(t, mcpNotificationTypes, 10, "should have exactly 10 notification types")
}
