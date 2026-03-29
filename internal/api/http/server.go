// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Package http implements the REST API server for mtix per FR-7.1 and NFR-4.3.
// Uses Gin framework with middleware for CSRF, cache-control, rate limiting,
// and error handling per NFR-5.x requirements.
package http

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/web"
)

// ServerConfig holds configuration for the HTTP server per FR-7.1.
type ServerConfig struct {
	Bind      string // Bind address (default 127.0.0.1 per NFR-5.2).
	Port      string // HTTP port (default 6849).
	RateLimit int    // Requests/second per agent (0=disabled per NFR-1.5).
}

// Server is the main HTTP server for mtix per FR-7.1.
type Server struct {
	router     *gin.Engine
	httpSrv    *http.Server
	config     ServerConfig
	logger     *slog.Logger
	clock      func() time.Time
	startedAt  time.Time
	wsHub      *WSHub
	store      *sqlite.Store
	nodeSvc    *service.NodeService
	bgSvc      *service.BackgroundService
	sessionSvc *service.SessionService
	agentSvc   *service.AgentService
	configSvc  *service.ConfigService
}

// NewServer creates a new HTTP server with all middleware configured.
// Binds to localhost by default per NFR-5.2; non-localhost binding
// requires explicit configuration and logs a security warning.
func NewServer(
	store *sqlite.Store,
	nodeSvc *service.NodeService,
	bgSvc *service.BackgroundService,
	sessionSvc *service.SessionService,
	agentSvc *service.AgentService,
	configSvc *service.ConfigService,
	logger *slog.Logger,
	config ServerConfig,
	clock func() time.Time,
) *Server {
	if config.Bind == "" {
		config.Bind = "127.0.0.1"
	}
	if config.Port == "" {
		config.Port = "6849"
	}
	if clock == nil {
		clock = time.Now
	}

	gin.SetMode(gin.ReleaseMode)
	router := gin.New()

	hub := NewWSHub(logger)
	go hub.Run()

	s := &Server{
		router:     router,
		config:     config,
		logger:     logger,
		clock:      clock,
		startedAt:  clock(),
		wsHub:      hub,
		store:      store,
		nodeSvc:    nodeSvc,
		bgSvc:      bgSvc,
		sessionSvc: sessionSvc,
		agentSvc:   agentSvc,
		configSvc:  configSvc,
	}

	s.setupMiddleware()
	s.setupRoutes()

	return s
}

// setupMiddleware configures the middleware stack per NFR-5.x.
func (s *Server) setupMiddleware() {
	s.router.Use(gin.Recovery())
	s.router.Use(RequestIDMiddleware())
	s.router.Use(LoggingMiddleware(s.logger))
	s.router.Use(CacheControlMiddleware())
	s.router.Use(CORSMiddleware())

	if s.config.RateLimit > 0 {
		s.router.Use(RateLimitMiddleware(s.config.RateLimit))
	}
}

// setupRoutes mounts all route groups per FR-7.1.
func (s *Server) setupRoutes() {
	// Health check — root-relative per FR-7.3b.
	s.router.GET("/health", s.handleHealth)

	// OpenAPI spec — served at /api/ level, no CSRF per FR-16.4.
	s.router.GET("/api/openapi.yaml", s.handleOpenAPIYAML)
	s.router.GET("/api/openapi.json", s.handleOpenAPIJSON)

	// WebSocket events — root-relative, no CSRF per FR-7.5.
	s.router.GET("/ws/events", s.handleWebSocket)

	// API v1 group with CSRF protection on mutations.
	v1 := s.router.Group("/api/v1")
	v1.Use(CSRFMiddleware())
	s.registerNodeRoutes(v1)
	s.registerWorkflowRoutes(v1)
	s.registerQueryRoutes(v1)
	s.registerDepRoutes(v1)
	s.registerAgentRoutes(v1)
	s.registerAdminRoutes(v1)
	s.registerBulkRoutes(v1)

	// Mount embedded SPA UI at root per FR-9.1.
	// Non-API requests fall through to the SPA handler for client-side routing.
	s.mountUI()
}

// mountUI registers the embedded SPA handler as a catch-all.
// If the UI is not embedded (built without web/dist/), this is a no-op.
func (s *Server) mountUI() {
	if !web.HasEmbeddedUI() {
		s.logger.Debug("embedded UI not available, skipping mount")
		return
	}

	spaHandler, err := web.SPAHandler()
	if err != nil {
		s.logger.Warn("failed to create SPA handler", "error", err)
		return
	}

	s.router.NoRoute(gin.WrapH(spaHandler))
	s.logger.Info("mounted embedded UI")
}

// Router returns the Gin engine for testing.
func (s *Server) Router() *gin.Engine {
	return s.router
}

// Start starts the HTTP server and blocks until shutdown.
func (s *Server) Start() error {
	addr := net.JoinHostPort(s.config.Bind, s.config.Port)

	// Security warning for non-localhost binding per NFR-5.2.
	if s.config.Bind != "127.0.0.1" && s.config.Bind != "localhost" {
		s.logger.Warn("server binding to non-localhost address",
			"bind", s.config.Bind,
			"warning", "ensure authentication is configured")
	}

	s.httpSrv = &http.Server{
		Addr:              addr,
		Handler:           s.router,
		ReadHeaderTimeout: 10 * time.Second,
	}

	s.logger.Info("starting HTTP server", "addr", addr)
	return s.httpSrv.ListenAndServe()
}

// Shutdown gracefully shuts down the server per FR-7.1.
// Stops accepting connections, waits for in-flight requests (10s timeout),
// then closes the database cleanly.
func (s *Server) Shutdown(ctx context.Context) error {
	s.logger.Info("initiating graceful shutdown")

	// Create timeout context for shutdown.
	shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// Close WebSocket hub — disconnects all clients gracefully.
	if s.wsHub != nil {
		s.wsHub.Close()
	}

	// Shut down HTTP server.
	if s.httpSrv != nil {
		if err := s.httpSrv.Shutdown(shutdownCtx); err != nil {
			s.logger.Error("http server shutdown error", "error", err)
			return fmt.Errorf("http shutdown: %w", err)
		}
	}

	// Close database.
	if s.store != nil {
		if err := s.store.Close(); err != nil {
			s.logger.Error("database close error", "error", err)
			return fmt.Errorf("database close: %w", err)
		}
	}

	s.logger.Info("shutdown complete")
	return nil
}

// ListenAndServeWithGracefulShutdown starts the server and handles OS signals.
func (s *Server) ListenAndServeWithGracefulShutdown() error {
	errCh := make(chan error, 1)
	go func() {
		if err := s.Start(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	// Wait for interrupt signal.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		s.logger.Info("received signal", "signal", sig)
		return s.Shutdown(context.Background())
	case err := <-errCh:
		return err
	}
}

// handleHealth returns server health status per FR-7.3b.
func (s *Server) handleHealth(c *gin.Context) {
	uptime := s.clock().Sub(s.startedAt).Seconds()
	c.JSON(http.StatusOK, gin.H{
		"status":         "ok",
		"version":        "dev",
		"uptime_seconds": int(uptime),
	})
}
