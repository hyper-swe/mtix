// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestWSHub_Close_Idempotent(t *testing.T) {
	for _, tc := range []struct {
		name   string
		action func(*testing.T, *WSHub, *Server)
	}{
		{"close twice", func(_ *testing.T, hub *WSHub, _ *Server) {
			hub.Close()
			hub.Close()
		}},
		{"close then shutdown", func(t *testing.T, hub *WSHub, server *Server) {
			hub.Close()
			require.NoError(t, server.Shutdown(t.Context()))
		}},
		{"shutdown then close", func(t *testing.T, hub *WSHub, server *Server) {
			require.NoError(t, server.Shutdown(t.Context()))
			hub.Close()
		}},
		{"concurrent close", func(t *testing.T, hub *WSHub, _ *Server) {
			closeWSHubConcurrently(t, hub)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			hub := NewWSHub(logger)
			// Exercise the real Shutdown method without coupling this hub test
			// to database or HTTP server close behavior.
			server := &Server{wsHub: hub, logger: logger}
			runDone := make(chan struct{})
			go func() {
				hub.Run()
				close(runDone)
			}()
			t.Cleanup(func() {
				closeWSTestHub(hub)
				select {
				case <-runDone:
				case <-time.After(wsCompletionHangBound):
					t.Error("hub.Run did not stop during cleanup")
				}
			})

			// Call Close directly in the actions: the guarded fixture cleanup
			// must not mask a repeated-close panic in production.
			require.NotPanics(t, func() { tc.action(t, hub, server) })
			select {
			case <-hub.done:
			default:
				t.Fatal("Close must signal hub shutdown")
			}
			select {
			case <-runDone:
			case <-time.After(wsCompletionHangBound):
				t.Fatal("hub.Run did not exit after Close")
			}
		})
	}
}

func closeWSHubConcurrently(t *testing.T, hub *WSHub) {
	t.Helper()
	const callers = 32
	start := make(chan struct{})
	completed := make(chan struct{}, callers)
	panics := make(chan any, callers)
	for range callers {
		go func() {
			defer func() {
				if p := recover(); p != nil {
					panics <- p
				}
				completed <- struct{}{}
			}()
			<-start
			hub.Close()
		}()
	}
	close(start)
	guard := time.NewTimer(wsCompletionHangBound)
	defer guard.Stop()
	for range callers {
		select {
		case <-completed:
		case <-guard.C:
			t.Fatal("concurrent Close callers did not complete")
		}
	}
	select {
	case p := <-panics:
		t.Fatalf("concurrent Close panicked: %v", p)
	default:
	}
}
