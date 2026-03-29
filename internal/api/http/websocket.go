// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"github.com/hyper-swe/mtix/internal/service"
)

// wsUpgrader configures the WebSocket upgrade from HTTP.
// This is a stateless configuration value, not mutable global state.
var wsUpgrader = websocket.Upgrader{ //nolint:gochecknoglobals // stateless config
	CheckOrigin: func(_ *http.Request) bool {
		return true // CORS handled by middleware
	},
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

// subscriptionFilter defines client-side event filtering per FR-7.5a.
type subscriptionFilter struct {
	Under  string   `json:"under"`  // Only events under this node prefix
	Events []string `json:"events"` // Whitelist of event types
}

// wsClient represents a connected WebSocket client.
type wsClient struct {
	conn     *websocket.Conn
	filterMu sync.RWMutex
	filter   *subscriptionFilter
	send     chan []byte
	done     chan struct{}
}

// WSHub manages WebSocket connections and event broadcasting per FR-7.5.
type WSHub struct {
	clients    map[*wsClient]bool
	mu         sync.RWMutex
	register   chan *wsClient
	unregister chan *wsClient
	broadcast  chan service.Event
	logger     *slog.Logger
	done       chan struct{}
}

// NewWSHub creates a new WebSocket hub.
func NewWSHub(logger *slog.Logger) *WSHub {
	return &WSHub{
		clients:    make(map[*wsClient]bool),
		register:   make(chan *wsClient),
		unregister: make(chan *wsClient),
		broadcast:  make(chan service.Event, 256),
		logger:     logger,
		done:       make(chan struct{}),
	}
}

// Run starts the hub's event loop. Call in a goroutine.
func (h *WSHub) Run() {
	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			h.clients[client] = true
			h.mu.Unlock()
			h.logger.Info("websocket client connected",
				"clients", len(h.clients))

		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
			}
			h.mu.Unlock()
			h.logger.Info("websocket client disconnected",
				"clients", len(h.clients))

		case event := <-h.broadcast:
			h.broadcastEvent(event)

		case <-h.done:
			h.mu.Lock()
			for client := range h.clients {
				close(client.send)
				delete(h.clients, client)
			}
			h.mu.Unlock()
			return
		}
	}
}

// Broadcast implements service.EventBroadcaster for the hub.
// Non-blocking — drops events if buffer is full.
func (h *WSHub) Broadcast(_ context.Context, event service.Event) error {
	select {
	case h.broadcast <- event:
	default:
		h.logger.Warn("websocket broadcast buffer full, dropping event",
			"type", event.Type)
	}
	return nil
}

// Close shuts down the hub and closes all connections.
func (h *WSHub) Close() {
	close(h.done)
}

// ClientCount returns the number of connected clients.
func (h *WSHub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// broadcastEvent sends an event to all matching clients.
func (h *WSHub) broadcastEvent(event service.Event) {
	data, err := json.Marshal(event)
	if err != nil {
		h.logger.Error("failed to marshal event", "error", err)
		return
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	for client := range h.clients {
		client.filterMu.RLock()
		matches := matchesFilter(client.filter, event)
		client.filterMu.RUnlock()
		if !matches {
			continue
		}
		select {
		case client.send <- data:
		default:
			// Client send buffer full — will be cleaned up.
			h.logger.Warn("dropping event for slow client")
		}
	}
}

// matchesFilter checks if an event matches a client's subscription filter.
func matchesFilter(filter *subscriptionFilter, event service.Event) bool {
	if filter == nil {
		return true // No filter = all events
	}

	// Check under prefix filter.
	if filter.Under != "" && !strings.HasPrefix(event.NodeID, filter.Under) {
		return false
	}

	// Check event type whitelist.
	if len(filter.Events) > 0 {
		matched := false
		for _, et := range filter.Events {
			if et == string(event.Type) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	return true
}

// handleWebSocket handles WS /ws/events per FR-7.5.
func (s *Server) handleWebSocket(c *gin.Context) {
	if s.wsHub == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "websocket not enabled",
		})
		return
	}

	conn, err := wsUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		s.logger.Error("websocket upgrade failed", "error", err)
		return
	}

	client := &wsClient{
		conn: conn,
		send: make(chan []byte, 64),
		done: make(chan struct{}),
	}

	s.wsHub.register <- client

	// Read pump — handles subscribe messages and ping/pong.
	go s.wsReadPump(client)
	// Write pump — sends events to the client.
	go s.wsWritePump(client)
}

// wsReadPump reads messages from the client.
// Handles subscription filter messages and keepalive pongs.
func (s *Server) wsReadPump(client *wsClient) {
	defer func() {
		s.wsHub.unregister <- client
		_ = client.conn.Close()
	}()

	// Set read deadline for keepalive.
	_ = client.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	client.conn.SetPongHandler(func(_ string) error {
		_ = client.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		_, message, err := client.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(
				err, websocket.CloseGoingAway, websocket.CloseNormalClosure,
			) {
				s.logger.Warn("websocket read error", "error", err)
			}
			return
		}

		// Try to parse as subscription filter.
		var sub struct {
			Subscribe *subscriptionFilter `json:"subscribe"`
		}
		if json.Unmarshal(message, &sub) == nil && sub.Subscribe != nil {
			client.filterMu.Lock()
			client.filter = sub.Subscribe
			client.filterMu.Unlock()
			s.logger.Info("websocket subscription updated",
				"under", sub.Subscribe.Under,
				"events", sub.Subscribe.Events)
		}
	}
}

// wsWritePump writes events to the client.
// Sends periodic pings for keepalive per FR-7.5.
func (s *Server) wsWritePump(client *wsClient) {
	ticker := time.NewTicker(30 * time.Second)
	defer func() {
		ticker.Stop()
		_ = client.conn.Close()
	}()

	for {
		select {
		case message, ok := <-client.send:
			_ = client.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				// Hub closed the channel.
				_ = client.conn.WriteMessage(
					websocket.CloseMessage,
					websocket.FormatCloseMessage(
						websocket.CloseNormalClosure, "server shutting down"),
				)
				return
			}

			if err := client.conn.WriteMessage(websocket.TextMessage, message); err != nil {
				return
			}

		case <-ticker.C:
			_ = client.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := client.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
