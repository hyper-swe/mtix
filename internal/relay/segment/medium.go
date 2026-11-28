// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package segment

import (
	"errors"
	"fmt"
	"io/fs"
	"syscall"
)

// CodeMediumUnreadable marks a relay segment, ack or meta file or directory
// that could not be opened, statted or read because of an I/O or permission
// error (FR-21 §9). The bytes were never read, so it is never
// RELAY_SEGMENT_CORRUPT. A file that does not exist keeps its own meaning and
// is not classified here. Recovery is on the medium: check that the relay
// directory is mounted and readable by this user, then rerun.
const CodeMediumUnreadable = "RELAY_MEDIUM_UNREADABLE"

// ErrMediumUnreadable is RELAY_MEDIUM_UNREADABLE.
var ErrMediumUnreadable = errors.New(CodeMediumUnreadable + ": relay medium could not be read")

// mediumError carries the verdict with a message built here, so it names the
// relay-relative path and the OS error class and never an absolute host path
// or file content. It still unwraps to the OS error for errors.Is.
type mediumError struct {
	rel   string
	class string
	cause error
}

func (e *mediumError) Error() string {
	return fmt.Sprintf("%s: cannot read %s: %s", CodeMediumUnreadable, e.rel, e.class)
}

func (e *mediumError) Unwrap() []error { return []error{ErrMediumUnreadable, e.cause} }

// MediumError classifies a failure to open, stat or read the relay path rel
// (relative to the relay directory). It returns err unchanged when err is nil,
// when the path does not exist, or when err already carries a verdict code
// (a symlink or foreign entry is not an I/O fault); every other failure
// becomes RELAY_MEDIUM_UNREADABLE.
func MediumError(rel string, err error) error {
	if err == nil || errors.Is(err, fs.ErrNotExist) || CodeOf(err) != "" {
		return err
	}
	return &mediumError{rel: rel, class: errorClass(err), cause: err}
}

// errorClass names the OS error without the path the OS error carries.
func errorClass(err error) string {
	if errors.Is(err, fs.ErrPermission) {
		return "permission denied"
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno.Error()
	}
	return "I/O error"
}
