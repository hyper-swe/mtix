// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"
	"math"
	"strconv"
	"unicode/utf8"
)

// jsonParser is a strict, I-JSON aware JSON parser. Unlike encoding/json it
// reports duplicate member names (the later member replaces the earlier one
// at every nesting level), rejects lone surrogates and invalid UTF-8, and
// rejects numbers that are not finite IEEE doubles.
type jsonParser struct {
	b        []byte
	pos      int
	depth    int
	maxDepth int
	dup      bool

	// spanKey names a member of the outermost object whose raw byte range
	// is recorded; the last occurrence wins.
	spanKey   string
	span      [2]int
	spanFound bool
}

// parseJSON parses raw into a generic value made of map[string]any, []any,
// string, float64, bool and nil. It reports whether any object repeated a
// member name. Nesting deeper than maxDepth is an error.
func parseJSON(raw []byte, maxDepth int) (value any, dup bool, err error) {
	p := &jsonParser{b: raw, maxDepth: maxDepth}
	v, err := p.parseDocument()
	if err != nil {
		return nil, false, err
	}
	return v, p.dup, nil
}

// parseJSONSpan is parseJSON that also returns the raw bytes of the named
// member of the outermost object (the last occurrence when repeated).
func parseJSONSpan(raw []byte, maxDepth int, key string) (value any, span []byte, found bool, err error) {
	p := &jsonParser{b: raw, maxDepth: maxDepth, spanKey: key}
	v, err := p.parseDocument()
	if err != nil {
		return nil, nil, false, err
	}
	if p.spanFound {
		return v, raw[p.span[0]:p.span[1]], true, nil
	}
	return v, nil, false, nil
}

func (p *jsonParser) parseDocument() (any, error) {
	p.skipSpace()
	v, err := p.parseValue()
	if err != nil {
		return nil, err
	}
	p.skipSpace()
	if p.pos != len(p.b) {
		return nil, p.errf("unexpected data after the JSON value")
	}
	return v, nil
}

func (p *jsonParser) errf(format string, args ...any) error {
	return fmt.Errorf("json at byte %d: %s: %w", p.pos, fmt.Sprintf(format, args...), ErrInvalidInput)
}

func (p *jsonParser) skipSpace() {
	for p.pos < len(p.b) {
		switch p.b[p.pos] {
		case ' ', '\t', '\n', '\r':
			p.pos++
		default:
			return
		}
	}
}

func (p *jsonParser) parseValue() (any, error) {
	if p.pos >= len(p.b) {
		return nil, p.errf("unexpected end of input")
	}
	switch c := p.b[p.pos]; {
	case c == '{':
		return p.parseObject()
	case c == '[':
		return p.parseArray()
	case c == '"':
		return p.parseString()
	case c == '-' || (c >= '0' && c <= '9'):
		return p.parseNumber()
	}
	return p.parseLiteral()
}

func (p *jsonParser) parseLiteral() (any, error) {
	for lit, val := range map[string]any{"true": true, "false": false, "null": nil} {
		if len(p.b)-p.pos >= len(lit) && string(p.b[p.pos:p.pos+len(lit)]) == lit {
			p.pos += len(lit)
			return val, nil
		}
	}
	return nil, p.errf("invalid literal")
}

func (p *jsonParser) enter() error {
	p.depth++
	if p.depth > p.maxDepth {
		return p.errf("nesting deeper than %d", p.maxDepth)
	}
	return nil
}

func (p *jsonParser) parseArray() (any, error) {
	if err := p.enter(); err != nil {
		return nil, err
	}
	p.pos++ // [
	out := []any{}
	p.skipSpace()
	if p.pos < len(p.b) && p.b[p.pos] == ']' {
		p.pos++
		p.depth--
		return out, nil
	}
	for {
		p.skipSpace()
		v, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		out = append(out, v)
		p.skipSpace()
		if p.pos >= len(p.b) {
			return nil, p.errf("unterminated array")
		}
		c := p.b[p.pos]
		p.pos++
		if c == ']' {
			p.depth--
			return out, nil
		}
		if c != ',' {
			return nil, p.errf("expected , or ] in array")
		}
	}
}

func (p *jsonParser) parseObject() (any, error) {
	if err := p.enter(); err != nil {
		return nil, err
	}
	top := p.depth == 1
	p.pos++ // {
	out := map[string]any{}
	p.skipSpace()
	if p.pos < len(p.b) && p.b[p.pos] == '}' {
		p.pos++
		p.depth--
		return out, nil
	}
	for {
		if err := p.parseMember(out, top); err != nil {
			return nil, err
		}
		p.skipSpace()
		if p.pos >= len(p.b) {
			return nil, p.errf("unterminated object")
		}
		c := p.b[p.pos]
		p.pos++
		if c == '}' {
			p.depth--
			return out, nil
		}
		if c != ',' {
			return nil, p.errf("expected , or } in object")
		}
	}
}

// parseMember reads one "name": value pair into out; a repeated name
// replaces the earlier member.
func (p *jsonParser) parseMember(out map[string]any, top bool) error {
	p.skipSpace()
	if p.pos >= len(p.b) || p.b[p.pos] != '"' {
		return p.errf("expected a member name")
	}
	name, err := p.parseString()
	if err != nil {
		return err
	}
	p.skipSpace()
	if p.pos >= len(p.b) || p.b[p.pos] != ':' {
		return p.errf("expected : after a member name")
	}
	p.pos++
	p.skipSpace()
	start := p.pos
	v, err := p.parseValue()
	if err != nil {
		return err
	}
	if _, seen := out[name]; seen {
		p.dup = true
	}
	out[name] = v
	if top && p.spanKey != "" && name == p.spanKey {
		p.span, p.spanFound = [2]int{start, p.pos}, true
	}
	return nil
}

func (p *jsonParser) parseNumber() (any, error) {
	start := p.pos
	if err := p.scanNumber(); err != nil {
		return nil, err
	}
	f, err := strconv.ParseFloat(string(p.b[start:p.pos]), 64)
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
		return nil, p.errf("number is not a finite IEEE double")
	}
	return f, nil
}

// scanNumber advances over one JSON number token.
func (p *jsonParser) scanNumber() error {
	if p.b[p.pos] == '-' {
		p.pos++
	}
	if p.pos >= len(p.b) {
		return p.errf("invalid number")
	}
	switch {
	case p.b[p.pos] == '0':
		p.pos++
	case p.b[p.pos] >= '1' && p.b[p.pos] <= '9':
		p.digits()
	default:
		return p.errf("invalid number")
	}
	if p.peek() == '.' {
		p.pos++
		if !p.digits() {
			return p.errf("invalid number fraction")
		}
	}
	if c := p.peek(); c == 'e' || c == 'E' {
		p.pos++
		if sign := p.peek(); sign == '+' || sign == '-' {
			p.pos++
		}
		if !p.digits() {
			return p.errf("invalid number exponent")
		}
	}
	return nil
}

// peek returns the next byte, or 0 at the end of input.
func (p *jsonParser) peek() byte {
	if p.pos < len(p.b) {
		return p.b[p.pos]
	}
	return 0
}

func (p *jsonParser) digits() bool {
	n := 0
	for p.pos < len(p.b) && p.b[p.pos] >= '0' && p.b[p.pos] <= '9' {
		p.pos++
		n++
	}
	return n > 0
}

func (p *jsonParser) parseString() (string, error) {
	p.pos++ // opening quote
	var out []byte
	for p.pos < len(p.b) {
		c := p.b[p.pos]
		switch {
		case c == '"':
			p.pos++
			return string(out), nil
		case c < 0x20:
			return "", p.errf("control character in string")
		case c == '\\':
			r, err := p.parseEscape()
			if err != nil {
				return "", err
			}
			out = utf8.AppendRune(out, r)
		case c < utf8.RuneSelf:
			out = append(out, c)
			p.pos++
		default:
			r, size := utf8.DecodeRune(p.b[p.pos:])
			if r == utf8.RuneError && size <= 1 {
				return "", p.errf("invalid UTF-8")
			}
			out = append(out, p.b[p.pos:p.pos+size]...)
			p.pos += size
		}
	}
	return "", p.errf("unterminated string")
}

func (p *jsonParser) parseEscape() (rune, error) {
	p.pos++ // backslash
	if p.pos >= len(p.b) {
		return 0, p.errf("unterminated escape")
	}
	c := p.b[p.pos]
	p.pos++
	switch c {
	case '"', '\\', '/':
		return rune(c), nil
	case 'b':
		return '\b', nil
	case 'f':
		return '\f', nil
	case 'n':
		return '\n', nil
	case 'r':
		return '\r', nil
	case 't':
		return '\t', nil
	case 'u':
		return p.parseUnicodeEscape()
	}
	return 0, p.errf("invalid escape")
}

func (p *jsonParser) hex4() (rune, bool) {
	if p.pos+4 > len(p.b) {
		return 0, false
	}
	n, err := strconv.ParseUint(string(p.b[p.pos:p.pos+4]), 16, 16)
	if err != nil {
		return 0, false
	}
	p.pos += 4
	return rune(int32(n)), true
}

func (p *jsonParser) parseUnicodeEscape() (rune, error) {
	r, ok := p.hex4()
	if !ok {
		return 0, p.errf("invalid \\u escape")
	}
	switch {
	case r >= 0xDC00 && r <= 0xDFFF:
		return 0, p.errf("lone low surrogate")
	case r >= 0xD800 && r <= 0xDBFF:
		if p.pos+2 > len(p.b) || p.b[p.pos] != '\\' || p.b[p.pos+1] != 'u' {
			return 0, p.errf("lone high surrogate")
		}
		p.pos += 2
		lo, ok := p.hex4()
		if !ok || lo < 0xDC00 || lo > 0xDFFF {
			return 0, p.errf("lone high surrogate")
		}
		return 0x10000 + (r-0xD800)<<10 + (lo - 0xDC00), nil
	}
	return r, nil
}
