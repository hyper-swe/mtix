// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"database/sql"
	"fmt"
	"strings"
)

// fileURIPathFor escapes the characters that end a file: URI path, for the
// given GOOS. The OS is a parameter so the Windows branch runs on every host
// (MTIX-112.2.4). On Windows a drive path C:\x becomes /C:/x, the form SQLite
// reads, and a UNC path \\host\share becomes ////host/share (empty
// authority, then the //host/share path). A drive-relative C:dir stays as is.
func fileURIPathFor(path, goos string) string {
	if goos == "windows" {
		path = windowsURIPath(path)
	}
	return strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23").Replace(path)
}

// windowsURIPath rewrites a Windows path to its forward-slash URI path form.
func windowsURIPath(path string) string {
	slashed := strings.ReplaceAll(path, `\`, "/")
	switch {
	case strings.HasPrefix(slashed, "//"):
		return "//" + slashed
	case len(slashed) >= 3 && slashed[1] == ':' && slashed[2] == '/' && isASCIILetter(slashed[0]):
		return "/" + slashed
	default:
		return slashed
	}
}

func isASCIILetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

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
// only sql.Open("sqlite", ...) call in non-test code, guarded against an
// empty database beside a WAL (NFR-2.8); a source-scan test
// (dsn_single_builder_test.go) holds that line.
func openSQLite(path string, params ...string) (*sql.DB, error) {
	// Every production open passes here, so one guard covers them all: SQLite
	// deletes the WAL of an empty or one-byte database when it opens it.
	if err := guardEmptyDatabaseWAL(path); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", sqliteDSN(path, params...))
	if err != nil {
		return nil, fmt.Errorf("sql.Open: %w", err)
	}
	return db, nil
}

// readOnlyDSNParams are the DSN parameters of a store's read-only open
// (MTIX-58, MTIX-112.2.4): mode=ro makes the driver refuse a missing file
// instead of creating it, and query_only(1) is applied to every pooled
// connection so a write on the handle is rejected.
func readOnlyDSNParams() []string {
	return []string{"mode=ro", "_pragma=query_only(1)"}
}

// openShieldedSQLite opens the reader pool: the same database and DSN as
// openSQLite, with every connection shielded so a read cancelled mid-query
// cannot leave a stale snapshot on it (MTIX-250, read_shield.go). It stays in
// this file with the other driver opens (sql.OpenDB is allowed only here).
func openShieldedSQLite(path string, params ...string) (*sql.DB, error) {
	return openShieldedFrom(openSQLite, path, params...)
}

// openShieldedFrom is openShieldedSQLite with the plain open injected, so a test
// can observe that the driver probe handle is closed.
func openShieldedFrom(open func(string, ...string) (*sql.DB, error), path string, params ...string) (*sql.DB, error) {
	probe, err := open(path, params...)
	if err != nil {
		return nil, err
	}
	drv := probe.Driver()
	if err := probe.Close(); err != nil {
		return nil, fmt.Errorf("close driver probe: %w", err)
	}
	return sql.OpenDB(shieldConnector{drv: drv, dsn: sqliteDSN(path, params...)}), nil
}
