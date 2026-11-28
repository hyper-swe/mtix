// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Package segment implements the on-disk segment format of the FR-21
// file relay transport: framing, CRC32C, the HMAC layout, epochs, tail
// semantics, segment rotation, and a symlink-refusing directory walker.
//
// The package is deliberately pure — it imports no store, starts no
// goroutines, and adds no modules beyond the standard library. Its only
// external contact is regular file data, which ADR-005 names the single
// primitive a shared medium may be trusted to carry. Nothing here may
// grow a dependency on advisory locks, mmap, kernel rendezvous objects,
// rename atomicity, or fsync ordering: those either do not cross a
// mount boundary or are unverifiable through one, and a design that
// leans on them fails exactly where this transport is meant to work.
//
// The rules the rest of the relay inherits from this layer:
//
//   - Every byte is validated at read time. A record carries its own
//     length, CRC, and MAC, so a reader never needs the medium's
//     cooperation — or the file's completeness — to know what is true.
//   - A short or invalid frame at the tail of the *active* segment is
//     "not yet written" (FR-21 §5.4). The identical condition in a
//     *sealed* segment is damage, and the reader stops loudly rather
//     than skipping: a skipped gap can drop a causal predecessor, and a
//     loud stall is strictly better than a quiet hole.
//   - Contiguity is keyed on (pub_epoch, rs), never on rs alone
//     (FR-21 §5.3), so a publisher restore is a visible epoch
//     transition rather than a silent overwrite of consumed positions.
//
// Verdicts are the exported RELAY_* sentinels below; callers dispatch
// with errors.Is and surface CodeOf(err) to operators.
package segment

import (
	"errors"
	"fmt"
)

// Verdict codes surfaced to operators through status and doctor. They
// are contract: the FR-21 release-gate scenarios match on these
// strings, and each names one recovery.
const (
	// CodeSymlink refuses a symlink anywhere under the relay directory
	// (FR-21 §5.1). The relay medium is writable by software other than
	// mtix, so the CWE-59 lesson is applied preemptively rather than
	// after a report.
	CodeSymlink = "RELAY_SYMLINK"

	// CodeForeignEntry marks a directory entry that does not match the
	// §5.2 grammar. Foreign entries are ignored by the data path and
	// flagged by doctor — never parsed, never deleted.
	CodeForeignEntry = "RELAY_FOREIGN_ENTRY"

	// CodeSegmentCorrupt marks bytes that fail validation where they
	// cannot be explained by an append in flight (FR-21 §5.4).
	// Recovery is the writer republishing or the reader re-bootstrapping.
	CodeSegmentCorrupt = "RELAY_SEGMENT_CORRUPT"

	// CodeGap marks a break in (pub_epoch, rs) contiguity: a missing
	// predecessor, a repeated position, or an epoch moving backwards.
	// The reader stalls rather than skipping past it.
	CodeGap = "RELAY_GAP"

	// CodePeerIDConflict marks a peer directory whose newest segment is
	// not this writer's own continuation (FR-21 §5.2) — the self-check
	// that stands in for the cross-host mutual exclusion this design
	// deliberately does not attempt.
	CodePeerIDConflict = "RELAY_PEER_ID_CONFLICT"
)

// Verdict sentinels. Callers MUST dispatch with errors.Is; the wrapped
// text is informational and may name the offending path or position.
var (
	// ErrSymlink is RELAY_SYMLINK.
	ErrSymlink = errors.New(CodeSymlink + ": symlink under the relay directory")

	// ErrForeignEntry is RELAY_FOREIGN_ENTRY.
	ErrForeignEntry = errors.New(CodeForeignEntry + ": entry does not match the relay grammar")

	// ErrSegmentCorrupt is RELAY_SEGMENT_CORRUPT. Every frame-level
	// failure below wraps it, so a reader of a sealed segment branches
	// on one sentinel while still reporting the specific cause.
	ErrSegmentCorrupt = errors.New(CodeSegmentCorrupt + ": segment failed validation")

	// ErrGap is RELAY_GAP.
	ErrGap = errors.New(CodeGap + ": relay sequence is not contiguous")

	// ErrPeerIDConflict is RELAY_PEER_ID_CONFLICT.
	ErrPeerIDConflict = errors.New(CodePeerIDConflict + ": peer directory is not this writer's continuation")
)

// Frame-level causes. Each wraps ErrSegmentCorrupt: the cause tells an
// operator what broke, the wrapped sentinel tells the code what to do.
// Whether a given cause is fatal depends on where it was found, which
// is the reader's call under the §5.4 tail rule — not the parser's.
var (
	// ErrBadMagic marks a frame that does not begin with its magic.
	ErrBadMagic = fmt.Errorf("%w: frame magic mismatch", ErrSegmentCorrupt)

	// ErrPayloadTooLarge marks a length prefix above MaxPayloadBytes.
	// Per FR-21 §5.3 such a prefix is treated as corruption and is
	// never honored — the parser must not size a buffer from it.
	ErrPayloadTooLarge = fmt.Errorf("%w: payload length above the cap", ErrSegmentCorrupt)

	// ErrCRCMismatch marks a payload that fails its CRC32C.
	ErrCRCMismatch = fmt.Errorf("%w: payload CRC32C mismatch", ErrSegmentCorrupt)

	// ErrMACMismatch marks a record that fails authentication. Because
	// the MAC binds position and both epochs (FR-21 §5.3), this is also
	// the verdict for a valid record relocated to another position,
	// file, or epoch.
	ErrMACMismatch = fmt.Errorf("%w: record MAC mismatch", ErrSegmentCorrupt)

	// ErrBadFormatVersion marks a header whose major version this build
	// cannot read (FR-21 §10).
	ErrBadFormatVersion = fmt.Errorf("%w: unsupported format version", ErrSegmentCorrupt)

	// ErrBadPeerIDField marks a header peer_id field that is not
	// NUL-padded grammar-conforming ASCII.
	ErrBadPeerIDField = fmt.Errorf("%w: header peer_id field malformed", ErrSegmentCorrupt)
)

// ErrIncomplete reports that a frame is not fully present yet. It
// deliberately does NOT wrap ErrSegmentCorrupt: at the tail of the
// active segment this is the expected steady state of an append in
// flight, and treating it as damage would stall the fleet on every
// write (FR-21 §5.4).
var ErrIncomplete = errors.New("frame incomplete")

// ErrInvalidTransition marks a lifecycle transition the segment state
// machine does not permit. It is a caller bug, not a medium verdict,
// and so carries no RELAY_* code.
var ErrInvalidTransition = errors.New("invalid segment lifecycle transition")

// ErrUnauthenticatedKey marks a key supplied for a segment whose
// authenticated flag is clear, or a missing key for one whose flag is
// set. FR-21 §5.3 forbids mixed modes within one relay, so the
// mismatch is refused rather than resolved.
var ErrUnauthenticatedKey = fmt.Errorf("%w: authentication mode mismatch", ErrSegmentCorrupt)

// CodeOf returns the RELAY_* code a verdict carries, or "" when err is
// nil or is not one of this package's verdicts. Causes report the code
// of the verdict they wrap, so a CRC failure reports
// RELAY_SEGMENT_CORRUPT.
func CodeOf(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrSymlink):
		return CodeSymlink
	case errors.Is(err, ErrForeignEntry):
		return CodeForeignEntry
	case errors.Is(err, ErrGap):
		return CodeGap
	case errors.Is(err, ErrPeerIDConflict):
		return CodePeerIDConflict
	case errors.Is(err, ErrSegmentCorrupt):
		return CodeSegmentCorrupt
	default:
		return ""
	}
}

// IsIncomplete reports whether err means "not written yet" rather than
// "wrong". Readers of the active segment stop cleanly on it; readers of
// a sealed segment convert it to a corruption verdict.
func IsIncomplete(err error) bool {
	return err != nil && errors.Is(err, ErrIncomplete)
}
