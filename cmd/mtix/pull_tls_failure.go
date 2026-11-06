// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// isPullTLSFailure separates local trust/posture repair from server grants (MTIX-112.20.7).
func isPullTLSFailure(err error) bool {
	var authority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var certificate x509.CertificateInvalidError
	var header tls.RecordHeaderError
	var alert tls.AlertError
	switch {
	case errors.Is(err, transport.ErrTLSWeakWithoutFlag), errors.Is(err, transport.ErrTLSWeakNonLoopback),
		errors.Is(err, transport.ErrTLSUnverified), errors.As(err, &authority), errors.As(err, &hostname),
		errors.As(err, &certificate), errors.As(err, &header), errors.As(err, &alert):
		return true
	}
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "remote error" && op.Err != nil && strings.HasPrefix(op.Err.Error(), "tls:") {
		return true
	}
	// pgx exposes this protocol rejection only as a ConnectError with a fixed
	// driver message; arbitrary server SQL errors are classified before here.
	var connect *pgconn.ConnectError
	return errors.As(err, &connect) && strings.Contains(connect.Error(), "server refused TLS connection")
}
