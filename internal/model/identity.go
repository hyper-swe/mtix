// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// Distributed node identity: provisional vs. settled display ids (ADR-003 §2,
// §4, §8, §13, §14).
//
// A node carries two identifiers (ADR-003 §2): the display_path (the dot-notation
// id, the only thing surfaced to humans/agents) and the uid (the node's
// create_node event_id, a UUIDv7, used internally and as the provisional path
// segment). A node whose trailing number is not yet hub-settled renders its
// unsettled segment from its uid:
//
//	settled:     PRJX-1.4
//	provisional: PRJX-1.<uid-segment>            (single level)
//	provisional: PRJX-1.<uid-segment>.1          (numeric child under it)
//
// The uid-segment is a SHORT, hyphenless, deterministic, display-only rendering
// of the full uid (ADR-003 §13); the full uid is stored, never the segment. The
// segment is marker-prefixed so it can never be mistaken for a base-10 sequence
// number — this makes provisional-vs-settled detectable from the string SHAPE
// alone, with no database access, which tooling and agents rely on (ADR-003 §8).
//
// A uid is an identifier, not a secret/capability (ADR-003 §14): these helpers
// expose only the id's shape and never treat knowing a uid as authorization.

// UIDSegmentMarker prefixes a uid-bearing display segment. It is a non-hex,
// non-digit letter so a uid segment can never be confused with a base-10
// sequence number, which keeps IsProvisional shape-only (ADR-003 §4, §8).
const UIDSegmentMarker = "u"

// uidSegmentHexLen is the number of leading hyphenless-hex digits kept in the
// short display segment. The full uid is stored separately (ADR-003 §13); the
// segment is display-only, so it is intentionally a lossy, short prefix. 12 hex
// digits (48 bits) is ample to keep collisions cosmetic within a single
// project's unsettled-node set while staying short and not leaking the full
// UUIDv7 timestamp (ADR-003 §14/F-6).
const uidSegmentHexLen = 12

// RenderUIDSegment renders a full node uid (a create_node event_id, ADR-003 §2)
// into its short, hyphenless, marker-prefixed display segment for use in a
// provisional display_path (ADR-003 §4, §13). The result is deterministic and
// guaranteed non-numeric. The full uid must be stored; this segment is display
// only and is intentionally lossy (resolve a segment back to a full uid via the
// store during the claim flow, ADR-003 §5).
//
// Returns ErrInvalidInput if uid is not a valid UUID.
func RenderUIDSegment(uid string) (string, error) {
	parsed, err := uuid.Parse(uid)
	if err != nil {
		return "", fmt.Errorf("invalid node uid %q: %w", uid, ErrInvalidInput)
	}
	hyphenless := strings.ReplaceAll(parsed.String(), "-", "")
	return UIDSegmentMarker + hyphenless[:uidSegmentHexLen], nil
}

// IsUIDSegment reports whether a single dot-segment is a well-formed uid-bearing
// display segment: the marker followed by a non-empty run of lowercase hex
// (ADR-003 §4). It rejects spoofed shapes (uppercase, non-hex, marker-only,
// embedded hyphens) so validation can accept provisional ids without accepting
// garbage (ADR-003 §14).
func IsUIDSegment(seg string) bool {
	body, ok := strings.CutPrefix(seg, UIDSegmentMarker)
	if !ok || body == "" {
		return false
	}
	for _, r := range body {
		if !isLowerHex(r) {
			return false
		}
	}
	return true
}

// isLowerHex reports whether r is a lowercase hexadecimal digit (0-9, a-f),
// the alphabet of a rendered uid segment body (ADR-003 §13).
func isLowerHex(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')
}

// IsProvisional reports whether a display_path is provisional — i.e. its number
// is not yet settled — using the STRING SHAPE alone, with no database access
// (ADR-003 §4, §8). A path is provisional when any dot-segment after the
// project-dash prefix is not a base-10 sequence number; equivalently, a path is
// settled only when every such segment is fully numeric (ADR-003 §4). This is
// the predicate tooling uses to warn before a provisional id is externalized
// (ADR-003 §8).
func IsProvisional(displayPath string) bool {
	_, rootSeg, childSegs, ok := splitID(displayPath)
	if !ok {
		// No recognizable PREFIX-<root> shape at all: not a node id, and in
		// particular not a settled one.
		return false
	}
	// The whole id is settled only when the root and every child segment are
	// base-10 numbers; any other segment shape makes it provisional.
	for _, seg := range allSegments(rootSeg, childSegs) {
		if !isBase10Seq(seg) {
			return true
		}
	}
	return false
}

// ParseUIDSegments returns, in path order, every uid-bearing display segment in
// displayPath (ADR-003 §4). It is the model-side inverse of RenderUIDSegment:
// it recovers the short segment(s) from a provisional id; mapping a short
// segment back to its full uid is a store lookup performed in the claim/resolve
// flow (ADR-003 §5), since the segment is intentionally lossy. A settled path
// yields an empty slice.
func ParseUIDSegments(displayPath string) []string {
	_, rootSeg, childSegs, ok := splitID(displayPath)
	if !ok {
		return nil
	}
	var out []string
	for _, seg := range allSegments(rootSeg, childSegs) {
		if IsUIDSegment(seg) {
			out = append(out, seg)
		}
	}
	return out
}

// allSegments returns the root sequence segment followed by the child segments,
// the order in which a display_path's segments appear (ADR-003 §4).
func allSegments(rootSeg string, childSegs []string) []string {
	return append([]string{rootSeg}, childSegs...)
}

// BuildProvisionalID builds a provisional display_path for a node with the given
// full uid under an existing parent display_path (ADR-003 §4): the parent path
// followed by the node's short uid segment, e.g. PRJX-1.<uid-segment>. The
// parent must be non-empty: a project root is always hub-settled in ADR-003's
// model, so only a child can be provisional. The full uid must be stored; only
// the short segment appears in the id (ADR-003 §13).
//
// Returns ErrInvalidInput if parentDisplayPath is empty or uid is not a valid
// UUID.
func BuildProvisionalID(parentDisplayPath, uid string) (string, error) {
	if parentDisplayPath == "" {
		return "", fmt.Errorf("provisional id requires a parent: %w", ErrInvalidInput)
	}
	seg, err := RenderUIDSegment(uid)
	if err != nil {
		return "", err
	}
	return parentDisplayPath + "." + seg, nil
}

// ValidateNodeID validates a node display_path's grammar, accepting both settled
// (fully numeric) and provisional (uid-bearing) ids while rejecting malformed
// ones (ADR-003 §4, §8). Grammar:
//
//	id      := PREFIX '-' root ( '.' segment )*
//	PREFIX  := the FR-2.1a project prefix
//	root    := base-10 sequence number          (the project root is always settled)
//	segment := base-10 sequence number | uid-segment
//
// A uid-segment makes the id provisional but still valid (ADR-003 §8:
// provisional ids are valid and resolvable). Only the shape is accepted; nothing
// about a uid is trusted beyond its shape (ADR-003 §14).
//
// Returns ErrInvalidInput describing the first violation.
func ValidateNodeID(id string) error {
	prefix, rootSeg, childSegs, ok := splitID(id)
	if !ok {
		return fmt.Errorf("invalid node id %q: missing prefix-dash: %w", id, ErrInvalidInput)
	}
	if err := ValidatePrefix(prefix); err != nil {
		return fmt.Errorf("invalid node id %q: %w", id, err)
	}
	// The project root sequence is always hub-assigned and numeric (ADR-003 §4):
	// a clean child cannot exist under an unsettled root.
	if !isBase10Seq(rootSeg) {
		return fmt.Errorf(
			"invalid node id %q: root segment %q must be numeric: %w",
			id, rootSeg, ErrInvalidInput,
		)
	}
	for _, seg := range childSegs {
		if !isBase10Seq(seg) && !IsUIDSegment(seg) {
			return fmt.Errorf(
				"invalid node id %q: segment %q is neither a number nor a uid segment: %w",
				id, seg, ErrInvalidInput,
			)
		}
	}
	return nil
}

// splitID lexically splits a node display_path into its project prefix, root
// sequence segment, and child segments, the parse shared by the identity
// helpers (ADR-003 §4). The grammar is PREFIX '-' root ( '.' child )*; the
// prefix itself may contain hyphens (FR-2.1a), so the prefix/root boundary is
// the LAST dash before the first dot. ok is false only when there is no such
// dash (no recognizable PREFIX-<root> shape); segment-level validity is the
// caller's concern.
func splitID(id string) (prefix, rootSeg string, childSegs []string, ok bool) {
	head, tail, hasDot := strings.Cut(id, ".")
	dashIdx := strings.LastIndex(head, "-")
	if dashIdx <= 0 {
		return "", "", nil, false
	}
	prefix = head[:dashIdx]
	rootSeg = head[dashIdx+1:]
	if hasDot {
		// Preserve empty segments (e.g. a trailing dot or "..") so callers
		// reject them rather than silently treating them as settled.
		childSegs = strings.Split(tail, ".")
	}
	return prefix, rootSeg, childSegs, true
}

// isBase10Seq reports whether seg is a non-empty run of ASCII decimal digits,
// i.e. a settled sequence segment. It deliberately accepts leading zeros and
// rejects signs/spaces so detection matches BuildID's numeric output exactly
// (ADR-003 §4).
func isBase10Seq(seg string) bool {
	if seg == "" {
		return false
	}
	for _, r := range seg {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
