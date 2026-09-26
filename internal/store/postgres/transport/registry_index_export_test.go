// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"context"
	"errors"
)

// BuildRegistryIndexForTest runs the registry index build with leftOut as
// the creates it leaves out, on one pooled connection, so the external
// test package can make the build itself fail; it returns that
// connection's statement_timeout after the build and the build's error
// (MTIX-95.44).
func BuildRegistryIndexForTest(ctx context.Context, p *Pool, leftOut []string) (string, error) {
	conn, err := p.p.Acquire(ctx)
	if err != nil {
		return "", err
	}
	defer conn.Release()
	buildErr := createRegistryIndex(ctx, conn, leftOut)
	var timeout string
	if err := conn.QueryRow(ctx, `SELECT pg_catalog.current_setting('statement_timeout')`).Scan(&timeout); err != nil {
		return "", errors.Join(buildErr, err)
	}
	return timeout, buildErr
}
