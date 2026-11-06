// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/binary"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/stretchr/testify/require"
)

// daemonErrorHub speaks the real PostgreSQL startup protocol and refuses it
// with a typed server error; it never injects a fake daemon outcome.
func daemonErrorHub(t *testing.T, code string) (string, *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	calls := new(atomic.Int32)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			calls.Add(1)
			_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
			var header [4]byte
			if _, err := io.ReadFull(conn, header[:]); err == nil {
				size := binary.BigEndian.Uint32(header[:])
				if size >= 4 && size < 65536 {
					_, _ = io.CopyN(io.Discard, conn, int64(size-4))
					backend := pgproto3.NewBackend(conn, conn)
					backend.Send(&pgproto3.ErrorResponse{Severity: "ERROR", Code: code,
						Message: "connection refused postgres://user:daemon-outcome-secret@private.invalid/board"})
					_ = backend.Flush()
				}
			}
			_ = conn.Close()
		}
	}()
	t.Cleanup(func() { require.NoError(t, ln.Close()); <-done })
	return "postgres://user:daemon-outcome-secret@" + ln.Addr().String() + "/board?sslmode=disable", calls
}
