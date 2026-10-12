// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"github.com/jackc/pgx/v5"
)

type identityQuerier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func queryHubIdentity(ctx context.Context, pool identityQuerier, prefix string) (string, string, error) {
	rows, err := pool.Query(ctx, `SELECT project_prefix, first_event_hash FROM sync_projects WHERE project_prefix = $1`, prefix)
	if err != nil {
		return "", "", err
	}
	defer rows.Close()
	if !rows.Next() {
		return "", "", rows.Err()
	}
	var project, hash string
	if err := rows.Scan(&project, &hash); err != nil {
		return "", "", err
	}
	if err := rows.Err(); err != nil {
		return "", "", err
	}
	if hash == "" {
		return "", "", nil
	}
	return project, hash, nil
}
