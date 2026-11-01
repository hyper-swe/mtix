// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/model"
)

// matchAnnotationRef finds the one annotation a reference names (FR-3.4,
// MTIX-100.3): an exact id first, then a unique prefix of at least
// model.AnnotationShortIDLen characters. It returns ErrInvalidInput for a
// reference that is empty, too short or ambiguous (listing the full ids) and
// ErrNotFound when nothing matches. Ids and the reference are rendered with
// format.EventID, so stored or typed text cannot reach a terminal raw.
func matchAnnotationRef(annotations []model.Annotation, ref string) (string, error) {
	if ref == "" {
		return "", fmt.Errorf("annotation id is empty: %w", model.ErrInvalidInput)
	}
	for _, a := range annotations {
		if a.ID == ref {
			return a.ID, nil
		}
	}
	var matches []string
	for _, a := range annotations {
		if strings.HasPrefix(a.ID, ref) {
			matches = append(matches, a.ID)
		}
	}
	if len(matches) > 0 && utf8.RuneCountInString(ref) < model.AnnotationShortIDLen {
		// A short fragment is refused even when it would be unique: prefixes
		// shorter than the printed short id are too easy to mistype or to
		// make ambiguous later when more annotations are added.
		return "", fmt.Errorf("annotation id prefix %s is shorter than the %d characters `mtix show` prints: use at least %d: %w",
			format.EventID(ref), model.AnnotationShortIDLen, model.AnnotationShortIDLen, model.ErrInvalidInput)
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("annotation %s: %w", format.EventID(ref), model.ErrNotFound)
	case 1:
		return matches[0], nil
	default:
		quoted := make([]string, len(matches))
		for i, id := range matches {
			quoted[i] = format.EventID(id)
		}
		return "", fmt.Errorf("annotation id prefix %s matches %d annotations (%s): give more characters: %w",
			format.EventID(ref), len(matches), strings.Join(quoted, ", "), model.ErrInvalidInput)
	}
}
