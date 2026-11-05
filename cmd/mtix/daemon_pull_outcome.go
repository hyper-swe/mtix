// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/x509"
	"errors"
	"net"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// daemonPullOutcome carries no error text, credential or hub identity.
// The zero value means no pull happened, including a pre-cancelled pass.
type daemonPullOutcome struct {
	attempted bool
	ok        bool
	class     string
}

func failedDaemonPull(err error) daemonPullOutcome {
	return daemonPullOutcome{attempted: true, class: daemonPullErrorClass(err)}
}

func daemonPullErrorClass(err error) string {
	var pg *pgconn.PgError
	var network net.Error
	var authority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalidCert x509.CertificateInvalidError
	switch {
	case errors.As(err, &pg):
		switch {
		case strings.HasPrefix(pg.Code, "28"):
			return "auth"
		case pg.Code == "42501":
			return "refused"
		case pg.Code == "57014":
			return "timeout"
		case strings.HasPrefix(pg.Code, "08") || pg.Code == "57P03":
			return "unreachable"
		}
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, transport.ErrTLSWeakWithoutFlag), errors.Is(err, transport.ErrTLSWeakNonLoopback),
		errors.Is(err, transport.ErrTLSUnverified), errors.As(err, &authority), errors.As(err, &hostname), errors.As(err, &invalidCert):
		return "refused"
	case errors.As(err, &network):
		if network.Timeout() {
			return "timeout"
		}
		return "unreachable"
	}
	return "pull_failed"
}
