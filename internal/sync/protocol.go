// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Package sync holds protocol-level constants and helpers shared by the
// sync transport and the CLI. The version-negotiation gate defined here
// is the real mechanism behind ADR-003 §7 Phase 1.5/3 ("a project cuts
// over only when all its CLIs report a compatible version") and the
// advisory drift check in docs/SYNC-DESIGN §6.3.
//
// Per docs/SECURITY-MODEL.md and ADR-003 §9, this gate is a *liveness*
// mechanism, not a security boundary: a stale or hostile CLI can at
// worst hold a project back from cutover; it cannot lose or corrupt a
// node. Callers therefore treat a gate result as advisory-to-migration,
// never as authorization.
package sync

import (
	"fmt"
	"strconv"
	"strings"
)

// SyncProtocolVersion is the wire protocol major version this build
// speaks, per docs/SYNC-DESIGN §4.1 (=1 at the v0.2 release). A change
// here is a protocol-major bump (§4.2) and gates the ADR-003 §7 Phase 3
// cutover to UID-keyed events.
const SyncProtocolVersion = 1

// UIDKeyedMinVersion is the lowest CLI build version that understands
// renumber/remap events and UID-keyed resolution, per ADR-003 §7
// Phase 1.5 (the version that may safely have the partial unique index
// and the Phase-3 cutover enabled). ProjectAllClientsAtLeast gates the
// migration on every active client meeting this minimum.
//
// It is a semantic-version string (no leading "v"); ParseVersion
// tolerates a leading "v" on inputs regardless.
const UIDKeyedMinVersion = "0.2.0"

// Version is a parsed semantic version. Only Major/Minor/Patch and an
// optional PreRelease tag participate in comparison; build metadata
// (after "+") is parsed off and ignored per the semver spec.
type Version struct {
	Major      int
	Minor      int
	Patch      int
	PreRelease string // e.g. "beta"; empty for a release version
}

// ParseVersion parses a semantic version string of the form
// "MAJOR.MINOR.PATCH" with an optional "-prerelease" and "+build"
// suffix. A single leading "v" is tolerated (CLI builds stamp "v0.2.0").
// Build metadata is discarded. Returns an error for any malformed input
// (a non-numeric component, a missing component, a negative number, or
// the sentinel "dev" build version).
func ParseVersion(s string) (Version, error) {
	raw := strings.TrimPrefix(s, "v")
	if raw == "" {
		return Version{}, fmt.Errorf("parse version %q: empty", s)
	}

	// Strip build metadata (+...) — it does not affect precedence.
	if plus := strings.IndexByte(raw, '+'); plus >= 0 {
		raw = raw[:plus]
	}

	// Split off the pre-release tag (-...).
	var pre string
	if dash := strings.IndexByte(raw, '-'); dash >= 0 {
		pre = raw[dash+1:]
		raw = raw[:dash]
		if pre == "" {
			return Version{}, fmt.Errorf("parse version %q: empty pre-release", s)
		}
	}

	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return Version{}, fmt.Errorf("parse version %q: want MAJOR.MINOR.PATCH, got %d components", s, len(parts))
	}

	nums := make([]int, 3)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return Version{}, fmt.Errorf("parse version %q: component %q not numeric", s, p)
		}
		if n < 0 {
			return Version{}, fmt.Errorf("parse version %q: negative component %d", s, n)
		}
		nums[i] = n
	}

	return Version{Major: nums[0], Minor: nums[1], Patch: nums[2], PreRelease: pre}, nil
}

// Compare returns -1, 0, or +1 as v sorts before, equal to, or after
// other. Numeric components compare numerically (so 0.10.0 > 0.9.0, not
// the reverse a lexical compare would give). A pre-release version sorts
// *below* the same release without a pre-release tag (semver rule: a
// release has higher precedence than its pre-releases); two pre-releases
// compare lexically on their tag.
func (v Version) Compare(other Version) int {
	if c := cmpInt(v.Major, other.Major); c != 0 {
		return c
	}
	if c := cmpInt(v.Minor, other.Minor); c != 0 {
		return c
	}
	if c := cmpInt(v.Patch, other.Patch); c != 0 {
		return c
	}
	// Same numeric core: a pre-release is lower than a release.
	switch {
	case v.PreRelease == "" && other.PreRelease == "":
		return 0
	case v.PreRelease == "": // v is a release, other is a pre-release
		return 1
	case other.PreRelease == "":
		return -1
	default:
		return strings.Compare(v.PreRelease, other.PreRelease)
	}
}

// CompareVersions parses a and b and returns Compare(a, b). Returns an
// error if either string fails to parse.
func CompareVersions(a, b string) (int, error) {
	va, err := ParseVersion(a)
	if err != nil {
		return 0, err
	}
	vb, err := ParseVersion(b)
	if err != nil {
		return 0, err
	}
	return va.Compare(vb), nil
}

// AtLeast reports whether the version string have is greater than or
// equal to minVer. Returns an error if either string fails to parse.
// This is the per-client predicate ProjectAllClientsAtLeast applies to
// each active CLI version across a project.
func AtLeast(have, minVer string) (bool, error) {
	c, err := CompareVersions(have, minVer)
	if err != nil {
		return false, err
	}
	return c >= 0, nil
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
