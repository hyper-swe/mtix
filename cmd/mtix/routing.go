// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

// pidLockFile is the PID lock file name within the .mtix directory.
const pidLockFile = "mtix.pid"

// exemptCommands are commands that bypass auto-routing per FR-14.1b.
// These always operate directly regardless of server state.
var exemptCommands = map[string]bool{
	"config":   true, // Reads/writes .mtix/config.yaml, no DB.
	"init":     true, // No project yet.
	"migrate":  true, // Must run before server.
	"docs":     true, // Writes files, no DB.
	"version":  true, // No DB needed.
	"help":     true, // No DB needed.
}

// adminRoutes maps CLI admin commands to REST admin endpoints per FR-14.1b.
var adminRoutes = map[string]adminRoute{
	"backup": {method: "POST", path: "/admin/backup"},
	"export": {method: "GET", path: "/admin/export"},
	"import": {method: "POST", path: "/admin/import"},
	"gc":     {method: "POST", path: "/admin/gc"},
	"verify": {method: "GET", path: "/admin/verify"},
}

// adminRoute describes how a CLI admin command maps to a REST endpoint.
type adminRoute struct {
	method string
	path   string
}

// shouldRouteToServer checks if a command should be routed to a running server.
// Returns the server port if routing should occur, or 0 if direct mode.
// Per FR-14.1b: if PID lock exists and process is alive, route through REST.
func shouldRouteToServer(cmd *cobra.Command) int {
	// Check if command is exempt from routing.
	if isExemptCommand(cmd) {
		return 0
	}

	mtixDir, err := findMtixDir()
	if err != nil {
		return 0
	}

	port, alive := readPIDLock(mtixDir)
	if !alive {
		return 0
	}

	return port
}

// isExemptCommand checks if a command should bypass auto-routing per FR-14.1b.
func isExemptCommand(cmd *cobra.Command) bool {
	name := cmd.Name()
	if exemptCommands[name] {
		return true
	}

	// Check parent command for subcommands (e.g., "config get").
	if cmd.Parent() != nil && exemptCommands[cmd.Parent().Name()] {
		return true
	}

	return false
}

// readPIDLock reads the PID lock file and checks if the process is alive.
// Returns (port, alive). A stale lock (dead process) returns (0, false).
func readPIDLock(mtixDir string) (int, bool) {
	lockPath := filepath.Join(mtixDir, pidLockFile)

	data, err := os.ReadFile(lockPath)
	if err != nil {
		return 0, false
	}

	// Parse lock file: "PID\nPORT\n"
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 2 {
		return 0, false
	}

	pid, err := strconv.Atoi(lines[0])
	if err != nil {
		return 0, false
	}

	port, err := strconv.Atoi(lines[1])
	if err != nil {
		return 0, false
	}

	// Check if process is alive using signal 0.
	if !isProcessAlive(pid) {
		// Stale lock — clean it up.
		_ = os.Remove(lockPath)
		return 0, false
	}

	return port, true
}

// writePIDLock creates the PID lock file with current PID and port.
func writePIDLock(mtixDir string, port int) error {
	lockPath := filepath.Join(mtixDir, pidLockFile)
	content := fmt.Sprintf("%d\n%d\n", os.Getpid(), port)
	return os.WriteFile(lockPath, []byte(content), 0o600)
}

// removePIDLock removes the PID lock file.
func removePIDLock(mtixDir string) {
	lockPath := filepath.Join(mtixDir, pidLockFile)
	_ = os.Remove(lockPath)
}

// isProcessAlive checks if a process with the given PID exists.
// Uses kill(pid, 0) which checks existence without sending a signal.
func isProcessAlive(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Signal 0 checks if process exists without sending a real signal.
	err = process.Signal(syscall.Signal(0))
	return err == nil
}

// routeToServer sends a CLI command through the running server's REST API
// per FR-14.1b. The routing is transparent — same flags, same output format.
func routeToServer(cmd *cobra.Command, args []string, port int) error {
	cmdName := cmd.Name()

	// Check if this is an admin command.
	if route, ok := adminRoutes[cmdName]; ok {
		return routeAdminCommand(route, port)
	}

	// For non-admin commands, route through the standard REST API.
	return routeStandardCommand(cmd, args, port)
}

// routeAdminCommand sends an admin command to the admin REST endpoint.
func routeAdminCommand(route adminRoute, port int) error {
	url := fmt.Sprintf("http://127.0.0.1:%d%s", port, route.path)

	ctx := context.Background()
	client := &http.Client{Timeout: 30 * time.Second}

	req, err := http.NewRequestWithContext(ctx, route.method, url, nil)
	if err != nil {
		return fmt.Errorf("create admin request: %w", err)
	}
	if route.method != "GET" {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("route to server: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	return handleRouteResponse(resp)
}

// routeStandardCommand routes a standard CLI command through the REST API.
func routeStandardCommand(cmd *cobra.Command, args []string, port int) error {
	cmdName := cmd.Name()
	baseURL := fmt.Sprintf("http://127.0.0.1:%d/api/v1", port)

	var url, method string
	var body io.Reader

	// Map CLI commands to REST API endpoints.
	switch cmdName {
	case "show":
		if len(args) < 1 {
			return fmt.Errorf("node ID required")
		}
		url = fmt.Sprintf("%s/nodes/%s", baseURL, args[0])
		method = "GET"
	case "list":
		url = fmt.Sprintf("%s/nodes", baseURL)
		method = "GET"
	case "search":
		if len(args) < 1 {
			return fmt.Errorf("search query required")
		}
		url = fmt.Sprintf("%s/nodes/search?q=%s", baseURL, args[0])
		method = "GET"
	case "tree":
		if len(args) < 1 {
			return fmt.Errorf("node ID required")
		}
		url = fmt.Sprintf("%s/nodes/%s/tree", baseURL, args[0])
		method = "GET"
	case "stats":
		url = fmt.Sprintf("%s/stats", baseURL)
		method = "GET"
	default:
		return fmt.Errorf("command %q not supported for server routing", cmdName)
	}

	ctx := context.Background()
	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("X-Requested-With", "mtix")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("route to server: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	return handleRouteResponse(resp)
}

// handleRouteResponse reads and displays the server response.
func handleRouteResponse(resp *http.Response) error {
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		var errResp struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error != "" {
			return fmt.Errorf("server error: %s", errResp.Error)
		}
		return fmt.Errorf("server returned %d: %s", resp.StatusCode, string(respBody))
	}

	// Output the response body.
	fmt.Print(string(respBody))
	return nil
}
