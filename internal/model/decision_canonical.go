// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"
)

// CanonicalJSON parses raw (a duplicate member name replaces the earlier
// one) and returns its RFC 8785 (JSON Canonicalization Scheme) form. It
// returns a wrapped ErrInvalidInput when raw is not valid I-JSON.
func CanonicalJSON(raw []byte) ([]byte, error) {
	v, _, err := parseJSON(raw, MaxDecisionCanonicalDepth)
	if err != nil {
		return nil, err
	}
	return canonicalValue(v)
}

// JSONHasDuplicateNames reports whether any object in raw repeats a member
// name. It returns a wrapped ErrInvalidInput when raw is not valid I-JSON.
func JSONHasDuplicateNames(raw []byte) (bool, error) {
	_, dup, err := parseJSON(raw, MaxDecisionCanonicalDepth)
	return dup, err
}

// canonicalValue serializes a parsed generic value in RFC 8785 form.
func canonicalValue(v any) ([]byte, error) {
	var buf bytes.Buffer
	if err := writeCanonical(&buf, v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeCanonical(buf *bytes.Buffer, v any) error {
	switch t := v.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		buf.WriteString(strconv.FormatBool(t))
	case float64:
		s, err := formatECMAScriptNumber(t)
		if err != nil {
			return err
		}
		buf.WriteString(s)
	case string:
		writeCanonicalString(buf, t)
	case []any:
		buf.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeCanonical(buf, e); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case map[string]any:
		return writeCanonicalObject(buf, t)
	default:
		return fmt.Errorf("canonical json: unsupported type %T: %w", v, ErrInvalidInput)
	}
	return nil
}

func writeCanonicalObject(buf *bytes.Buffer, m map[string]any) error {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, compareUTF16)
	buf.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		writeCanonicalString(buf, k)
		buf.WriteByte(':')
		if err := writeCanonical(buf, m[k]); err != nil {
			return err
		}
	}
	buf.WriteByte('}')
	return nil
}

// compareUTF16 orders strings by their UTF-16 code units, as RFC 8785
// requires for member names.
func compareUTF16(a, b string) int {
	ua, ub := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	return slices.Compare(ua, ub)
}

func writeCanonicalString(buf *bytes.Buffer, s string) {
	buf.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			buf.WriteString(`\"`)
		case r == '\\':
			buf.WriteString(`\\`)
		case r == '\b':
			buf.WriteString(`\b`)
		case r == '\t':
			buf.WriteString(`\t`)
		case r == '\n':
			buf.WriteString(`\n`)
		case r == '\f':
			buf.WriteString(`\f`)
		case r == '\r':
			buf.WriteString(`\r`)
		case r < 0x20:
			fmt.Fprintf(buf, `\u%04x`, r)
		default:
			buf.WriteRune(r)
		}
	}
	buf.WriteByte('"')
}

// formatECMAScriptNumber renders f with the ECMAScript Number-to-String
// rule that RFC 8785 section 3.2.2.3 requires.
func formatECMAScriptNumber(f float64) (string, error) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "", fmt.Errorf("canonical json: number is not finite: %w", ErrInvalidInput)
	}
	if f == 0 {
		return "0", nil
	}
	sign := ""
	if f < 0 {
		sign, f = "-", -f
	}
	// 'e' with precision -1 yields the shortest digits that round-trip.
	mant, expStr, _ := strings.Cut(strconv.FormatFloat(f, 'e', -1, 64), "e")
	digits := strings.Replace(mant, ".", "", 1)
	exp10, err := strconv.Atoi(expStr)
	if err != nil {
		return "", fmt.Errorf("canonical json: format number: %w", err)
	}
	k, n := len(digits), exp10+1
	switch {
	case k <= n && n <= 21:
		return sign + digits + strings.Repeat("0", n-k), nil
	case 0 < n && n <= 21:
		return sign + digits[:n] + "." + digits[n:], nil
	case -6 < n && n <= 0:
		return sign + "0." + strings.Repeat("0", -n) + digits, nil
	}
	e := n - 1
	esign := "+"
	if e < 0 {
		esign, e = "-", -e
	}
	if k == 1 {
		return sign + digits + "e" + esign + strconv.Itoa(e), nil
	}
	return sign + digits[:1] + "." + digits[1:] + "e" + esign + strconv.Itoa(e), nil
}

// sha256Hex returns the lowercase hex SHA-256 of b.
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
