// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"strconv"
	"strings"
)

// InboxChangeStore is the cheap change detector a parked inbox wait polls
// before it asks any inbox question (MTIX-326). The sqlite store provides it.
type InboxChangeStore interface {
	// InboxChangeToken returns an opaque token that differs after any commit to
	// the database. It opens no connection and runs no query.
	InboxChangeToken(ctx context.Context) (string, error)
}

const (
	// walIndexHeaderLen is how much of the -shm WAL index header the token
	// reads: iVersion, unused, iChange (offset 8), isInit and flags, szPage,
	// mxFrame (offset 16) and nPage (offset 20).
	walIndexHeaderLen = 24
	// dbHeaderLen covers the main database header through the file change
	// counter at bytes 24 to 27.
	dbHeaderLen = 28
)

// InboxChangeToken combines three file-level signals, none of which needs a
// connection or a query: the size and modification time of the database and
// its WAL, and a commit counter. In WAL mode the counter is iChange of the
// WAL index header in the -shm file, which SQLite increments on every
// commit, so a commit that leaves size and mtime unchanged (the WAL restarts
// in place after a checkpoint, on a filesystem with a coarse timestamp) still
// moves the token. Without a readable -shm (rollback-journal mode, a database
// never opened in WAL, or a platform that refuses to open the file while
// another process holds it) the counter is the file change counter in the main
// database header, which rollback-journal mode bumps on every commit. The -shm
// is only ever opened read-only. A pinned connection would starve the
// single-connection read-only store and a pooled connection's data_version is
// per connection, hence files. A change the token still misses is bounded by
// the caller's safety read.
func (s *Store) InboxChangeToken(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("inbox change token: %w", err)
	}
	var tok strings.Builder
	for _, p := range []string{s.dbPath, s.dbPath + "-wal"} {
		fi, err := os.Stat(p)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			tok.WriteString("-|")
		case err != nil:
			return "", fmt.Errorf("stat %s: %w", p, err)
		default:
			tok.WriteString(strconv.FormatInt(fi.Size(), 10) + ":" + strconv.FormatInt(fi.ModTime().UnixNano(), 10) + "|")
		}
	}
	if idx, ok := s.walIndexCounter(); ok {
		tok.WriteString("shm:" + idx)
		return tok.String(), nil
	}
	hdr, found, err := readHeader(s.dbPath, dbHeaderLen)
	if err != nil {
		return "", fmt.Errorf("inbox change token: %w", err)
	}
	if found {
		tok.WriteString("chg:" + hex.EncodeToString(hdr[24:28]))
	}
	return tok.String(), nil
}

// walIndexCounter reads the commit counter and frame count from the -shm WAL
// index header, read-only. It reports false when the file is absent, short or
// cannot be opened; an open failure other than absence is logged once.
func (s *Store) walIndexCounter() (string, bool) {
	shm := s.dbPath + "-shm"
	hdr, found, err := readHeader(shm, walIndexHeaderLen)
	if err != nil {
		if s.shmReported.CompareAndSwap(false, true) {
			s.logger.Warn("inbox change token: WAL index unreadable, using the database change counter",
				slog.String("path", shm), slog.Any("error", err))
		}
		return "", false
	}
	if !found {
		return "", false
	}
	return hex.EncodeToString(hdr[8:12]) + hex.EncodeToString(hdr[16:24]), true
}

// readHeader reads the first n bytes of path read-only. A missing or shorter
// file reports found=false with no error; any other failure is returned.
func readHeader(path string, n int) (hdr []byte, found bool, err error) {
	f, err := os.Open(path) //nolint:gosec // G304: the path is the store's own database file or its -shm sibling
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, n)
	if _, err := io.ReadFull(f, buf); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("read %s: %w", path, err)
	}
	return buf, true, nil
}
