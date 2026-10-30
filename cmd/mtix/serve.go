// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	mtixhttp "github.com/hyper-swe/mtix/internal/api/http"
	"github.com/hyper-swe/mtix/internal/service"
)

// newServeCmd creates the mtix serve command per FR-6.4. Its help names
// only what serve starts: the HTTP server, which also serves the web UI
// and the WebSocket event stream (MTIX-95.14).
func newServeCmd() *cobra.Command {
	var (
		addr     string
		port     int
		insecure bool
	)

	cmd := &cobra.Command{
		Use:   "serve",
		Args:  cobra.NoArgs,
		Short: "Start the mtix HTTP and WebSocket server",
		RunE: func(c *cobra.Command, _ []string) error {
			return runServeCommand(serveRequest{
				Addr: addr, AddrChanged: c.Flags().Changed("addr"), Port: port, InsecureBind: insecure,
			})
		},
	}

	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1",
		"Bind address (default: the api.bind setting, else localhost); a non-loopback address needs --insecure-bind")
	cmd.Flags().BoolVar(&insecure, "insecure-bind", false,
		"Allow a non-loopback bind: the server requires a bearer token, created on first use in the operator-local state directory (NFR-5.2)")
	cmd.Flags().IntVar(&port, "port", 8377, "HTTP port")

	return cmd
}

func runServe(addr string, port int) error {
	return runServeWith(serveConfig(addr, port))
}

// runServeWith starts the HTTP server with a prepared configuration.
func runServeWith(cfg mtixhttp.ServerConfig) error {
	if app.store == nil {
		return fmt.Errorf("not in an mtix project")
	}

	// Mirror parity per FR-15.3 / MTIX-26.1: serve mutations reach the
	// tasks.json mirror without a process exit.
	defer wireMirrorExporter(app.logger)()

	// MTIX-53: dispatch hooks on every mutation host-side, same as the CLI and
	// MCP server, so a REST/gRPC mutation can fire an exec cold-start. After the
	// mirror wiring so it keeps on-commit slot 0.
	defer wireHookDispatch()()

	clock := func() time.Time { return time.Now().UTC() }

	dependencies := service.NewDependencyServiceFromNodeService(app.nodeSvc)
	admin := service.NewAdminService(app.store)
	services := mtixhttp.Services{
		Read: mtixhttp.ReadServices{
			Nodes: app.nodeSvc, Background: app.bgSvc, Sessions: app.sessionSvc,
			Agents: app.agentSvc, Config: app.configSvc, Dependencies: dependencies, Admin: admin,
		},
		Write: mtixhttp.WriteServices{
			Nodes: app.nodeSvc, Background: app.bgSvc, Sessions: app.sessionSvc,
			Agents: app.agentSvc, Config: app.configSvc, Dependencies: dependencies, Admin: admin,
		},
	}
	srv := mtixhttp.NewServer(services, app.logger, cfg, clock)

	fmt.Printf("Starting mtix server at %s:%s\n", cfg.Bind, cfg.Port)
	return srv.ListenAndServeWithGracefulShutdown()
}

// serveConfig builds the HTTP server configuration for `mtix serve`,
// including the build version injected at link time, which /health
// reports (FR-7.3b, MTIX-95.14).
func serveConfig(addr string, port int) mtixhttp.ServerConfig {
	return mtixhttp.ServerConfig{
		Bind:    addr,
		Port:    fmt.Sprintf("%d", port),
		Version: version,
	}
}
