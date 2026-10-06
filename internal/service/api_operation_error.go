// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import "fmt"

// apiOperationError preserves the existing transport message per FR-7.7 while
// carrying service operation context and the complete backend error chain.
// Only newly extracted API operations use it; existing domain errors are intact.
type apiOperationError struct {
	operation string
	message   string
	wrapped   error
}

func wrapAPIOperation(operation string, cause error) error {
	return &apiOperationError{operation: operation, message: cause.Error(), wrapped: fmt.Errorf("%s: %w", operation, cause)}
}

func (e *apiOperationError) Error() string { return e.message }
func (e *apiOperationError) Unwrap() error { return e.wrapped }

// Operation exposes service context without altering the public error message.
func (e *apiOperationError) Operation() string { return e.operation }
