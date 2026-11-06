// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const salvageInboxScanSQL = `SELECT event_id,project_prefix,node_id,COALESCE(uid,''),
 author_id,payload,created_at,rowid,op_type FROM sync_events NOT INDEXED`

type salvageInboxRow struct {
	mail salvageMail
	op   string
}

// A table scan can see cells hidden by damaged rowid routing. Keep its readable
// rows in physical arrival order, then append rows only the verified walk sees (FR-19.1).
func collectSalvageInboxRows(ctx context.Context, db *sql.DB) ([]salvageInboxRow, error) {
	records, scanErr := scanSalvageInboxRows(ctx, db)
	scanRows := make(map[salvageInboxRow]bool, len(records))
	identities := make(map[string]bool, len(records))
	duplicateIdentity := false
	for _, row := range records {
		duplicateIdentity = duplicateIdentity || identities[row.mail.e.EventID]
		identities[row.mail.e.EventID] = true
		scanRows[row] = true
	}
	if duplicateIdentity {
		scanErr = errors.Join(scanErr, fmt.Errorf("duplicate journal identity detected before recipient filtering; journal incomplete, lost record count unknown"))
	}
	walk := inboxRowidWalk{next: 1}
	mismatch := false
	for {
		mail, op, ok := walk.read(ctx, db)
		if !ok {
			break
		}
		row := salvageInboxRow{mail: mail, op: op}
		if _, seen := scanRows[row]; seen {
			scanRows[row] = false
		} else {
			records = append(records, row)
			mismatch = true
		}
	}
	for _, missing := range scanRows {
		mismatch = mismatch || missing
	}
	err := errors.Join(scanErr, walk.resultError())
	if mismatch {
		err = errors.Join(err, fmt.Errorf("journal incomplete: sequential scan and verified rowid walk disagree; readable rows combined; lost record count unknown"))
	}
	return records, err
}

// scanSalvageInboxRows retains rows after a column conversion failure (FR-19.1).
func scanSalvageInboxRows(ctx context.Context, db *sql.DB) (records []salvageInboxRow, err error) {
	// A sequential scan can recover cells hidden by a corrupt rowid seek path.
	rows, err := db.QueryContext(ctx, salvageInboxScanSQL)
	if err != nil {
		return nil, fmt.Errorf("open sequential journal scan: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close sequential journal scan: %w", closeErr))
		}
	}()
	failed := 0
	var firstErr error
	for rows.Next() {
		var row salvageInboxRow
		e := &row.mail.e
		if scanErr := rows.Scan(&e.EventID, &e.Project, &e.NodeID, &e.UID, &e.Author, &e.Payload, &e.CreatedAt, &row.mail.seq, &row.op); scanErr != nil {
			failed++
			if firstErr == nil {
				firstErr = scanErr
			}
			continue
		}
		records = append(records, row)
	}
	if failed > 0 {
		err = fmt.Errorf("sequential journal scan: %d unreadable row(s): %w", failed, firstErr)
	}
	if readErr := rows.Err(); readErr != nil {
		err = errors.Join(err, fmt.Errorf("iterate sequential journal scan: %w", readErr))
	}
	return records, err
}
