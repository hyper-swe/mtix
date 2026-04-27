// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package postgres

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// dockerProvider spins up an ephemeral postgres container via the local
// docker CLI. We use the CLI rather than testcontainers-go to keep the
// dependency surface small (no new go.mod entries) and to make the harness
// trivially debuggable: every action is a docker subcommand a developer
// can run by hand.
//
// Lifecycle per test:
//  1. Setup() runs `docker run -d --rm -e POSTGRES_PASSWORD=... -P postgres:16-alpine`
//  2. Polls `pg_isready` (via `docker exec`) until ready or startupTimeout elapses.
//  3. Returns a DSN pointing at the published 5432 host port.
//  4. Cleanup runs `docker rm -f <container>`.
//
// All long-running subprocesses are bounded by ctx; tests pass t.Context()
// (Go 1.25+) so cancellation propagates cleanly on test failure.
type dockerProvider struct {
	cfg providerConfig
}

func newDockerProvider(cfg providerConfig) *dockerProvider {
	return &dockerProvider{cfg: cfg}
}

func (p *dockerProvider) Name() string                      { return ProviderDocker }
func (p *dockerProvider) SupportsAdvisoryLocks() bool       { return true }
func (p *dockerProvider) SupportsPreparedStatements() bool  { return true }

// Setup launches a postgres container. On any failure (docker missing,
// container won't start, port detection fails) the test is skipped via
// t.Skipf rather than failed — a missing local Docker should not break a
// developer's `make test`.
func (p *dockerProvider) Setup(ctx context.Context, t *testing.T) (string, func()) {
	t.Helper()

	if !dockerAvailable(p.cfg.dockerCmd) {
		t.Skipf("%v: docker binary %q not on PATH", ErrProviderUnavailable, p.cfg.dockerCmd)
	}

	containerName := "mtix-test-pg-" + uniqueSuffix()
	dbName := uniqueDBName(p.cfg.suiteTag)
	password := uniqueSuffix() // random per-container; never logged
	startCtx, cancel := context.WithTimeout(ctx, p.cfg.startupTimeout)
	defer cancel()

	id, err := p.runContainer(startCtx, containerName, dbName, password)
	if err != nil {
		t.Skipf("%v: failed to start postgres container: %v", ErrProviderUnavailable, err)
	}

	cleanup := func() {
		// Use a fresh, bounded context: the test context may already be
		// cancelled by the time t.Cleanup runs.
		killCtx, killCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer killCancel()
		_ = exec.CommandContext(killCtx, p.cfg.dockerCmd, "rm", "-f", id).Run() //nolint:gosec // controlled args
	}
	t.Cleanup(cleanup)

	port, err := p.publishedPort(startCtx, id)
	if err != nil {
		cleanup()
		t.Skipf("%v: could not detect published port: %v", ErrProviderUnavailable, err)
	}

	if err := p.waitReady(startCtx, id); err != nil {
		cleanup()
		t.Skipf("%v: postgres failed readiness probe: %v", ErrProviderUnavailable, err)
	}

	dsn := fmt.Sprintf("postgres://postgres:%s@127.0.0.1:%s/%s?sslmode=disable",
		password, port, dbName)
	return dsn, cleanup
}

// runContainer launches the postgres container in detached mode and
// returns the container ID. The published port is auto-assigned by docker
// (-P) so concurrent test runs do not collide.
func (p *dockerProvider) runContainer(ctx context.Context, name, db, password string) (string, error) {
	args := []string{
		"run", "-d", "--rm",
		"--name", name,
		"-e", "POSTGRES_PASSWORD=" + password,
		"-e", "POSTGRES_DB=" + db,
		"-P",
		p.cfg.dockerImage,
	}
	out, err := exec.CommandContext(ctx, p.cfg.dockerCmd, args...).Output() //nolint:gosec // controlled args
	if err != nil {
		return "", wrapExecErr("docker run", err)
	}
	id := strings.TrimSpace(string(out))
	if id == "" {
		return "", errors.New("docker run returned empty container id")
	}
	return id, nil
}

// publishedPort queries docker for the host port mapped to the container's
// 5432/tcp. Returns the port as a string (so it can be plugged straight
// into a DSN).
func (p *dockerProvider) publishedPort(ctx context.Context, id string) (string, error) {
	out, err := exec.CommandContext(ctx, p.cfg.dockerCmd, //nolint:gosec // controlled args
		"port", id, "5432/tcp").Output()
	if err != nil {
		return "", wrapExecErr("docker port", err)
	}
	// Output looks like "0.0.0.0:55432\n[::]:55432\n". Pick the first port.
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if i := strings.LastIndex(line, ":"); i != -1 && i+1 < len(line) {
			return strings.TrimSpace(line[i+1:]), nil
		}
	}
	return "", fmt.Errorf("could not parse port from %q", string(out))
}

// waitReady polls pg_isready inside the container until it succeeds or the
// context expires. We probe via docker exec (rather than a host-side TCP
// dial) so we exercise the same auth path the test will use.
func (p *dockerProvider) waitReady(ctx context.Context, id string) error {
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("readiness timeout: %w", ctx.Err())
		case <-tick.C:
			cmd := exec.CommandContext(ctx, p.cfg.dockerCmd, //nolint:gosec // controlled args
				"exec", id, "pg_isready", "-U", "postgres")
			if err := cmd.Run(); err == nil {
				return nil
			}
		}
	}
}

// wrapExecErr standardizes subprocess error reporting. We deliberately
// strip stderr (which can contain DSN-shaped strings if a misconfigured
// container echoes its env) and return only the canonical reason.
func wrapExecErr(action string, err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return fmt.Errorf("%s: exit %d", action, ee.ExitCode())
	}
	return fmt.Errorf("%s: %w", action, err)
}
