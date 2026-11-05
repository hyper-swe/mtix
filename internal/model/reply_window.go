// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"
	"time"
)

// Reply-window bounds (MTIX-112.20.3, ADR-007 X6, C1, C2).
const (
	// DefaultReplyWindow is the --await-reply duration when none is given.
	DefaultReplyWindow = time.Hour
	// MaxReplyWindow is the longest a reply window or a recipient hold may last.
	MaxReplyWindow = 8 * time.Hour
)

// ParseAwaitReply parses an --await-reply duration. Empty means the default
// (one hour). The result is clamped to MaxReplyWindow; a zero or negative or
// malformed duration is ErrInvalidInput.
func ParseAwaitReply(value string) (time.Duration, error) {
	if value == "" {
		return DefaultReplyWindow, nil
	}
	d, err := time.ParseDuration(value)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("await-reply %q: want a positive duration such as 30m or 2h: %w", value, ErrInvalidInput)
	}
	return ClampReplyWindow(d), nil
}

// ClampReplyWindow limits a reply window to MaxReplyWindow.
func ClampReplyWindow(d time.Duration) time.Duration {
	if d > MaxReplyWindow {
		return MaxReplyWindow
	}
	return d
}

// FormatReplyBy renders a reply deadline as the additive payload value:
// RFC 3339 in UTC.
func FormatReplyBy(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// ParseReplyBy reads a payload reply_by. Absent or malformed is the zero time
// (no deadline): a bad value from another replica must never break ingest.
func ParseReplyBy(value string) time.Time {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}
