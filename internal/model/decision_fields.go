// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	decisionTokenPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	decisionOptionID     = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)
	decisionKeyPattern   = regexp.MustCompile(`^[A-Za-z0-9:._/-]+$`)
	decisionCategory     = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)
	decisionSourceKey    = regexp.MustCompile(`^[a-z_]{1,24}$`)
	decisionOpaqueRef    = regexp.MustCompile(`^[A-Za-z0-9._:#/-]{1,128}$`)
	decisionDisplayEnum  = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)
	decisionChannel      = regexp.MustCompile(`^[a-z0-9][a-z0-9:_.-]{0,63}$`)
	decisionHex          = regexp.MustCompile(`^[0-9a-fA-F]+$`)
)

// decisionCheck collects the problems found in one decision object. A
// problem is either a broken field rule or an unknown authority enum value.
type decisionCheck struct {
	bad         []string
	unsupported []string
}

func (c *decisionCheck) fail(path, format string, args ...any) {
	c.bad = append(c.bad, path+": "+fmt.Sprintf(format, args...))
}

func (c *decisionCheck) unsup(path, format string, args ...any) {
	c.unsupported = append(c.unsupported, path+": "+fmt.Sprintf(format, args...))
}

// class returns the classification and its first reason.
func (c *decisionCheck) class() (DecisionClass, string) {
	switch {
	case len(c.bad) > 0:
		return DecisionMalformed, c.bad[0]
	case len(c.unsupported) > 0:
		return DecisionUnsupported, c.unsupported[0]
	}
	return DecisionValid, ""
}

// fieldObj reads typed fields from a parsed JSON object.
type fieldObj struct {
	c    *decisionCheck
	path string
	m    map[string]any
}

func (c *decisionCheck) root(v any) (*fieldObj, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		c.fail("decision", "must be an object")
		return nil, false
	}
	return &fieldObj{c: c, path: "decision", m: m}, true
}

func (o *fieldObj) at(key string) string { return o.path + "." + key }

// strRule describes the limits of a string field. Zero limits are not
// enforced. maxChars counts runes; maxBytes counts UTF-8 bytes.
type strRule struct {
	required bool
	minChars int
	maxChars int
	maxBytes int
	oneLine  bool
	pattern  *regexp.Regexp
}

func (o *fieldObj) str(key string, r strRule) string {
	raw, present := o.m[key]
	if !present || raw == nil {
		if r.required {
			o.c.fail(o.at(key), "required")
		}
		return ""
	}
	s, ok := raw.(string)
	if !ok {
		o.c.fail(o.at(key), "must be a string")
		return ""
	}
	o.checkStr(key, s, r)
	return s
}

func (o *fieldObj) checkStr(key, s string, r strRule) {
	n := utf8.RuneCountInString(s)
	switch {
	case n < r.minChars:
		o.c.fail(o.at(key), "shorter than %d characters", r.minChars)
	case r.maxChars > 0 && n > r.maxChars:
		o.c.fail(o.at(key), "longer than %d characters", r.maxChars)
	case r.maxBytes > 0 && len(s) > r.maxBytes:
		o.c.fail(o.at(key), "longer than %d bytes", r.maxBytes)
	case r.pattern != nil && !r.pattern.MatchString(s):
		o.c.fail(o.at(key), "does not match %s", r.pattern)
	case !plainText(s, r.oneLine):
		o.c.fail(o.at(key), "contains a control character or a line break that is not allowed")
	}
}

// plainText reports whether s holds no control characters other than tab,
// line feed and carriage return, and no line break at all when oneLine.
func plainText(s string, oneLine bool) bool {
	for _, r := range s {
		switch {
		case r == '\t':
		case r == '\n' || r == '\r' || r == ' ' || r == ' ' || r == '\u0085':
			if oneLine {
				return false
			}
		case unicode.IsControl(r):
			return false
		}
	}
	return true
}

// enum reads a string field restricted to allowed. An unknown string is an
// authority violation (unsupported); a non-string is malformed.
func (o *fieldObj) enum(key, def string, allowed ...string) string {
	raw, present := o.m[key]
	if !present || raw == nil {
		if def == "" {
			o.c.fail(o.at(key), "required")
		}
		return def
	}
	s, ok := raw.(string)
	if !ok {
		o.c.fail(o.at(key), "must be a string")
		return def
	}
	if !slices.Contains(allowed, s) {
		o.c.unsup(o.at(key), "unknown value %q", clip(s))
	}
	return s
}

func clip(s string) string {
	if len(s) > 40 {
		return s[:40] + "..."
	}
	return s
}

func (o *fieldObj) boolean(key string, def bool) bool {
	raw, present := o.m[key]
	if !present || raw == nil {
		return def
	}
	b, ok := raw.(bool)
	if !ok {
		o.c.fail(o.at(key), "must be a boolean")
		return def
	}
	return b
}

// integer reads a whole number in [min, 2^53].
func (o *fieldObj) integer(key string, required bool, minVal int64) (int64, bool) {
	raw, present := o.m[key]
	if !present || raw == nil {
		if required {
			o.c.fail(o.at(key), "required")
		}
		return 0, false
	}
	return o.asInt(key, raw, minVal)
}

func (o *fieldObj) asInt(key string, raw any, minVal int64) (int64, bool) {
	f, ok := raw.(float64)
	if !ok || f != math.Trunc(f) || math.Abs(f) > 1<<53 {
		o.c.fail(o.at(key), "must be an integer")
		return 0, false
	}
	if int64(f) < minVal {
		o.c.fail(o.at(key), "must be at least %d", minVal)
		return 0, false
	}
	return int64(f), true
}

func (o *fieldObj) object(key string, required bool) *fieldObj {
	raw, present := o.m[key]
	if !present || raw == nil {
		if required {
			o.c.fail(o.at(key), "required")
		}
		return nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		o.c.fail(o.at(key), "must be an object")
		return nil
	}
	return &fieldObj{c: o.c, path: o.at(key), m: m}
}

func (o *fieldObj) array(key string, required bool, maxLen int) []any {
	raw, present := o.m[key]
	if !present || raw == nil {
		if required {
			o.c.fail(o.at(key), "required")
		}
		return nil
	}
	a, ok := raw.([]any)
	if !ok {
		o.c.fail(o.at(key), "must be an array")
		return nil
	}
	if len(a) > maxLen {
		o.c.fail(o.at(key), "more than %d entries", maxLen)
		return nil
	}
	return a
}

// strings reads an array of strings, each checked with r. Entries are
// reported under the array's key.
func (o *fieldObj) strings(key string, maxLen int, r strRule) []string {
	out := []string{}
	for i, e := range o.array(key, false, maxLen) {
		s, ok := e.(string)
		if !ok {
			o.c.fail(fmt.Sprintf("%s[%d]", o.at(key), i), "must be a string")
			continue
		}
		sub := &fieldObj{c: o.c, path: fmt.Sprintf("%s[%d]", o.at(key), i), m: map[string]any{"": s}}
		sub.checkStr("", s, r)
		out = append(out, s)
	}
	return out
}

// timestamp reads an RFC 3339 UTC time ("Z" suffix).
func (o *fieldObj) timestamp(key string, required bool) string {
	s := o.str(key, strRule{required: required, maxChars: 40})
	if s == "" {
		return ""
	}
	if !validUTCTimestamp(s) {
		o.c.fail(o.at(key), "must be an RFC 3339 UTC time")
		return ""
	}
	return s
}

func validUTCTimestamp(s string) bool {
	if !strings.HasSuffix(s, "Z") {
		return false
	}
	_, err := time.Parse(time.RFC3339, s)
	return err == nil
}

// actor reads {kind, model}. decider.kind and the like are authority enums.
// With authority false an unknown kind is malformed instead of unsupported.
func (o *fieldObj) actor(key string, required, authority bool, def *DecisionActor) DecisionActor {
	sub := o.object(key, required && def == nil)
	if sub == nil {
		if def != nil {
			return *def
		}
		return DecisionActor{}
	}
	kind := sub.enum("kind", "", DecisionActorHuman, DecisionActorAgent)
	if !authority && len(o.c.unsupported) > 0 {
		o.c.fail(sub.at("kind"), "must be human or agent")
		o.c.unsupported = o.c.unsupported[:len(o.c.unsupported)-1]
	}
	return DecisionActor{
		Kind:  kind,
		Model: sub.str("model", strRule{maxChars: maxModelChars, oneLine: true}),
	}
}

// decisionEventID is the grammar of a decision event id reference.
var decisionEventID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// timestampAny reads an RFC 3339 time with any offset.
func (o *fieldObj) timestampAny(key string, required bool) string {
	s := o.str(key, strRule{required: required, maxChars: 40})
	if s == "" {
		return ""
	}
	if _, err := time.Parse(time.RFC3339, s); err != nil {
		o.c.fail(o.at(key), "must be an RFC 3339 time")
		return ""
	}
	return s
}
