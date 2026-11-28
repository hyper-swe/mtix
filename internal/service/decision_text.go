// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"fmt"
	"strings"

	"golang.org/x/text/unicode/norm"

	"github.com/hyper-swe/mtix/internal/model"
)

// combiningGraphemeJoiner is the code point x/text inserts when a run of
// combining marks exceeds the stream-safe limit.
const combiningGraphemeJoiner = "\u034f"

// NormalizeDecisionText returns the comparison value of a decision answer's
// free text: surrounding Unicode white space trimmed, then exact Unicode NFC
// (ADR-008 section 3.2). It never affects the wire hash.
//
// The Unicode library applies stream-safe processing, which inserts a
// combining grapheme joiner into a run of more than 30 combining marks. That
// would make two notes that differ under exact NFC compare equal, hiding a
// conflict, so such text is refused with model.ErrInvalidInput instead of
// being rewritten. An explicit joiner already in the input is preserved.
func NormalizeDecisionText(s string) (string, error) {
	trimmed := strings.TrimSpace(s)
	normalized := norm.NFC.String(trimmed)
	if strings.Count(normalized, combiningGraphemeJoiner) != strings.Count(trimmed, combiningGraphemeJoiner) {
		return "", fmt.Errorf("decision text has a run of more than 30 combining marks: %w", model.ErrInvalidInput)
	}
	return normalized, nil
}

// ValidateDecisionAnswer checks the request-dependent wire fields of an
// answer, comparing free text by NormalizeDecisionText (ADR-008 section 3.2).
// Authorisation and journal references are resolved separately.
func ValidateDecisionAnswer(request, answer model.DecisionPayload) error {
	if err := model.ValidateDecisionAnswer(request, answer, NormalizeDecisionText); err != nil {
		return fmt.Errorf("validate decision answer: %w", err)
	}
	return nil
}
