// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	decisionTimestamp = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]+)?(Z|[+-]([01][0-9]|2[0-3]):[0-5][0-9])$`)
	decisionKey       = regexp.MustCompile(`^[A-Za-z0-9:._/-]{1,128}$`)
	requestKey        = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	decisionCategory  = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)
	optionID          = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)
	sourceName        = regexp.MustCompile(`^[a-z_]{1,24}$`)
	receiptHash       = regexp.MustCompile(`^[A-Fa-f0-9]{0,128}$`)
	decisionEventID   = regexp.MustCompile(`^[a-fA-F0-9]{8}-[a-fA-F0-9]{4}-7[a-fA-F0-9]{3}-[89abAB][a-fA-F0-9]{3}-[a-fA-F0-9]{12}$`)
	decisionRef       = regexp.MustCompile(`^[A-Za-z0-9._:#/-]{1,128}$`)
)

func validDecisionURL(s string) bool {
	u, err := url.Parse(s)
	if err != nil || u.Hostname() == "" || strings.Contains(s, "\\") {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	return u.Scheme == "http" && slices.Contains([]string{"localhost", "127.0.0.1", "::1"}, strings.ToLower(u.Hostname()))
}

// A new authority value changes fold semantics. Ordinary shape/type errors
// stay malformed, even when the field is an authority field.
func checkedString(valid func(string) bool, authority bool) decisionReader[string] {
	return func(v decisionValue) string {
		s, ok := v.data.(string)
		if !ok {
			v.invalid()
			return ""
		}
		if valid != nil && !valid(s) {
			if authority {
				v.unsupported()
			} else {
				v.invalid()
			}
		}
		return s
	}
}

func oneOf(values ...string) func(string) bool {
	return func(s string) bool { return slices.Contains(values, s) }
}

func nonempty(s string) bool { return s != "" }
func atMostChars(n int) func(string) bool {
	return func(s string) bool { return utf8.RuneCountInString(s) <= n }
}

func atMostBytes(n int) func(string) bool {
	return func(s string) bool { return len(s) <= n }
}

func singleLine(s string) bool { return !strings.ContainsAny(s, "\r\n\u0085\u2028\u2029") }
func decisionQuestion(s string) bool {
	return nonempty(s) && singleLine(s) && utf8.RuneCountInString(s) <= 300
}

func decisionConsequence(s string) bool { return singleLine(s) && utf8.RuneCountInString(s) <= 160 }
func validDecisionTime(s string) bool {
	if !decisionTimestamp.MatchString(s) {
		return false
	}
	_, err := time.Parse(time.RFC3339, s)
	return err == nil
}

func validDecisionUTC(s string) bool {
	return validDecisionTime(s) && (strings.HasSuffix(s, "Z") || strings.HasSuffix(s, "+00:00"))
}
