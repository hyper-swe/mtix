// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/hyper-swe/mtix/internal/service"
)

// mcpNotificationTypes maps service event types to MCP notification method names per FR-14.5.
// agent.heartbeat is intentionally excluded (high-frequency, UI-only via WebSocket).
var mcpNotificationTypes = map[service.EventType]string{
	service.EventNodeCreated:      "notifications/node.created",
	service.EventNodeUpdated:      "notifications/node.updated",
	service.EventNodeDeleted:      "notifications/node.deleted",
	service.EventProgressChanged:  "notifications/progress.changed",
	service.EventNodesInvalidated: "notifications/nodes.invalidated",
	service.EventAgentStateChanged: "notifications/agent.state",
	service.EventAgentStuck:       "notifications/agent.stuck",
	service.EventStatusChanged:    "notifications/node.status_changed",
	service.EventNodeClaimed:      "notifications/node.claimed",
	service.EventNodeCancelled:    "notifications/node.cancelled",
}

// NotificationPayload is the JSON-RPC notification sent over MCP per FR-14.5.
type NotificationPayload struct {
	NodeID    string          `json:"node_id"`
	EventType string          `json:"event_type"`
	Timestamp string          `json:"timestamp"`
	Author    string          `json:"author,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
}

// NotificationForwarder subscribes to the event hub and forwards matching events
// as MCP JSON-RPC notifications per FR-14.5.
type NotificationForwarder struct {
	server *Server
	hub    *service.Hub
	logger *slog.Logger
	done   chan struct{}
}

// NewNotificationForwarder creates a forwarder that bridges Hub events to MCP notifications.
func NewNotificationForwarder(server *Server, hub *service.Hub, logger *slog.Logger) *NotificationForwarder {
	if logger == nil {
		logger = slog.Default()
	}
	return &NotificationForwarder{
		server: server,
		hub:    hub,
		logger: logger,
		done:   make(chan struct{}),
	}
}

// Start begins forwarding events. Call Stop to clean up.
// Subscribes to all MCP-relevant event types via the Hub.
func (nf *NotificationForwarder) Start(ctx context.Context) {
	// Build filter for MCP-relevant events only.
	eventTypes := make([]service.EventType, 0, len(mcpNotificationTypes))
	for et := range mcpNotificationTypes {
		eventTypes = append(eventTypes, et)
	}

	ch := nf.hub.Subscribe(service.SubscriptionFilter{
		Events: eventTypes,
	})

	go nf.forwardLoop(ctx, ch)
}

// Stop signals the forwarding loop to exit.
func (nf *NotificationForwarder) Stop() {
	close(nf.done)
}

// forwardLoop reads events from the subscription channel and writes MCP notifications.
func (nf *NotificationForwarder) forwardLoop(ctx context.Context, ch <-chan service.Event) {
	defer nf.hub.Unsubscribe(ch)

	for {
		select {
		case <-ctx.Done():
			return
		case <-nf.done:
			return
		case event, ok := <-ch:
			if !ok {
				return
			}
			nf.forwardEvent(event)
		}
	}
}

// forwardEvent converts a service.Event to an MCP JSON-RPC notification and sends it.
func (nf *NotificationForwarder) forwardEvent(event service.Event) {
	method, ok := mcpNotificationTypes[event.Type]
	if !ok {
		return
	}

	payload := NotificationPayload{
		NodeID:    event.NodeID,
		EventType: string(event.Type),
		Timestamp: event.Timestamp.UTC().Format("2006-01-02T15:04:05Z"),
		Author:    event.Author,
		Data:      event.Data,
	}

	params, err := json.Marshal(payload)
	if err != nil {
		nf.logger.Error("marshal notification payload",
			"event_type", event.Type, "error", err)
		return
	}

	// JSON-RPC notification: no ID field per spec.
	notification := Request{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}

	if err := nf.server.writeJSON(notification); err != nil {
		nf.logger.Error("write MCP notification",
			"method", method, "error", err)
	}
}
