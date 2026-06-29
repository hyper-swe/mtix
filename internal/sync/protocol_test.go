// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sync_test

import (
	"testing"

	syncpkg "github.com/hyper-swe/mtix/internal/sync"
	"github.com/stretchr/testify/require"
)

// TestConstants pins the protocol/min-version constants so an
// accidental edit (which would silently re-gate every project) trips a
// test. See ADR-003 §7 Phase 1.5/3.
func TestConstants(t *testing.T) {
	require.Equal(t, 1, syncpkg.SyncProtocolVersion,
		"SyncProtocolVersion must match SYNC-DESIGN §4.1 (=1 at v0.2)")
	require.NotEmpty(t, syncpkg.UIDKeyedMinVersion,
		"UIDKeyedMinVersion must be a non-empty semver string")
	// The min version MUST be a parseable semver.
	_, err := syncpkg.ParseVersion(syncpkg.UIDKeyedMinVersion)
	require.NoError(t, err, "UIDKeyedMinVersion must parse as semver")
}

func TestParseVersion(t *testing.T) {
	cases := []struct {
		in      string
		major   int
		minor   int
		patch   int
		pre     string
		wantErr bool
	}{
		{in: "0.1.0", major: 0, minor: 1, patch: 0},
		{in: "1.2.3", major: 1, minor: 2, patch: 3},
		{in: "v1.2.3", major: 1, minor: 2, patch: 3}, // leading v tolerated
		{in: "0.2.0-beta", major: 0, minor: 2, patch: 0, pre: "beta"},
		// CORNER: double-digit components must not sort lexically.
		{in: "0.10.0", major: 0, minor: 10, patch: 0},
		{in: "10.0.0", major: 10, minor: 0, patch: 0},
		{in: "1.0.12", major: 1, minor: 0, patch: 12},
		// CORNER: a CLI version string carrying build metadata.
		{in: "1.2.3-beta+build.5", major: 1, minor: 2, patch: 3, pre: "beta"},
		// EDGE: malformed.
		{in: "", wantErr: true},
		{in: "1.2", wantErr: true},
		{in: "1.2.x", wantErr: true},
		{in: "a.b.c", wantErr: true},
		{in: "dev", wantErr: true},
		{in: "-1.0.0", wantErr: true},
		{in: "1.2.3-", wantErr: true}, // EDGE: trailing dash, empty pre-release tag
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			v, err := syncpkg.ParseVersion(c.in)
			if c.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, c.major, v.Major)
			require.Equal(t, c.minor, v.Minor)
			require.Equal(t, c.patch, v.Patch)
			require.Equal(t, c.pre, v.PreRelease)
		})
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a    string
		b    string
		want int // sign of Compare(a,b)
	}{
		{a: "1.0.0", b: "1.0.0", want: 0},
		{a: "1.0.1", b: "1.0.0", want: 1},
		{a: "1.0.0", b: "1.0.1", want: -1},
		{a: "1.1.0", b: "1.0.9", want: 1},
		{a: "2.0.0", b: "1.9.9", want: 1},
		// CORNER: double-digit — 0.10.0 > 0.9.0 numerically.
		{a: "0.10.0", b: "0.9.0", want: 1},
		{a: "0.9.0", b: "0.10.0", want: -1},
		{a: "1.0.12", b: "1.0.9", want: 1},
		// CORNER: pre-release is LOWER than its release (semver rule).
		{a: "1.0.0-beta", b: "1.0.0", want: -1},
		{a: "1.0.0", b: "1.0.0-beta", want: 1},
		// EDGE: two pre-releases compared lexically on the tag.
		{a: "1.0.0-alpha", b: "1.0.0-beta", want: -1},
		{a: "1.0.0-beta", b: "1.0.0-beta", want: 0},
		// EDGE: leading v normalization on one side only.
		{a: "v1.2.3", b: "1.2.3", want: 0},
	}
	for _, c := range cases {
		t.Run(c.a+"_vs_"+c.b, func(t *testing.T) {
			got, err := syncpkg.CompareVersions(c.a, c.b)
			require.NoError(t, err)
			require.Equal(t, c.want, sign(got))
		})
	}
}

func TestCompareVersions_Errors(t *testing.T) {
	_, err := syncpkg.CompareVersions("dev", "1.0.0")
	require.Error(t, err)
	_, err = syncpkg.CompareVersions("1.0.0", "garbage")
	require.Error(t, err)
}

func TestAtLeast(t *testing.T) {
	cases := []struct {
		have string
		min  string
		want bool
	}{
		{have: "1.0.0", min: "1.0.0", want: true},  // exactly the min passes
		{have: "1.0.1", min: "1.0.0", want: true},  // above passes
		{have: "0.9.9", min: "1.0.0", want: false}, // below fails
		// CORNER: pre-release of the min target does NOT meet the min.
		{have: "1.0.0-beta", min: "1.0.0", want: false},
		// CORNER: double-digit minor meets a single-digit min.
		{have: "0.10.0", min: "0.9.0", want: true},
	}
	for _, c := range cases {
		t.Run(c.have+"_ge_"+c.min, func(t *testing.T) {
			got, err := syncpkg.AtLeast(c.have, c.min)
			require.NoError(t, err)
			require.Equal(t, c.want, got)
		})
	}
}

func TestAtLeast_Error(t *testing.T) {
	_, err := syncpkg.AtLeast("dev", syncpkg.UIDKeyedMinVersion)
	require.Error(t, err)
}

func sign(n int) int {
	switch {
	case n > 0:
		return 1
	case n < 0:
		return -1
	default:
		return 0
	}
}
