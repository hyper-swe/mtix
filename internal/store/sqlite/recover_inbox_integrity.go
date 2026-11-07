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

// checkInboxIntegrity detects damage shared by both salvage reads (FR-19.1).
// A bounded check that cannot prove structural integrity is incomplete, not clean.
func checkInboxIntegrity(ctx context.Context, db *sql.DB) (err error) {
	ctx, cancel := context.WithTimeout(ctx, inboxIntegrityTimeout)
	defer cancel()
	// Check the entire source, including unreachable and multiply referenced pages.
	// Limit diagnostic rows as well as elapsed time; neither limit implies success.
	rows, err := db.QueryContext(ctx, `PRAGMA integrity_check(20)`)
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("journal integrity_check did not finish (5 second limit): %w", err)
		}
		return fmt.Errorf("journal integrity_check unavailable: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close journal integrity_check: %w", closeErr))
		}
	}()
	var problems []string
	verified := false
	for rows.Next() {
		var verdict string
		if scanErr := rows.Scan(&verdict); scanErr != nil {
			return fmt.Errorf("read journal integrity_check: %w", scanErr)
		}
		if verdict == "ok" {
			verified = true
		} else {
			problems = append(problems, verdict)
		}
	}
	if readErr := rows.Err(); readErr != nil {
		return fmt.Errorf("journal integrity_check did not finish (5 second limit): %w", readErr)
	}
	if len(problems) > 0 {
		return fmt.Errorf("journal integrity_check detected damage; lost record count unknown: %s", strings.Join(problems, "; "))
	}
	if !verified {
		return fmt.Errorf("journal integrity_check returned no integrity verdict; lost record count unknown")
	}
	return nil
}
