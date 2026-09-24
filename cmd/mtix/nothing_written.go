// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

// nothingWrittenError marks a command error raised before the command wrote
// anything to the store (MTIX-95.31.1). withAutoExport skips the auto-export
// for it: the store is unchanged, and re-exporting would only overwrite
// .mtix/tasks.json, which may hold a board just pulled from git, and its
// stored hash. The message and the error chain are those of the wrapped
// error.
type nothingWrittenError struct{ err error }

// Error returns the wrapped error's message.
func (e *nothingWrittenError) Error() string { return e.err.Error() }

// Unwrap exposes the wrapped error to errors.Is and errors.As.
func (e *nothingWrittenError) Unwrap() error { return e.err }

// nothingWritten wraps a non-nil err as a nothingWrittenError; nil stays nil.
func nothingWritten(err error) error {
	if err == nil {
		return nil
	}
	return &nothingWrittenError{err: err}
}
