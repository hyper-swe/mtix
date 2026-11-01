// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Fixed reasons a DSN cannot be parsed. Each wraps ErrDSNMalformed and
// quotes no part of the DSN: the parsers' own errors may quote it
// (FR-18.17, MTIX-95.15).
const (
	reasonScheme = "it must start with postgres:// or postgresql://, " +
		"or use the key=value form (host=... user=... password='...')"
	reasonURL = "percent-encode reserved characters in the user name and password, " +
		"or use the key=value form (host=... user=... password='...')"
	reasonKeyValue = "check that each setting is key=value, single-quote a password " +
		"that holds a space, and escape ' and \\ in it with a backslash"
)

// errNotKeyValue and errKeyValueSyntax tell parseKeyValue's two failure
// classes apart: text that is not a key=value DSN, and a key=value DSN
// with a quoting fault such as an unterminated quote.
var (
	errNotKeyValue    = errors.New("not key=value")
	errKeyValueSyntax = errors.New("key=value syntax")
)

// kvPair is one libpq key=value setting.
type kvPair struct{ key, val string }

// dsnSettings is a parsed hub DSN in either accepted form: the
// postgres:// URL form or the libpq key=value form. It reads and
// writes the few settings the TLS posture owns (sslmode, sslrootcert,
// password) and serializes back to the same form, so the key=value
// form carries any password byte for byte with no encoding. The
// driver still parses the serialized form once, in ApproveDSN.
type dsnSettings struct {
	u  *url.URL
	q  url.Values
	kv []kvPair
}

// get returns the value of setting key, empty when it is absent.
func (s *dsnSettings) get(key string) string {
	if s.u != nil {
		return s.q.Get(key)
	}
	val := ""
	for _, p := range s.kv {
		if p.key == key {
			val = p.val
		}
	}
	return val
}

// set gives setting key the value val, replacing any earlier value.
func (s *dsnSettings) set(key, val string) {
	if s.u != nil {
		s.q.Set(key, val)
		return
	}
	kept := make([]kvPair, 0, len(s.kv)+1)
	for _, p := range s.kv {
		if p.key != key {
			kept = append(kept, p)
		}
	}
	kept = append(kept, kvPair{key, val})
	s.kv = kept
}

// setPasswordIfAbsent gives the DSN the password pw unless it already
// names one, in its user info, query or key=value settings.
func (s *dsnSettings) setPasswordIfAbsent(pw string) {
	if pw == "" || s.get("password") != "" {
		return
	}
	if s.u == nil {
		s.set("password", pw)
		return
	}
	name := ""
	if s.u.User != nil {
		if existing, ok := s.u.User.Password(); ok && existing != "" {
			return
		}
		name = s.u.User.Username()
	}
	s.u.User = url.UserPassword(name, pw)
}

// String returns the DSN in the form it was parsed from.
func (s *dsnSettings) String() string {
	if s.u != nil {
		u := *s.u
		u.RawQuery = s.q.Encode()
		return u.String()
	}
	parts := make([]string, len(s.kv))
	for i, p := range s.kv {
		parts[i] = p.key + "=" + kvQuote(p.val)
	}
	return strings.Join(parts, " ")
}

// kvQuote writes val as a libpq single-quoted value.
func kvQuote(val string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(val) + "'"
}

// parseDSN parses the postgres:// or postgresql:// URL form or the
// libpq key=value form (FR-18.15). ApproveDSN is its only caller. Every
// failure is a fixed message wrapping ErrDSNMalformed that names the
// likely cause and quotes no part of the DSN; the parser's own error is
// discarded, since it may quote the DSN (FR-18.17, MTIX-95.15).
func parseDSN(dsn string) (*dsnSettings, error) {
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			// Deliberately not wrapped: err may quote the DSN.
			return nil, fmt.Errorf("%w: %s", ErrDSNMalformed, reasonURL)
		}
		return &dsnSettings{u: u, q: u.Query()}, nil
	}
	// Text that is a key=value DSN is judged as one even when a value holds
	// "://", as a password may (MTIX-87); only text that is not key=value
	// is refused as a foreign scheme.
	pairs, err := parseKeyValue(dsn)
	switch {
	case errors.Is(err, errKeyValueSyntax):
		return nil, fmt.Errorf("%w: %s", ErrDSNMalformed, reasonKeyValue)
	case err != nil:
		return nil, fmt.Errorf("%w: %s", ErrDSNMalformed, reasonScheme)
	}
	return &dsnSettings{kv: pairs}, nil
}

// parseKeyValue splits a libpq key=value connection string into its
// settings. A value is either bare, ended by white space, or
// single-quoted; a backslash escapes the next character in both. The
// driver parses the result again, so this split only has to find the
// settings the TLS posture owns without changing any value.
func parseKeyValue(dsn string) ([]kvPair, error) {
	var pairs []kvPair
	i := 0
	for {
		i = skipSpace(dsn, i)
		if i >= len(dsn) {
			break
		}
		start := i
		for i < len(dsn) && isKeyChar(dsn[i]) {
			i++
		}
		key := dsn[start:i]
		i = skipSpace(dsn, i)
		if key == "" || i >= len(dsn) || dsn[i] != '=' {
			return nil, errNotKeyValue
		}
		val, next, err := readValue(dsn, skipSpace(dsn, i+1))
		if err != nil {
			return nil, err
		}
		pairs = append(pairs, kvPair{key, val})
		i = next
	}
	if len(pairs) == 0 {
		return nil, errNotKeyValue
	}
	return pairs, nil
}

// readValue reads the value that starts at dsn[i] and returns it with
// the index after it.
func readValue(dsn string, i int) (string, int, error) {
	var b strings.Builder
	quoted := i < len(dsn) && dsn[i] == '\''
	if quoted {
		i++
	}
	for i < len(dsn) {
		c := dsn[i]
		switch {
		case c == '\\':
			if i+1 >= len(dsn) {
				return "", 0, errKeyValueSyntax
			}
			b.WriteByte(dsn[i+1])
			i += 2
		case quoted && c == '\'':
			return b.String(), i + 1, nil
		case !quoted && isSpace(c):
			return b.String(), i, nil
		default:
			b.WriteByte(c)
			i++
		}
	}
	if quoted {
		return "", 0, errKeyValueSyntax
	}
	return b.String(), i, nil
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f'
}

func isKeyChar(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func skipSpace(s string, i int) int {
	for i < len(s) && isSpace(s[i]) {
		i++
	}
	return i
}
