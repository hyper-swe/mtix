// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package format

import (
	"fmt"
	"strings"

	"github.com/hyper-swe/mtix/internal/model"
)

// Provisional-id display + guardrail helpers (ADR-003 §8).
//
// A node's display_path is the only id surfaced to humans and agents. A path
// that still carries a uid segment is PROVISIONAL: it is valid and resolvable,
// but its eventual settled number will differ, so it must never be embedded in
// an immutable external artifact (git commit, PR body). A fully-numeric path is
// settled and safe to externalize (ADR-003 §8). Both the visible marker and the
// guardrail below decide provisional-vs-settled from the id SHAPE alone, via
// model.IsProvisional, with no database access (ADR-003 §4, §8). A uid is an
// identifier, not a secret (ADR-003 §14); these helpers only reflect an id's
// shape and never treat a uid as authorization.

// ProvisionalMarker is the visible suffix appended to a provisional id wherever
// an id is shown to a human or agent (ADR-003 §8). It is plain text so it
// survives copy/paste and is obvious in non-color output; it is appended (never
// substituted) so the underlying display_path stays usable verbatim.
const ProvisionalMarker = " (provisional)"

// AnnotateID returns id with ProvisionalMarker appended when id is a provisional
// display_path, and returns id unchanged otherwise (ADR-003 §8). The decision is
// shape-only via model.IsProvisional, so a settled (fully-numeric) id and any
// non-node string pass through untouched while single-level and deeply-nested
// provisional ids gain the marker. The original id is always preserved verbatim
// as a prefix so callers can still surface a copyable id.
func AnnotateID(id string) string {
	if model.IsProvisional(id) {
		return id + ProvisionalMarker
	}
	return id
}

// CheckExternalizable reports an error naming every provisional id among ids,
// the guardrail tooling calls before an id is written to an immutable external
// artifact (git commit, PR body) (ADR-003 §8). Detection is shape-only via
// model.IsProvisional, so settled (fully-numeric) ids and non-node strings pass
// and only true provisional shapes are refused. All offenders are reported (not
// just the first) so a caller can fix them in one pass. Returns nil when every
// id is safe to externalize.
func CheckExternalizable(ids ...string) error {
	var provisional []string
	for _, id := range ids {
		if model.IsProvisional(id) {
			provisional = append(provisional, id)
		}
	}
	if len(provisional) == 0 {
		return nil
	}
	return fmt.Errorf(
		"refusing to externalize provisional id(s) %s: their numbers are not yet "+
			"settled and will change — re-resolve via mtix and use the settled id "+
			"(ADR-003 §8): %w",
		strings.Join(provisional, ", "), model.ErrInvalidInput,
	)
}
