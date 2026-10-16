// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package format

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/hyper-swe/mtix/internal/model"
)

// validateRecipient mirrors the MTIX-125 local-agent contract, with a required
// recipient. It preserves accepted bytes and keeps sender validation separate.
func validateRecipient(agent string) error {
	if agent == "" {
		return fmt.Errorf("recipient agent ID is required: %w", model.ErrInvalidInput)
	}
	if strings.TrimSpace(agent) == "" {
		return fmt.Errorf("recipient agent ID must not be whitespace-only: %w", model.ErrInvalidInput)
	}
	if len(agent) > 64 {
		return fmt.Errorf("recipient agent ID exceeds 64 UTF-8 bytes: %w", model.ErrInvalidInput)
	}
	for _, r := range agent {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return fmt.Errorf("recipient agent ID contains control or invisible formatting character U+%04X: %w", r, model.ErrInvalidInput)
		}
	}
	return nil
}

// quoteRecipient returns one POSIX single-quoted word without normalizing bytes.
func quoteRecipient(agent string) string { return "'" + strings.ReplaceAll(agent, "'", `'\''`) + "'" }

// maxBoardPathBytes bounds the displayed board root (MTIX-189).
const maxBoardPathBytes = 4096

// BoardSelection returns the trailing " -C '<board>'" word appended to every
// displayed command, or "" when no board is given (MTIX-189). The board must
// be an absolute path without control or invisible formatting characters, so
// the displayed command is one safe POSIX word that cannot break the line.
func BoardSelection(board string) (string, error) {
	if board == "" {
		return "", nil
	}
	if !filepath.IsAbs(board) {
		return "", fmt.Errorf("envelope board %q must be an absolute path: %w", board, model.ErrInvalidInput)
	}
	if len(board) > maxBoardPathBytes {
		return "", fmt.Errorf("envelope board path exceeds %d bytes: %w", maxBoardPathBytes, model.ErrInvalidInput)
	}
	if !utf8.ValidString(board) {
		return "", fmt.Errorf("envelope board path is not valid UTF-8: %w", model.ErrInvalidInput)
	}
	for _, r := range board {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return "", fmt.Errorf("envelope board path contains control or invisible formatting character U+%04X: %w", r, model.ErrInvalidInput)
		}
	}
	return " -C " + quoteRecipient(board), nil
}
