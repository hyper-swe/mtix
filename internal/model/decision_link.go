// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"
	"net/url"
	"strings"
)

// ValidateDecisionLinkURL applies the link rule: https is allowed; http is
// allowed only to 127.0.0.1, [::1] and localhost on any port. Every other
// scheme, other http hosts and URLs with user info are refused. It returns
// a wrapped ErrInvalidInput.
func ValidateDecisionLinkURL(raw string) error {
	if raw == "" || len(raw) > maxLinkURLBytes {
		return fmt.Errorf("link url must be 1 to %d bytes: %w", maxLinkURLBytes, ErrInvalidInput)
	}
	for _, r := range raw {
		if r <= ' ' || r == 0x7f {
			return fmt.Errorf("link url contains a space or control character: %w", ErrInvalidInput)
		}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("link url does not parse: %w", ErrInvalidInput)
	}
	if u.User != nil || strings.Contains(u.Host, "@") {
		return fmt.Errorf("link url must not carry user info: %w", ErrInvalidInput)
	}
	if u.Hostname() == "" || u.Opaque != "" {
		return fmt.Errorf("link url needs a host: %w", ErrInvalidInput)
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return nil
	case "http":
		switch strings.ToLower(u.Hostname()) {
		case "127.0.0.1", "::1", "localhost":
			return nil
		}
		return fmt.Errorf("http links are allowed only to a loopback host: %w", ErrInvalidInput)
	}
	return fmt.Errorf("link url scheme %q is not allowed: %w", clip(u.Scheme), ErrInvalidInput)
}
