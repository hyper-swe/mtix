// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
)

// InboxChangeStore is the cheap change detector a parked inbox wait polls
// before it asks any inbox question (MTIX-326). The sqlite store provides it.
type InboxChangeStore interface {
	// InboxChangeToken returns an opaque token that differs after any commit to
	// the database. It opens no connection and runs no query.
	InboxChangeToken(ctx context.Context) (string, error)
}

// InboxChangeToken stats the database and its WAL: every commit in WAL mode
// grows or rewrites the -wal file and every checkpoint rewrites the main
// file, so a changed size or modification time means something was written.
// A connection pinned for the wait would starve the single-connection
// read-only store, and a pooled connection's data_version is per connection,
// hence the stat. A missed change is bounded by the caller's safety read.
func (s *Store) InboxChangeToken(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("inbox change token: %w", err)
	}
	tok := ""
	for _, p := range []string{s.dbPath, s.dbPath + "-wal"} {
		fi, err := os.Stat(p)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			tok += "-|"
		case err != nil:
			return "", fmt.Errorf("stat %s: %w", p, err)
		default:
			tok += strconv.FormatInt(fi.Size(), 10) + ":" + strconv.FormatInt(fi.ModTime().UnixNano(), 10) + "|"
		}
	}
	return tok, nil
}
