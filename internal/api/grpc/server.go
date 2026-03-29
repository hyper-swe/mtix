// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Package grpc implements the gRPC server for mtix per FR-8.1.
// Provides typed RPC access to all mtix operations alongside the REST API.
package grpc

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// ServerConfig holds configuration for the gRPC server per FR-8.1.
type ServerConfig struct {
	Port string // gRPC port (default 6850).
}

// Server is the gRPC server for mtix per FR-8.1.
type Server struct {
	grpcSrv     *grpc.Server
	config      ServerConfig
	logger      *slog.Logger
	clock       func() time.Time
	store       *sqlite.Store
	nodeSvc     *service.NodeService
	bgSvc       *service.BackgroundService
	sessionSvc  *service.SessionService
	agentSvc    *service.AgentService
	configSvc   *service.ConfigService
	contextSvc  *service.ContextService
	promptSvc   *service.PromptService
	broadcaster service.EventBroadcaster
}

// NewServer creates a new gRPC server with interceptors per FR-8.1.
// Registers the MtixService implementation and enables reflection.
func NewServer(
	store *sqlite.Store,
	nodeSvc *service.NodeService,
	bgSvc *service.BackgroundService,
	sessionSvc *service.SessionService,
	agentSvc *service.AgentService,
	configSvc *service.ConfigService,
	contextSvc *service.ContextService,
	promptSvc *service.PromptService,
	broadcaster service.EventBroadcaster,
	logger *slog.Logger,
	config ServerConfig,
	clock func() time.Time,
) *Server {
	if config.Port == "" {
		config.Port = "6850"
	}
	if clock == nil {
		clock = time.Now
	}

	s := &Server{
		config:      config,
		logger:      logger,
		clock:       clock,
		store:       store,
		nodeSvc:     nodeSvc,
		bgSvc:       bgSvc,
		sessionSvc:  sessionSvc,
		agentSvc:    agentSvc,
		configSvc:   configSvc,
		contextSvc:  contextSvc,
		promptSvc:   promptSvc,
		broadcaster: broadcaster,
	}

	// Create gRPC server with interceptors.
	s.grpcSrv = grpc.NewServer(
		grpc.ChainUnaryInterceptor(
			s.recoveryInterceptor(),
			s.loggingInterceptor(),
		),
		grpc.ChainStreamInterceptor(
			s.streamRecoveryInterceptor(),
			s.streamLoggingInterceptor(),
		),
	)

	// Enable server reflection for grpcurl/grpcui debugging.
	reflection.Register(s.grpcSrv)

	return s
}

// Start starts the gRPC server and blocks until stopped.
func (s *Server) Start() error {
	addr := net.JoinHostPort("127.0.0.1", s.config.Port)
	lc := net.ListenConfig{}
	lis, err := lc.Listen(context.Background(), "tcp", addr)
	if err != nil {
		return fmt.Errorf("grpc listen on %s: %w", addr, err)
	}

	s.logger.Info("starting gRPC server", "addr", addr)
	return s.grpcSrv.Serve(lis)
}

// GracefulStop gracefully shuts down the gRPC server per FR-8.1.
// Stops accepting new connections and waits for in-flight RPCs.
func (s *Server) GracefulStop() {
	s.logger.Info("initiating gRPC graceful shutdown")
	s.grpcSrv.GracefulStop()
	s.logger.Info("gRPC shutdown complete")
}

// Stop immediately stops the gRPC server without waiting.
func (s *Server) Stop() {
	s.grpcSrv.Stop()
}

// GRPCServer returns the underlying grpc.Server for registration.
func (s *Server) GRPCServer() *grpc.Server {
	return s.grpcSrv
}

// mapError converts service-layer errors to gRPC status codes per FR-7.7.
func mapError(err error) error {
	if err == nil {
		return nil
	}

	switch {
	case isError(err, model.ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	case isError(err, model.ErrInvalidInput):
		return status.Error(codes.InvalidArgument, err.Error())
	case isError(err, model.ErrAlreadyExists):
		return status.Error(codes.AlreadyExists, err.Error())
	case isError(err, model.ErrInvalidTransition):
		return status.Error(codes.FailedPrecondition, err.Error())
	case isError(err, model.ErrCycleDetected):
		return status.Error(codes.FailedPrecondition, err.Error())
	case isError(err, model.ErrConflict):
		return status.Error(codes.Aborted, err.Error())
	case isError(err, model.ErrAlreadyClaimed):
		return status.Error(codes.FailedPrecondition, err.Error())
	case isError(err, model.ErrNodeBlocked):
		return status.Error(codes.FailedPrecondition, err.Error())
	case isError(err, model.ErrStillDeferred):
		return status.Error(codes.FailedPrecondition, err.Error())
	case isError(err, model.ErrAgentStillActive):
		return status.Error(codes.FailedPrecondition, err.Error())
	case isError(err, model.ErrNoActiveSession):
		return status.Error(codes.FailedPrecondition, err.Error())
	case isError(err, model.ErrInvalidConfigKey):
		return status.Error(codes.InvalidArgument, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}

// isError checks if err wraps the target error.
func isError(err, target error) bool {
	return err != nil && (err == target || containsError(err, target))
}

// containsError unwraps the error chain to find the target.
func containsError(err, target error) bool {
	for {
		if err == target {
			return true
		}
		unwrapped, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapped.Unwrap()
		if err == nil {
			return false
		}
	}
}

// --- Interceptors ---

// recoveryInterceptor catches panics in unary RPCs and returns Internal error.
func (s *Server) recoveryInterceptor() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (resp any, err error) {
		defer func() {
			if r := recover(); r != nil {
				s.logger.Error("grpc panic recovered",
					"method", info.FullMethod, "panic", r)
				err = status.Error(codes.Internal, "internal error")
			}
		}()
		return handler(ctx, req)
	}
}

// loggingInterceptor logs unary RPC calls.
func (s *Server) loggingInterceptor() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		start := s.clock()
		resp, err := handler(ctx, req)
		duration := s.clock().Sub(start)

		level := slog.LevelInfo
		if err != nil {
			level = slog.LevelWarn
		}

		s.logger.Log(ctx, level, "grpc request",
			"method", info.FullMethod,
			"duration_ms", duration.Milliseconds(),
			"error", err)

		return resp, err
	}
}

// streamRecoveryInterceptor catches panics in streaming RPCs.
func (s *Server) streamRecoveryInterceptor() grpc.StreamServerInterceptor {
	return func(
		srv any,
		ss grpc.ServerStream,
		info *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) (err error) {
		defer func() {
			if r := recover(); r != nil {
				s.logger.Error("grpc stream panic recovered",
					"method", info.FullMethod, "panic", r)
				err = status.Error(codes.Internal, "internal error")
			}
		}()
		return handler(srv, ss)
	}
}

// streamLoggingInterceptor logs streaming RPC calls.
func (s *Server) streamLoggingInterceptor() grpc.StreamServerInterceptor {
	return func(
		srv any,
		ss grpc.ServerStream,
		info *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		start := s.clock()
		err := handler(srv, ss)
		duration := s.clock().Sub(start)

		level := slog.LevelInfo
		if err != nil {
			level = slog.LevelWarn
		}

		s.logger.Log(ss.Context(), level, "grpc stream",
			"method", info.FullMethod,
			"duration_ms", duration.Milliseconds(),
			"error", err)

		return err
	}
}
