// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const inboxIntegrityTimeout = 5 * time.Second

var errInboxIntegrityUnfinished = errors.New("journal integrity_check did not finish")

// checkInboxIntegrity detects damage shared by both salvage reads (FR-19.1).
// A bounded check that cannot prove structural integrity is incomplete, not clean.
func checkInboxIntegrity(ctx context.Context, db *sql.DB, limit time.Duration) (err error) {
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	// Check the entire source, including unreachable and multiply referenced pages.
	// Limit diagnostic rows as well as elapsed time; neither limit implies success.
	rows, err := db.QueryContext(ctx, `PRAGMA integrity_check(20)`)
	if err != nil {
		return inboxIntegrityReadError(ctx, limit, "unavailable", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			err = errors.Join(err, inboxIntegrityReadError(ctx, limit, "close failed", closeErr))
		}
	}()
	var problems []string
	verified := false
	for rows.Next() {
		var verdict string
		if scanErr := rows.Scan(&verdict); scanErr != nil {
			return inboxIntegrityReadError(ctx, limit, "read failed", scanErr)
		}
		if verdict == "ok" {
			verified = true
		} else {
			problems = append(problems, verdict)
		}
	}
	if readErr := rows.Err(); readErr != nil {
		return inboxIntegrityReadError(ctx, limit, "failed", readErr)
	}
	if len(problems) > 0 {
		return fmt.Errorf("journal integrity_check detected damage; lost record count unknown: %s", strings.Join(problems, "; "))
	}
	if !verified {
		return fmt.Errorf("journal integrity_check returned no integrity verdict; lost record count unknown")
	}
	return nil
}

// Only an expired context justifies naming the limit; corruption is a failure.
func inboxIntegrityReadError(ctx context.Context, limit time.Duration, operation string, cause error) error {
	if ctx.Err() == nil {
		return fmt.Errorf("journal integrity_check %s: %w", operation, cause)
	}
	label := limit.String() + " limit"
	if limit == inboxIntegrityTimeout {
		label = "5 second limit"
	}
	return fmt.Errorf("%w (%s): %w", errInboxIntegrityUnfinished, label, cause)
}
