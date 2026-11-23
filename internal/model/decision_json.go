// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/big"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

const (
	// decisionMaxCanonicalBytes caps the RFC 8785 form of a decision. Every
	// replica computes the same canonical bytes, so every replica reaches the
	// same verdict whichever number spelling its transport kept.
	decisionMaxCanonicalBytes = 64 << 10
	// decisionMaxRawBytes only bounds the work of parsing. A JSONB round trip
	// can expand a number written in exponent form into its decimal digits
	// (about 44 times for a double near 1e308), so it sits above any raw form
	// of a canonical value within the cap. Transport budgets are enforced by
	// the transport.
	decisionMaxRawBytes = 4 << 20
)

func decisionInvalid(field string) error {
	return fmt.Errorf("decision %s: %w", field, ErrInvalidInput)
}

type decisionParser struct {
	decoder    *json.Decoder
	duplicates bool
	author     bool
}

// canonicalDecision replaces duplicate members before serializing the I-JSON value.
func canonicalDecision(raw []byte, author bool) ([]byte, bool, error) {
	if len(raw) > decisionMaxRawBytes || !utf8.Valid(raw) || !pairedSurrogates(raw) {
		return nil, false, decisionInvalid("size or Unicode")
	}
	p := decisionParser{decoder: json.NewDecoder(bytes.NewReader(raw)), author: author}
	if author {
		// Keep number tokens as written so the author path can refuse any
		// that a double would round (I-JSON, RFC 7493 section 2.2).
		p.decoder.UseNumber()
	}
	value, err := p.value(0)
	if err != nil {
		return nil, false, fmt.Errorf("parse decision: %w", err)
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, false, decisionInvalid("object required")
	}
	if _, err = p.decoder.Token(); err != io.EOF {
		return nil, false, decisionInvalid("trailing input")
	}
	var out bytes.Buffer
	writeDecisionJSON(&out, value)
	if out.Len() > decisionMaxCanonicalBytes {
		return nil, false, decisionInvalid("canonical size")
	}
	return out.Bytes(), p.duplicates, nil
}

// encoding/json replaces lone surrogate escapes with U+FFFD. I-JSON rejects them.
func pairedSurrogates(raw []byte) bool {
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			continue
		}
		i++
		if i >= len(raw) || raw[i] != 'u' {
			continue
		}
		n, ok := hexUnit(raw[i+1:])
		if !ok {
			return false
		}
		i += 4
		if n < 0xd800 || n > 0xdfff {
			continue
		}
		if n > 0xdbff || !bytes.HasPrefix(raw[i+1:], []byte(`\u`)) {
			return false
		}
		low, ok := hexUnit(raw[i+3:])
		if !ok || low < 0xdc00 || low > 0xdfff {
			return false
		}
		i += 6
	}
	return true
}

// hasNoncharacter reports a Unicode noncharacter (U+FDD0..U+FDEF and the last
// two code points of each plane), which I-JSON (RFC 7493 section 2.1) forbids
// in names and values, whether written literally or as an escape.
func hasNoncharacter(s string) bool {
	for _, r := range s {
		if (r >= 0xfdd0 && r <= 0xfdef) || r&0xfffe == 0xfffe {
			return true
		}
	}
	return false
}

func hexUnit(b []byte) (uint64, bool) {
	if len(b) < 4 {
		return 0, false
	}
	n, err := strconv.ParseUint(string(b[:4]), 16, 16)
	return n, err == nil
}

func (p *decisionParser) value(depth int) (any, error) {
	t, err := p.decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("%w: JSON token: %w", ErrInvalidInput, err)
	}
	if n, isNumber := t.(json.Number); isNumber {
		return authorNumber(n)
	}
	d, ok := t.(json.Delim)
	if !ok {
		if str, isString := t.(string); isString && hasNoncharacter(str) {
			return nil, decisionInvalid("noncharacter")
		}
		return t, nil
	}
	if depth >= 10 {
		return nil, decisionInvalid("nesting depth")
	}
	object := map[string]any{}
	array := []any{}
	for p.decoder.More() {
		var key string
		if d == '{' {
			var err error
			key, err = p.memberName(object)
			if err != nil {
				return nil, fmt.Errorf("decision object: %w", err)
			}
		}
		v, err := p.value(depth + 1)
		if err != nil {
			return nil, fmt.Errorf("nested value: %w", err)
		}
		if d == '{' {
			object[key] = v
		} else {
			array = append(array, v)
		}
	}
	if _, err := p.decoder.Token(); err != nil {
		return nil, fmt.Errorf("%w: close container: %w", ErrInvalidInput, err)
	}
	if d == '{' {
		return object, nil
	}
	return array, nil
}

// authorNumber converts a number token read on the author path to the double
// the canonical form carries. It refuses a token whose decimal value differs
// from the shortest decimal spelling of that double (excess precision, an
// integer above 2^53, overflow or underflow): authoring never rounds
// (ADR-008 section 3.2, MTIX-219.4).
func authorNumber(n json.Number) (float64, error) {
	f, err := strconv.ParseFloat(string(n), 64)
	if err != nil || math.IsInf(f, 0) || len(n) > 400 || hugeExponent(string(n)) {
		return 0, decisionInvalid("number range")
	}
	exact, ok := new(big.Rat).SetString(string(n))
	shortest, ok2 := new(big.Rat).SetString(strconv.FormatFloat(f, 'e', -1, 64))
	if !ok || !ok2 || exact.Cmp(shortest) != 0 {
		return 0, decisionInvalid("number precision")
	}
	return f, nil
}

// hugeExponent reports an exponent too large to expand safely into a decimal.
func hugeExponent(tok string) bool {
	i := strings.IndexAny(tok, "eE")
	if i < 0 {
		return false
	}
	e, err := strconv.Atoi(tok[i+1:])
	return err != nil || e > 400 || e < -400
}

func (p *decisionParser) memberName(object map[string]any) (string, error) {
	t, err := p.decoder.Token()
	if err != nil {
		return "", fmt.Errorf("%w: member: %w", ErrInvalidInput, err)
	}
	key, ok := t.(string)
	if !ok {
		return "", decisionInvalid("member name")
	}
	if hasNoncharacter(key) {
		return "", decisionInvalid("noncharacter")
	}
	if _, exists := object[key]; exists {
		p.duplicates = true
		if p.author {
			return "", decisionInvalid("duplicate member")
		}
	}
	return key, nil
}

// RFC 8785 orders object names as UTF-16 code units. Arrays retain wire order.
func writeDecisionJSON(out *bytes.Buffer, value any) {
	switch v := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		slices.SortFunc(keys, func(a, b string) int { return slices.Compare(utf16.Encode([]rune(a)), utf16.Encode([]rune(b))) })
		out.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				out.WriteByte(',')
			}
			writeDecisionString(out, k)
			out.WriteByte(':')
			writeDecisionJSON(out, v[k])
		}
		out.WriteByte('}')
	case []any:
		out.WriteByte('[')
		for i, x := range v {
			if i > 0 {
				out.WriteByte(',')
			}
			writeDecisionJSON(out, x)
		}
		out.WriteByte(']')
	case string:
		writeDecisionString(out, v)
	case float64:
		out.WriteString(decisionNumber(v))
	case bool:
		out.WriteString(strconv.FormatBool(v))
	case nil:
		out.WriteString("null")
	}
}

func decisionNumber(v float64) string {
	if v == 0 {
		return "0"
	}
	abs := v
	if abs < 0 {
		abs = -abs
	}
	if abs >= 1e-6 && abs < 1e21 {
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return strings.Replace(strconv.FormatFloat(v, 'e', -1, 64), "e-0", "e-", 1)
}

func writeDecisionString(out *bytes.Buffer, s string) {
	const hex = "0123456789abcdef"
	out.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '\\':
			out.WriteByte('\\')
			out.WriteRune(r)
		case '\b':
			out.WriteString(`\b`)
		case '\t':
			out.WriteString(`\t`)
		case '\n':
			out.WriteString(`\n`)
		case '\f':
			out.WriteString(`\f`)
		case '\r':
			out.WriteString(`\r`)
		default:
			if r < 32 {
				out.WriteString(`\u00`)
				out.WriteByte(hex[r>>4])
				out.WriteByte(hex[r&15])
			} else {
				out.WriteRune(r)
			}
		}
	}
	out.WriteByte('"')
}
