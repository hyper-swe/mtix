// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package segment

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// FileExt is the segment file extension (FR-21 §5.2).
const FileExt = ".mrseg"

// filePrefix is the segment file name prefix (FR-21 §5.2).
const filePrefix = "seg-"

// fileDigits is the zero-padded width of the segment number in a file
// name. Numbers past this width keep every digit rather than
// truncating: ordering must survive a peer that outlives the padding.
const fileDigits = 8

// peerIDPattern is the FR-21 §5.2 identity grammar: the machine-hash
// prefix truncated to 16 hex characters, plus an optional
// operator-assigned label for seats whose environment churns.
var peerIDPattern = regexp.MustCompile(`^[0-9a-f]{16}(-[a-z0-9_-]{1,32})?$`)

// segFilePattern matches a segment file name. The number is required to
// be at least fileDigits wide so a hand-written "seg-1.mrseg" is a
// foreign entry rather than a silently reordered segment 1.
var segFilePattern = regexp.MustCompile(`^seg-([0-9]{8,})\.mrseg$`)

// ValidatePeerID checks id against the FR-21 §5.2 grammar. The grammar
// is also the path-safety boundary: it admits no separator, no dot, and
// no NUL, so a peer id from the medium can never escape the directory
// it names.
func ValidatePeerID(id string) error {
	if !peerIDPattern.MatchString(id) {
		return fmt.Errorf("%w: peer id %q", ErrForeignEntry, id)
	}
	return nil
}

// FileName renders the segment file name for a segment number.
func FileName(no uint64) string {
	return fmt.Sprintf("%s%0*d%s", filePrefix, fileDigits, no, FileExt)
}

// ParseFileName recovers the segment number from a file name. Naming
// carries ordering in this design — no manifest file is load-bearing
// (FR-21 §5.2) — so anything that is not exactly a segment name is a
// foreign entry rather than a best-effort guess.
func ParseFileName(name string) (uint64, error) {
	m := segFilePattern.FindStringSubmatch(name)
	if m == nil {
		return 0, fmt.Errorf("%w: segment file %q", ErrForeignEntry, name)
	}
	no, err := strconv.ParseUint(strings.TrimLeft(m[1], "0"), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: segment file %q: %v", ErrForeignEntry, name, err)
	}
	if no == 0 {
		// Segment numbers start at 1 (§5.2); zero would make the
		// "strictly consecutive" check ambiguous with "absent".
		return 0, fmt.Errorf("%w: segment file %q: segment number is zero", ErrForeignEntry, name)
	}
	return no, nil
}
