// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package oplocal

import (
	"errors"
	"fmt"
)

// nativeFlushError marks a write failure, rather than an unreadable record.
// Publication may already have completed when a directory or final flush fails.
type nativeFlushError struct{ cause error }

func (e nativeFlushError) Error() string     { return fmt.Sprintf("flush operator state: %v", e.cause) }
func (e nativeFlushError) Unwrap() error     { return e.cause }
func (nativeFlushError) nativeFlushFailure() {}

func isNativeFlushFailure(err error) bool {
	var marked interface{ nativeFlushFailure() }
	return errors.As(err, &marked)
}

func writeStateError(err error) error {
	if isNativeFlushFailure(err) {
		return fmt.Errorf("write state: %w", err)
	}
	return fmt.Errorf("write state: %w: %w", err, invalid("write failed"))
}
