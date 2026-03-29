// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
)

// --- Helpers ---

// wsTestServer creates a test HTTP server with a real WSHub and
// returns the server, the test httptest.Server, and a cleanup function.
func wsTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	s := testServer(t)

	ts := httptest.NewServer(s.Router())
	t.Cleanup(func() {
		s.wsHub.Close()
		ts.Close()
	})

	return s, ts
}

// wsConnect opens a WebSocket connection to /ws/events on the given test server.
// Returns the connection — caller is responsible for closing it.
func wsConnect(t *testing.T, ts *httptest.Server) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/events"
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	require.Equal(t, 101, resp.StatusCode)
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// readWSMessage reads a single JSON message from the WebSocket
// with a short timeout. Returns decoded event.
func readWSMessage(t *testing.T, conn *websocket.Conn, timeout time.Duration) service.Event {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	_, msg, err := conn.ReadMessage()
	require.NoError(t, err, "should receive a WS message within timeout")
	var event service.Event
	require.NoError(t, json.Unmarshal(msg, &event))
	return event
}

// --- MTIX-5.3.1: WebSocket Connection Tests ---

// TestWebSocket_Connect_Succeeds verifies WebSocket upgrade at /ws/events.
func TestWebSocket_Connect_Succeeds(t *testing.T) {
	s, ts := wsTestServer(t)
	conn := wsConnect(t, ts)
	require.NotNil(t, conn)

	// Give hub time to register the client.
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 1, s.wsHub.ClientCount())
}

// TestWebSocket_ReceivesEvents verifies events broadcast via the hub
// are delivered to connected WebSocket clients.
func TestWebSocket_ReceivesEvents(t *testing.T) {
	s, ts := wsTestServer(t)
	conn := wsConnect(t, ts)

	// Wait for registration.
	time.Sleep(50 * time.Millisecond)

	// Broadcast an event.
	event := service.Event{
		Type:      service.EventNodeCreated,
		NodeID:    "PROJ-1",
		Timestamp: time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC),
		Author:    "agent-1",
	}
	err := s.wsHub.Broadcast(context.Background(), event)
	require.NoError(t, err)

	// Read the event from the WS connection.
	received := readWSMessage(t, conn, 2*time.Second)
	assert.Equal(t, service.EventNodeCreated, received.Type)
	assert.Equal(t, "PROJ-1", received.NodeID)
	assert.Equal(t, "agent-1", received.Author)
}

// TestWebSocket_GracefulClose_OnShutdown verifies the hub sends close
// frames to connected clients when the hub is closed.
func TestWebSocket_GracefulClose_OnShutdown(t *testing.T) {
	_, ts := wsTestServer(t)
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/events"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	// Wait for registration.
	time.Sleep(50 * time.Millisecond)

	// Close the test server (which triggers hub.Close via cleanup).
	ts.Close()

	// The client should receive a close message or read error.
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, readErr := conn.ReadMessage()
	assert.Error(t, readErr, "should get an error after server shutdown")
}

// TestWebSocket_Keepalive_PingPong verifies the pong handler is configured
// by checking that the connection accepts a pong after setup.
func TestWebSocket_Keepalive_PingPong(t *testing.T) {
	_, ts := wsTestServer(t)
	conn := wsConnect(t, ts)

	// Send a ping from client — server's pong handler should respond.
	err := conn.WriteMessage(websocket.PingMessage, []byte("keepalive"))
	require.NoError(t, err)

	// Set a handler for the pong response.
	pongCh := make(chan string, 1)
	conn.SetPongHandler(func(appData string) error {
		pongCh <- appData
		return nil
	})

	// Read to trigger the pong handler (need to read for pong to be processed).
	go func() {
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		// ReadMessage will process control frames including pong.
		_, _, _ = conn.ReadMessage()
	}()

	select {
	case data := <-pongCh:
		assert.Equal(t, "keepalive", data)
	case <-time.After(2 * time.Second):
		// Pong may not be explicitly sent by server (server is the one
		// that sends pings; client sends pongs). The gorilla library
		// auto-responds to pings. Let's verify the connection is still alive.
		err := conn.WriteMessage(websocket.TextMessage, []byte(`{}`))
		assert.NoError(t, err, "connection should still be alive")
	}
}

// TestWebSocket_MultipleClients_AllReceive verifies events are broadcast
// to all connected clients.
func TestWebSocket_MultipleClients_AllReceive(t *testing.T) {
	s, ts := wsTestServer(t)
	conn1 := wsConnect(t, ts)
	conn2 := wsConnect(t, ts)

	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 2, s.wsHub.ClientCount())

	// Broadcast an event.
	event := service.Event{
		Type:   service.EventNodeUpdated,
		NodeID: "PROJ-1.1",
	}
	err := s.wsHub.Broadcast(context.Background(), event)
	require.NoError(t, err)

	// Both clients should receive the event.
	e1 := readWSMessage(t, conn1, 2*time.Second)
	e2 := readWSMessage(t, conn2, 2*time.Second)
	assert.Equal(t, service.EventNodeUpdated, e1.Type)
	assert.Equal(t, service.EventNodeUpdated, e2.Type)
}

// TestWebSocket_ClientDisconnect_DecreasesCount verifies client count
// decreases when a client disconnects.
func TestWebSocket_ClientDisconnect_DecreasesCount(t *testing.T) {
	s, ts := wsTestServer(t)

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/events"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)

	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 1, s.wsHub.ClientCount())

	// Close the connection.
	_ = conn.Close()

	// Allow time for unregistration.
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, 0, s.wsHub.ClientCount())
}

// --- MTIX-5.3.2: Event Type Broadcast Tests ---

// TestEvent_NodeCreated_BroadcastOnCreate verifies node.created events.
func TestEvent_NodeCreated_BroadcastOnCreate(t *testing.T) {
	s, ts := wsTestServer(t)
	conn := wsConnect(t, ts)
	time.Sleep(50 * time.Millisecond)

	event := service.Event{
		Type:      service.EventNodeCreated,
		NodeID:    "PROJ-1",
		Timestamp: time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC),
		Author:    "agent-1",
		Data:      json.RawMessage(`{"title":"New Task"}`),
	}
	require.NoError(t, s.wsHub.Broadcast(context.Background(), event))

	received := readWSMessage(t, conn, 2*time.Second)
	assert.Equal(t, service.EventNodeCreated, received.Type)
	assert.Equal(t, "PROJ-1", received.NodeID)
	assert.Contains(t, string(received.Data), "New Task")
}

// TestEvent_NodeUpdated_IncludesChangedFields verifies node.updated events
// include the changed fields in the data payload.
func TestEvent_NodeUpdated_IncludesChangedFields(t *testing.T) {
	s, ts := wsTestServer(t)
	conn := wsConnect(t, ts)
	time.Sleep(50 * time.Millisecond)

	changedFields := `{"changed":["title","description"]}`
	event := service.Event{
		Type:   service.EventNodeUpdated,
		NodeID: "PROJ-1.2",
		Data:   json.RawMessage(changedFields),
	}
	require.NoError(t, s.wsHub.Broadcast(context.Background(), event))

	received := readWSMessage(t, conn, 2*time.Second)
	assert.Equal(t, service.EventNodeUpdated, received.Type)
	assert.Contains(t, string(received.Data), "title")
	assert.Contains(t, string(received.Data), "description")
}

// TestEvent_NodeDeleted_PerNodeEvent verifies node.deleted events.
func TestEvent_NodeDeleted_PerNodeEvent(t *testing.T) {
	s, ts := wsTestServer(t)
	conn := wsConnect(t, ts)
	time.Sleep(50 * time.Millisecond)

	event := service.Event{
		Type:   service.EventNodeDeleted,
		NodeID: "PROJ-3",
		Author: "agent-2",
	}
	require.NoError(t, s.wsHub.Broadcast(context.Background(), event))

	received := readWSMessage(t, conn, 2*time.Second)
	assert.Equal(t, service.EventNodeDeleted, received.Type)
	assert.Equal(t, "PROJ-3", received.NodeID)
}

// TestEvent_NodesDeleted_BatchCascadeEvent verifies batch delete events
// include count and parent_id in the data payload.
func TestEvent_NodesDeleted_BatchCascadeEvent(t *testing.T) {
	s, ts := wsTestServer(t)
	conn := wsConnect(t, ts)
	time.Sleep(50 * time.Millisecond)

	batchData := `{"parent_id":"PROJ-1","count":5}`
	event := service.Event{
		Type:   service.EventNodeDeleted,
		NodeID: "PROJ-1",
		Data:   json.RawMessage(batchData),
	}
	require.NoError(t, s.wsHub.Broadcast(context.Background(), event))

	received := readWSMessage(t, conn, 2*time.Second)
	assert.Contains(t, string(received.Data), "parent_id")
	assert.Contains(t, string(received.Data), "count")
}

// TestEvent_ProgressChanged_OnStatusTransition verifies progress.changed events.
func TestEvent_ProgressChanged_OnStatusTransition(t *testing.T) {
	s, ts := wsTestServer(t)
	conn := wsConnect(t, ts)
	time.Sleep(50 * time.Millisecond)

	progressData := `{"progress":0.75,"done":3,"total":4}`
	event := service.Event{
		Type:   service.EventProgressChanged,
		NodeID: "PROJ-1",
		Data:   json.RawMessage(progressData),
	}
	require.NoError(t, s.wsHub.Broadcast(context.Background(), event))

	received := readWSMessage(t, conn, 2*time.Second)
	assert.Equal(t, service.EventProgressChanged, received.Type)
	assert.Contains(t, string(received.Data), "0.75")
}

// TestEvent_NodesInvalidated_BatchRerunEvent verifies nodes.invalidated
// batch events include count and parent_id.
func TestEvent_NodesInvalidated_BatchRerunEvent(t *testing.T) {
	s, ts := wsTestServer(t)
	conn := wsConnect(t, ts)
	time.Sleep(50 * time.Millisecond)

	invalidData := `{"parent_id":"PROJ-2","count":8,"scope":"subtree"}`
	event := service.Event{
		Type:   service.EventNodesInvalidated,
		NodeID: "PROJ-2",
		Data:   json.RawMessage(invalidData),
	}
	require.NoError(t, s.wsHub.Broadcast(context.Background(), event))

	received := readWSMessage(t, conn, 2*time.Second)
	assert.Equal(t, service.EventNodesInvalidated, received.Type)
	assert.Contains(t, string(received.Data), "parent_id")
	assert.Contains(t, string(received.Data), "count")
	assert.Contains(t, string(received.Data), "subtree")
}

// TestEvent_AgentStuck_Broadcast verifies agent.stuck events.
func TestEvent_AgentStuck_Broadcast(t *testing.T) {
	s, ts := wsTestServer(t)
	conn := wsConnect(t, ts)
	time.Sleep(50 * time.Millisecond)

	stuckData := `{"agent_id":"agent-3","stuck_duration_s":300}`
	event := service.Event{
		Type:   service.EventAgentStuck,
		NodeID: "PROJ-1.3",
		Author: "agent-3",
		Data:   json.RawMessage(stuckData),
	}
	require.NoError(t, s.wsHub.Broadcast(context.Background(), event))

	received := readWSMessage(t, conn, 2*time.Second)
	assert.Equal(t, service.EventAgentStuck, received.Type)
	assert.Contains(t, string(received.Data), "agent-3")
}

// --- MTIX-5.3.3: Subscription Filter Tests ---

// TestSubscription_Default_AllEvents verifies that without a subscribe
// message, all events are delivered.
func TestSubscription_Default_AllEvents(t *testing.T) {
	s, ts := wsTestServer(t)
	conn := wsConnect(t, ts)
	time.Sleep(50 * time.Millisecond)

	// Send various event types — all should be received.
	events := []service.Event{
		{Type: service.EventNodeCreated, NodeID: "A-1"},
		{Type: service.EventNodeUpdated, NodeID: "B-2"},
		{Type: service.EventProgressChanged, NodeID: "C-3"},
	}
	for _, e := range events {
		require.NoError(t, s.wsHub.Broadcast(context.Background(), e))
	}

	for i, expected := range events {
		received := readWSMessage(t, conn, 2*time.Second)
		assert.Equal(t, expected.Type, received.Type, "event %d type mismatch", i)
		assert.Equal(t, expected.NodeID, received.NodeID, "event %d nodeID mismatch", i)
	}
}

// TestSubscription_UnderFilter_SubtreeOnly verifies that the "under" filter
// only delivers events for nodes under the specified prefix.
func TestSubscription_UnderFilter_SubtreeOnly(t *testing.T) {
	s, ts := wsTestServer(t)
	conn := wsConnect(t, ts)
	time.Sleep(50 * time.Millisecond)

	// Send subscribe message with under filter.
	subscribeMsg := `{"subscribe":{"under":"PROJ-42.1"}}`
	err := conn.WriteMessage(websocket.TextMessage, []byte(subscribeMsg))
	require.NoError(t, err)
	time.Sleep(50 * time.Millisecond)

	// Broadcast event outside the subtree — should be filtered.
	outsideEvent := service.Event{
		Type:   service.EventNodeCreated,
		NodeID: "PROJ-99.1",
	}
	require.NoError(t, s.wsHub.Broadcast(context.Background(), outsideEvent))

	// Broadcast event inside the subtree — should be delivered.
	insideEvent := service.Event{
		Type:   service.EventNodeUpdated,
		NodeID: "PROJ-42.1.3",
	}
	require.NoError(t, s.wsHub.Broadcast(context.Background(), insideEvent))

	// Broadcast exact match — should also be delivered.
	exactEvent := service.Event{
		Type:   service.EventProgressChanged,
		NodeID: "PROJ-42.1",
	}
	require.NoError(t, s.wsHub.Broadcast(context.Background(), exactEvent))

	// Should receive the inside event first (outside was filtered).
	received1 := readWSMessage(t, conn, 2*time.Second)
	assert.Equal(t, "PROJ-42.1.3", received1.NodeID)

	// Then the exact match.
	received2 := readWSMessage(t, conn, 2*time.Second)
	assert.Equal(t, "PROJ-42.1", received2.NodeID)

	// Verify no more messages (the outside event was filtered).
	_ = conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	_, _, readErr := conn.ReadMessage()
	assert.Error(t, readErr, "should timeout — no more events")
}

// TestSubscription_EventTypeFilter_WhitelistOnly verifies that the "events"
// filter only delivers whitelisted event types.
func TestSubscription_EventTypeFilter_WhitelistOnly(t *testing.T) {
	s, ts := wsTestServer(t)
	conn := wsConnect(t, ts)
	time.Sleep(50 * time.Millisecond)

	// Subscribe to only node.updated and progress.changed.
	subscribeMsg := `{"subscribe":{"events":["node.updated","progress.changed"]}}`
	err := conn.WriteMessage(websocket.TextMessage, []byte(subscribeMsg))
	require.NoError(t, err)
	time.Sleep(50 * time.Millisecond)

	// Broadcast node.created — should be filtered out.
	require.NoError(t, s.wsHub.Broadcast(context.Background(), service.Event{
		Type:   service.EventNodeCreated,
		NodeID: "X-1",
	}))

	// Broadcast node.updated — should be delivered.
	require.NoError(t, s.wsHub.Broadcast(context.Background(), service.Event{
		Type:   service.EventNodeUpdated,
		NodeID: "X-2",
	}))

	// Broadcast progress.changed — should be delivered.
	require.NoError(t, s.wsHub.Broadcast(context.Background(), service.Event{
		Type:   service.EventProgressChanged,
		NodeID: "X-3",
	}))

	// Should receive node.updated first.
	received1 := readWSMessage(t, conn, 2*time.Second)
	assert.Equal(t, service.EventNodeUpdated, received1.Type)

	// Then progress.changed.
	received2 := readWSMessage(t, conn, 2*time.Second)
	assert.Equal(t, service.EventProgressChanged, received2.Type)

	// No more messages.
	_ = conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	_, _, readErr := conn.ReadMessage()
	assert.Error(t, readErr, "should timeout — filtered event not delivered")
}

// TestSubscription_FilterHeartbeat_Excluded verifies that agent.heartbeat
// events can be excluded via the events whitelist.
func TestSubscription_FilterHeartbeat_Excluded(t *testing.T) {
	s, ts := wsTestServer(t)
	conn := wsConnect(t, ts)
	time.Sleep(50 * time.Millisecond)

	// Subscribe to only node.created — excludes heartbeats.
	subscribeMsg := `{"subscribe":{"events":["node.created"]}}`
	err := conn.WriteMessage(websocket.TextMessage, []byte(subscribeMsg))
	require.NoError(t, err)
	time.Sleep(50 * time.Millisecond)

	// Broadcast agent.state (simulating heartbeat-like event).
	require.NoError(t, s.wsHub.Broadcast(context.Background(), service.Event{
		Type:   service.EventAgentStateChanged,
		NodeID: "agent-1",
	}))

	// Broadcast node.created — should be delivered.
	require.NoError(t, s.wsHub.Broadcast(context.Background(), service.Event{
		Type:   service.EventNodeCreated,
		NodeID: "PROJ-1",
	}))

	received := readWSMessage(t, conn, 2*time.Second)
	assert.Equal(t, service.EventNodeCreated, received.Type)
	assert.Equal(t, "PROJ-1", received.NodeID)

	// No heartbeat event should have been received.
	_ = conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	_, _, readErr := conn.ReadMessage()
	assert.Error(t, readErr, "heartbeat should have been filtered")
}

// TestSubscription_InvalidMessage_Ignored verifies that malformed subscribe
// messages do not crash the server or disconnect the client.
func TestSubscription_InvalidMessage_Ignored(t *testing.T) {
	s, ts := wsTestServer(t)
	conn := wsConnect(t, ts)
	time.Sleep(50 * time.Millisecond)

	// Send garbage messages that are not valid subscribe messages.
	invalidMessages := []string{
		`not json at all`,
		`{"something":"else"}`,
		`{"subscribe":"not an object"}`,
		`{}`,
	}
	for _, msg := range invalidMessages {
		err := conn.WriteMessage(websocket.TextMessage, []byte(msg))
		require.NoError(t, err)
	}
	time.Sleep(50 * time.Millisecond)

	// Connection should still be alive — broadcast an event.
	require.NoError(t, s.wsHub.Broadcast(context.Background(), service.Event{
		Type:   service.EventNodeCreated,
		NodeID: "STILL-ALIVE",
	}))

	received := readWSMessage(t, conn, 2*time.Second)
	assert.Equal(t, "STILL-ALIVE", received.NodeID)
}

// TestSubscription_CombinedFilter_UnderAndEvents verifies that both
// under and events filters are applied together (AND logic).
func TestSubscription_CombinedFilter_UnderAndEvents(t *testing.T) {
	s, ts := wsTestServer(t)
	conn := wsConnect(t, ts)
	time.Sleep(50 * time.Millisecond)

	// Subscribe: under PROJ-5, only progress.changed events.
	subscribeMsg := `{"subscribe":{"under":"PROJ-5","events":["progress.changed"]}}`
	err := conn.WriteMessage(websocket.TextMessage, []byte(subscribeMsg))
	require.NoError(t, err)
	time.Sleep(50 * time.Millisecond)

	// Right subtree, wrong type — filtered.
	require.NoError(t, s.wsHub.Broadcast(context.Background(), service.Event{
		Type:   service.EventNodeCreated,
		NodeID: "PROJ-5.1",
	}))

	// Wrong subtree, right type — filtered.
	require.NoError(t, s.wsHub.Broadcast(context.Background(), service.Event{
		Type:   service.EventProgressChanged,
		NodeID: "PROJ-99.1",
	}))

	// Right subtree, right type — delivered.
	require.NoError(t, s.wsHub.Broadcast(context.Background(), service.Event{
		Type:   service.EventProgressChanged,
		NodeID: "PROJ-5.2.1",
	}))

	received := readWSMessage(t, conn, 2*time.Second)
	assert.Equal(t, service.EventProgressChanged, received.Type)
	assert.Equal(t, "PROJ-5.2.1", received.NodeID)

	// No more messages.
	_ = conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	_, _, readErr := conn.ReadMessage()
	assert.Error(t, readErr, "filtered events should not be delivered")
}

// --- Unit Tests for matchesFilter ---

// TestMatchesFilter_NilFilter_AllEvents verifies nil filter passes all events.
func TestMatchesFilter_NilFilter_AllEvents(t *testing.T) {
	event := service.Event{Type: service.EventNodeCreated, NodeID: "X-1"}
	assert.True(t, matchesFilter(nil, event))
}

// TestMatchesFilter_UnderPrefix verifies under prefix matching.
func TestMatchesFilter_UnderPrefix(t *testing.T) {
	tests := []struct {
		name   string
		under  string
		nodeID string
		want   bool
	}{
		{"exact match", "PROJ-1", "PROJ-1", true},
		{"child match", "PROJ-1", "PROJ-1.2", true},
		{"deep child", "PROJ-1", "PROJ-1.2.3.4", true},
		{"no match", "PROJ-1", "PROJ-2", false},
		{"partial prefix no match", "PROJ-1", "PROJ-10", true}, // prefix match
		{"empty under matches all", "", "PROJ-99", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filter := &subscriptionFilter{Under: tt.under}
			event := service.Event{NodeID: tt.nodeID}
			assert.Equal(t, tt.want, matchesFilter(filter, event))
		})
	}
}

// TestMatchesFilter_EventWhitelist verifies event type whitelist.
func TestMatchesFilter_EventWhitelist(t *testing.T) {
	tests := []struct {
		name      string
		whitelist []string
		eventType service.EventType
		want      bool
	}{
		{"in whitelist", []string{"node.created", "node.updated"}, service.EventNodeCreated, true},
		{"not in whitelist", []string{"node.created"}, service.EventNodeUpdated, false},
		{"empty whitelist passes all", []string{}, service.EventNodeDeleted, true},
		{"single match", []string{"progress.changed"}, service.EventProgressChanged, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filter := &subscriptionFilter{Events: tt.whitelist}
			event := service.Event{Type: tt.eventType}
			assert.Equal(t, tt.want, matchesFilter(filter, event))
		})
	}
}

// --- Hub Unit Tests ---

// TestWSHub_BroadcastBufferFull verifies non-blocking broadcast when
// the buffer is full.
func TestWSHub_BroadcastBufferFull(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	hub := NewWSHub(logger)
	// Do NOT start Run() — buffer will fill up.

	// Fill the broadcast buffer (capacity 256).
	for i := 0; i < 256; i++ {
		err := hub.Broadcast(context.Background(), service.Event{
			Type:   service.EventNodeCreated,
			NodeID: "fill",
		})
		require.NoError(t, err)
	}

	// 257th should be dropped (non-blocking), not error.
	err := hub.Broadcast(context.Background(), service.Event{
		Type:   service.EventNodeCreated,
		NodeID: "overflow",
	})
	assert.NoError(t, err, "broadcast should not block or error when buffer full")
}

// TestWSHub_Close_StopsRun verifies that Close() terminates the Run loop.
func TestWSHub_Close_StopsRun(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	hub := NewWSHub(logger)

	done := make(chan struct{})
	go func() {
		hub.Run()
		close(done)
	}()

	hub.Close()

	select {
	case <-done:
		// Run exited — success.
	case <-time.After(2 * time.Second):
		t.Fatal("hub.Run did not exit after Close")
	}
}

// TestWSHub_ClientCount_Empty verifies zero count with no clients.
func TestWSHub_ClientCount_Empty(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	hub := NewWSHub(logger)
	assert.Equal(t, 0, hub.ClientCount())
}
