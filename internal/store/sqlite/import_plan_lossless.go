// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"unicode/utf8"
)

// A staged plan keeps each workflow conflict as JSON in a row of plan_rows
// (MTIX-2.3.2.2, FR-7.8). encoding/json replaces invalid UTF-8 with U+FFFD, but
// the text report quotes the stored bytes as they are (a local title or a
// history value is whatever the store holds), so a conflict is stored through
// an escape that keeps every byte: the report of a staged plan is then
// byte-identical to the in-memory report for any store.

const (
	// losslessMark starts an escape: it is followed by 'm' for the mark itself,
	// or by a rune in the range losslessByteBase+0x80 .. +0xFF for an invalid byte.
	losslessMark = ''
	// losslessByteBase is the first rune of the private-use block that stands
	// for the bytes 0x00 .. 0xFF.
	losslessByteBase = 0xE100
	// losslessMarkSuffix follows the mark to stand for the mark itself.
	losslessMarkSuffix = 'm'
)

// escapeLossless returns s with every byte that is not part of a valid UTF-8
// sequence, and every mark, replaced by an escape, so the result is valid UTF-8
// and unescapeLossless returns s.
func escapeLossless(s string) string {
	if utf8.ValidString(s) && !strings.ContainsRune(s, losslessMark) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1: // not a valid sequence: one byte
			b.WriteRune(losslessMark)
			b.WriteRune(losslessByteBase + rune(s[i]))
		case r == losslessMark:
			b.WriteRune(losslessMark)
			b.WriteRune(losslessMarkSuffix)
		default:
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	return b.String()
}

// unescapeLossless reverses escapeLossless.
func unescapeLossless(s string) string {
	if !strings.ContainsRune(s, losslessMark) {
		return s
	}
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		if r != losslessMark || i >= len(s) {
			out = append(out, s[i-size:i]...)
			continue
		}
		next, nextSize := utf8.DecodeRuneInString(s[i:])
		i += nextSize
		switch {
		case next == losslessMarkSuffix:
			out = utf8.AppendRune(out, losslessMark)
		case next >= losslessByteBase+0x80 && next <= losslessByteBase+0xFF:
			out = append(out, byte(next-losslessByteBase)) //nolint:gosec // G115: next-base is within 0x80..0xFF, checked above
		default: // not an escape this code wrote: keep it as it is
			out = append(out, s[i-size-nextSize:i]...)
		}
	}
	return string(out)
}

// mapStrings returns a deep copy of v with fn applied to every string reachable
// through its exported fields, pointers and slices. A nil slice or pointer
// stays nil, an empty slice stays empty, and unexported fields are copied as
// they are.
func mapStrings(v reflect.Value, fn func(string) string) reflect.Value {
	switch v.Kind() {
	case reflect.String:
		return reflect.ValueOf(fn(v.String())).Convert(v.Type())
	case reflect.Pointer:
		if v.IsNil() {
			return v
		}
		out := reflect.New(v.Type().Elem())
		out.Elem().Set(mapStrings(v.Elem(), fn))
		return out
	case reflect.Slice:
		if v.IsNil() {
			return v
		}
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := 0; i < v.Len(); i++ {
			out.Index(i).Set(mapStrings(v.Index(i), fn))
		}
		return out
	case reflect.Struct:
		out := reflect.New(v.Type()).Elem()
		out.Set(v)
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				out.Field(i).Set(mapStrings(v.Field(i), fn))
			}
		}
		return out
	default:
		return v
	}
}

// encodeWorkflowConflict returns the JSON of c with every string escaped, so
// that decodeWorkflowConflict returns c byte for byte.
func encodeWorkflowConflict(c WorkflowConflict) (string, error) {
	escaped, ok := mapStrings(reflect.ValueOf(c), escapeLossless).Interface().(WorkflowConflict)
	if !ok {
		return "", fmt.Errorf("escape workflow conflict %s: unexpected type", c.ID)
	}
	raw, err := json.Marshal(escaped)
	if err != nil {
		return "", fmt.Errorf("marshal workflow conflict %s: %w", c.ID, err)
	}
	return string(raw), nil
}

// decodeWorkflowConflict reverses encodeWorkflowConflict.
func decodeWorkflowConflict(raw string) (WorkflowConflict, error) {
	var escaped WorkflowConflict
	if err := json.Unmarshal([]byte(raw), &escaped); err != nil {
		return escaped, fmt.Errorf("decode workflow conflict: %w", err)
	}
	c, ok := mapStrings(reflect.ValueOf(escaped), unescapeLossless).Interface().(WorkflowConflict)
	if !ok {
		return escaped, fmt.Errorf("unescape workflow conflict %s: unexpected type", escaped.ID)
	}
	return c, nil
}
