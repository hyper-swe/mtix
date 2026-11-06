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
// prefix in physical arrival order, then append rows only the verified walk sees.
func collectSalvageInboxRows(ctx context.Context, db *sql.DB) ([]salvageInboxRow, error) {
	records, scanErr := scanSalvageInboxRows(ctx, db)
	scanRows := make(map[salvageInboxRow]bool, len(records))
	for _, row := range records {
		scanRows[row] = true
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

func scanSalvageInboxRows(ctx context.Context, db *sql.DB) (records []salvageInboxRow, err error) {
	rows, err := db.QueryContext(ctx, salvageInboxScanSQL)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var row salvageInboxRow
		e := &row.mail.e
		if err := rows.Scan(&e.EventID, &e.Project, &e.NodeID, &e.UID, &e.Author, &e.Payload, &e.CreatedAt, &row.mail.seq, &row.op); err != nil {
			return records, err
		}
		records = append(records, row)
	}
	return records, rows.Err()
}
