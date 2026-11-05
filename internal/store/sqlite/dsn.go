// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"database/sql"
	"fmt"
	"strings"
)

// sqliteDSN builds the driver DSN for a database file as a file: URI with the
// path escaped (fileURIPath) and the given query parameters appended after
// it (MTIX-112.2.4). It is the only place a DSN is built: concatenating
// "?mode=ro" onto a raw path lets a '?', '#' or '%' in a directory name cut
// the path, so the driver silently opens or creates a different, empty
// database beside the real one.
func sqliteDSN(path string, params ...string) string {
	dsn := "file:" + fileURIPath(path)
	if len(params) > 0 {
		dsn += "?" + strings.Join(params, "&")
	}
	return dsn
}

// openSQLite opens the database file at path through sqliteDSN. It is the
// only sql.Open("sqlite", ...) call in non-test code; a source-scan test
// (dsn_single_builder_test.go) holds that line.
func openSQLite(path string, params ...string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", sqliteDSN(path, params...))
	if err != nil {
		return nil, fmt.Errorf("sql.Open: %w", err)
	}
	return db, nil
}
