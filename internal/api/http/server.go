// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Package http implements the REST API server for mtix per FR-7.1 and NFR-4.3.
// Uses Gin framework with middleware for CSRF, cache-control, rate limiting,
// and error handling per NFR-5.x requirements.
package http

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hyper-swe/mtix/internal/web"
)

// ServerConfig holds configuration for the HTTP server per FR-7.1.
type ServerConfig struct {
	Bind      string // Bind address (default 127.0.0.1 per NFR-5.2).
	Port      string // HTTP port (default 6849).
	RateLimit int    // Requests/second per client address (0=disabled per NFR-1.5).
	Version   string // Build version reported by /health (default "dev" per FR-7.3b).
}

// Server is the main HTTP server for mtix per FR-7.1.
type Server struct {
	router        *gin.Engine
	httpSrv       *http.Server
	config        ServerConfig
	logger        *slog.Logger
	clock         func() time.Time
	startedAt     time.Time
	wsHub         *WSHub
	nodeSvc       NodeReader
	nodeWriter    NodeWriter
	bgSvc         BackgroundReader
	bgWriter      BackgroundWriter
	sessionSvc    SessionReader
	sessionWriter SessionWriter
	agentSvc      AgentReader
	agentWriter   AgentWriter
	configSvc     ConfigReader
	configWriter  ConfigWriter
	depSvc        DependencyReader
	depWriter     DependencyWriter
	adminSvc      AdminReader
	adminWriter   AdminWriter
	readOnly      bool
	warnOut       io.Writer // Destination of the network-exposure warning.
}

// NewServer creates a new HTTP server with all middleware configured.
// Binds to localhost by default per NFR-5.2; non-localhost binding
// requires explicit configuration and logs a security warning.
// The client address that the request log and ClientIP report is the TCP
// peer: forwarded-address headers are not consulted (MTIX-95.14).
func NewServer(services Services, logger *slog.Logger, config ServerConfig, clock func() time.Time) *Server {
	return newServer(services, logger, config, clock, false)
}

// NewReadOnlyServer builds from read interfaces alone per MTIX-99.1.
// It registers no mutation service; this is the FR-23 precursor.
func NewReadOnlyServer(read ReadServices, logger *slog.Logger, config ServerConfig, clock func() time.Time) *Server {
	return newServer(Services{Read: read}, logger, config, clock, true)
}

func newServer(services Services, logger *slog.Logger, config ServerConfig, clock func() time.Time, readOnly bool) *Server {
	if config.Bind == "" {
		config.Bind = "127.0.0.1"
	}
	if config.Port == "" {
		config.Port = "6849"
	}
	if config.Version == "" {
		config.Version = "dev"
	}
	if clock == nil {
		clock = time.Now
	}

	if logger == nil {
		logger = slog.Default()
	}
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	// Client address = TCP peer, never X-Forwarded-For or X-Real-IP
	// (MTIX-95.14).
	router.ForwardedByClientIP = false

	hub := NewWSHub(logger)
	go hub.Run()

	s := &Server{
		router:    router,
		config:    config,
		logger:    logger,
		clock:     clock,
		startedAt: clock(),
		wsHub:     hub,

		nodeSvc:       services.Read.Nodes,
		nodeWriter:    services.Write.Nodes,
		bgSvc:         services.Read.Background,
		bgWriter:      services.Write.Background,
		sessionSvc:    services.Read.Sessions,
		sessionWriter: services.Write.Sessions,
		agentSvc:      services.Read.Agents,
		agentWriter:   services.Write.Agents,
		configSvc:     services.Read.Config, configWriter: services.Write.Config,
		depSvc: services.Read.Dependencies, depWriter: services.Write.Dependencies,
		adminSvc: services.Read.Admin, adminWriter: services.Write.Admin, readOnly: readOnly,
		warnOut: os.Stderr,
	}

	s.setupMiddleware()
	s.setupRoutes()

	return s
}

// setupMiddleware configures the middleware stack per NFR-5.x. Panic
// recovery logs no request headers, and every request must carry an
// allowlisted Host header (MTIX-95.14).
func (s *Server) setupMiddleware() {
	s.router.Use(RecoveryMiddleware(s.logger))
	s.router.Use(RequestIDMiddleware())
	s.router.Use(LoggingMiddleware(s.logger))
	s.router.Use(SecurityHeadersMiddleware())
	s.router.Use(CacheControlMiddleware())
	s.router.Use(HostAllowlistMiddleware(s.config.Bind))
	s.router.Use(CORSMiddleware(s.config.Bind, s.config.Port))

	if s.config.RateLimit > 0 {
		s.router.Use(RateLimitMiddleware(s.config.RateLimit, rateLimitMaxKeys, s.clock))
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
	if s.readOnly {
		s.registerReadOnlyRoutes(v1)
	} else {
		s.registerNodeRoutes(v1)
		s.registerWorkflowRoutes(v1)
		s.registerQueryRoutes(v1)
		s.registerDepRoutes(v1)
		s.registerAgentRoutes(v1)
		s.registerAdminRoutes(v1)
		s.registerBulkRoutes(v1)
	}

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

// Start starts the HTTP server and blocks until shutdown. It warns about
// network exposure when the bind address is not loopback: localhost, ::1
// and the rest of 127.0.0.0/8 are loopback (NFR-5.2, MTIX-95.14).
func (s *Server) Start() error {
	addr := net.JoinHostPort(s.config.Bind, s.config.Port)

	// Security warning for non-localhost binding per NFR-5.2.
	if !isLoopbackHost(s.config.Bind) {
		fmt.Fprintf(s.warnOut, "\n"+
			"WARNING: Binding to %s exposes the API to the network without authentication.\n"+
			"All task data is readable and writable by anyone who can reach this address.\n"+
			"This is intended for trusted networks only (e.g., local development, VPN).\n\n",
			addr)
	}

	s.httpSrv = &http.Server{
		Addr:              addr,
		Handler:           s.router,
		ReadHeaderTimeout: 10 * time.Second,
	}

	s.logger.Info("starting HTTP server", "addr", addr, "version", s.config.Version)
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
	if s.adminWriter != nil {
		if err := s.adminWriter.Close(); err != nil {
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

// handleHealth returns server health status per FR-7.3b, including the
// build version from ServerConfig.Version (MTIX-95.14).
func (s *Server) handleHealth(c *gin.Context) {
	uptime := s.clock().Sub(s.startedAt).Seconds()
	c.JSON(http.StatusOK, gin.H{
		"status":         "ok",
		"version":        s.config.Version,
		"uptime_seconds": int(uptime),
	})
}
