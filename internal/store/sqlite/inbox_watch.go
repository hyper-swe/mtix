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
	"strings"
)

// InboxChangeStore is the cheap change detector a parked inbox wait polls
// before it asks any inbox question (MTIX-326). The sqlite store provides it.
type InboxChangeStore interface {
	// InboxChangeToken returns an opaque token that differs after a commit to
	// the database. It opens no connection, opens no file and runs no query.
	InboxChangeToken(ctx context.Context) (string, error)
}

// tokenStatFunc is the only file-system call the change token makes. It is a
// parameter so a test can pin that rule (MTIX-326).
type tokenStatFunc func(name string) (fs.FileInfo, error)

// InboxChangeToken is the stat of the database file, its -wal and its -shm:
// for each, whether it exists, its size and its modification time in
// nanoseconds. It needs no connection and no query.
//
// DESIGN RULE (MTIX-326, review round 2 S1): this process holds the SQLite
// connections, and SQLite takes POSIX fcntl locks on the database and -shm
// inodes. POSIX releases every lock a process holds on an inode when ANY file
// descriptor for that inode is closed, so opening and closing the database,
// the -wal or the -shm here, even read-only, would silently drop SQLite's own
// locks and can corrupt the database. The token therefore never opens, reads
// or closes those files; it only calls os.Stat, which takes no descriptor.
//
// Known gap: a commit that leaves the size and the nanosecond mtime of all
// three files unchanged (a WAL restarted in place after a checkpoint on a
// filesystem with a coarse timestamp) does not move the token. The caller's
// 60 second safety read (inboxSafetyFloor) catches it, and a wait that holds
// events keeps its 2 second full read, so the miss is bounded.
func (s *Store) InboxChangeToken(ctx context.Context) (string, error) {
	return inboxChangeTokenFor(ctx, s.dbPath, os.Stat)
}

// inboxChangeTokenFor builds the token from stat alone (see InboxChangeToken).
func inboxChangeTokenFor(ctx context.Context, dbPath string, stat tokenStatFunc) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("inbox change token: %w", err)
	}
	var tok strings.Builder
	for _, p := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		fi, err := stat(p)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			tok.WriteString("-|")
		case err != nil:
			return "", fmt.Errorf("stat %s: %w", p, err)
		default:
			tok.WriteString(strconv.FormatInt(fi.Size(), 10) + ":" + strconv.FormatInt(fi.ModTime().UnixNano(), 10) + "|")
		}
	}
	return tok.String(), nil
}
