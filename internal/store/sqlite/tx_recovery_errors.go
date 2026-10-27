// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"database/sql"
	"database/sql/driver"
	"errors"
)

// isOnlyClosedConnError refuses to hide a genuine recovery failure beside a
// closed-connection cause in a joined error (MTIX-198.4).
func isOnlyClosedConnError(err error) bool {
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		causes := joined.Unwrap()
		if len(causes) == 0 {
			return false
		}
		for _, cause := range causes {
			if !isOnlyClosedConnError(cause) {
				return false
			}
		}
		return true
	}
	if cause := errors.Unwrap(err); cause != nil {
		return isOnlyClosedConnError(cause)
	}
	return errors.Is(err, driver.ErrBadConn) || errors.Is(err, sql.ErrConnDone)
}
