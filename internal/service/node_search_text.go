// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"fmt"
	"strings"

	"github.com/hyper-swe/mtix/internal/model"
)

const (
	// MaxSearchWords is the most words one text query may carry (MTIX-146).
	MaxSearchWords = 16
	// MaxSearchWordLen is the longest single query word in bytes (MTIX-146).
	MaxSearchWordLen = 256
)

// SplitSearchText splits free text into query words for NodeFilter.Text.
// Whitespace separates words; a text with no words yields nil (MTIX-146).
func SplitSearchText(text string) []string {
	return strings.Fields(text)
}

// validateSearchText rejects a malformed text query before it reaches the
// store (MTIX-146, boundary validation): at most MaxSearchWords words, each
// non-blank, at most MaxSearchWordLen bytes, with no NUL byte.
//
// Returns ErrInvalidInput describing the first violation.
func validateSearchText(words []string) error {
	if len(words) > MaxSearchWords {
		return fmt.Errorf("search query has %d words, at most %d allowed: %w",
			len(words), MaxSearchWords, model.ErrInvalidInput)
	}
	for _, w := range words {
		switch {
		case strings.TrimSpace(w) == "":
			return fmt.Errorf("search query contains an empty word: %w", model.ErrInvalidInput)
		case len(w) > MaxSearchWordLen:
			return fmt.Errorf("search query word is %d bytes, at most %d allowed: %w",
				len(w), MaxSearchWordLen, model.ErrInvalidInput)
		case strings.ContainsRune(w, 0):
			return fmt.Errorf("search query contains a NUL byte: %w", model.ErrInvalidInput)
		}
	}
	return nil
}
