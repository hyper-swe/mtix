// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"sync"

	"github.com/jackc/pgx/v5/pgconn"
)

// MaxLoggedWarnings caps a WarningLog. A caller treats any warning as a
// failure, so the first ones are enough to report it (MTIX-95.1).
const MaxLoggedWarnings = 64

// WarningLog records the WARNING notices the server sends on a pool's
// connections (MTIX-95.1). PostgreSQL reports some failures only as a
// WARNING, which the driver does not return as an error: a REVOKE by a
// role that did not grant the privilege is one. Pass Record as
// Options.OnNotice, then Take after each statement and treat any warning
// as a failure. Safe for concurrent use; a nil log records nothing.
type WarningLog struct {
	mu       sync.Mutex
	warnings []string
}

// NewWarningLog returns an empty WarningLog.
func NewWarningLog() *WarningLog {
	return &WarningLog{}
}

// Record keeps n's message when n is a WARNING and ignores lower
// severities. It fits Options.OnNotice. The severity is read from the
// field the server never translates, so a localized server is handled.
func (l *WarningLog) Record(n *pgconn.Notice) {
	if l == nil || n == nil {
		return
	}
	severity := n.SeverityUnlocalized
	if severity == "" {
		severity = n.Severity
	}
	if severity != "WARNING" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.warnings) < MaxLoggedWarnings {
		l.warnings = append(l.warnings, n.Message)
	}
}

// Take returns the warnings recorded since the last Take and clears the
// log.
func (l *WarningLog) Take() []string {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	out := l.warnings
	l.warnings = nil
	return out
}
