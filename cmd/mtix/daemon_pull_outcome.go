// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// daemonPullOutcome contains only safe labels; no attempt means retain the prior record (MTIX-112.20.7).
type daemonPullOutcome struct {
	attempted, ok, reported bool
	class                   string
}

// failedDaemonPull labels a failure without claiming that transport started (MTIX-112.20.7).
func failedDaemonPull(err error) daemonPullOutcome {
	return daemonPullOutcome{class: daemonPullErrorClass(err)}
}

// daemonPullErrorClass classifies typed causes before CLI formatting discards them (MTIX-112.20.7).
func daemonPullErrorClass(err error) string {
	var pg *pgconn.PgError
	var op *net.OpError
	var path *os.PathError
	var link *os.LinkError
	var network net.Error
	switch {
	case errors.As(err, &pg):
		return pullSQLStateClass(pg.Code)
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case isPullTLSFailure(err):
		return "tls"
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "unreachable"
	case errors.As(err, &op):
		return pullNetworkClass(op)
	case errors.As(err, &path), errors.As(err, &link):
		return "pull_failed"
	case errors.As(err, &network):
		return pullNetworkClass(network)
	}
	return "pull_failed"
}

// pullSQLStateClass accepts the whole auth class and transient startup refusal (MTIX-112.20.7).
func pullSQLStateClass(code string) string {
	switch {
	case strings.HasPrefix(code, "28"):
		return "auth"
	case code == "42501":
		return "refused"
	case code == "57014":
		return "timeout"
	case strings.HasPrefix(code, "08") || code == "57P03":
		return "unreachable"
	default:
		return "pull_failed"
	}
}

// pullNetworkClass does not treat local filesystem errors as network failures (MTIX-112.20.7).
func pullNetworkClass(err net.Error) string {
	if err.Timeout() {
		return "timeout"
	}
	return "unreachable"
}
