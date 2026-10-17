// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package redact

import (
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// knownMask replaces a known secret, matching the marker DSN uses.
const knownMask = "REDACTED"

// passwordValues returns the value of every password setting in dsn as
// written, read the way the parser reads it (MTIX-87): in a URL query
// (?password=v or &password=v) a value ends at '&' or white space; in the
// key=value form it is single-quoted, or bare and ended by ASCII white
// space, a backslash escaping the next character in both. So a bare
// password holding an ampersand or an escaped space is one value.
func passwordValues(dsn string) []string {
	var out []string
	for _, m := range passwordKey.FindAllStringSubmatchIndex(dsn, -1) {
		start := m[3]
		inQuery := m[0] > 0 && (dsn[m[0]-1] == '?' || dsn[m[0]-1] == '&')
		out = append(out, dsn[start:passwordValueEnd(dsn, start, inQuery)])
	}
	return out
}

// unescapeValue returns v without the backslashes that escape the
// character after them.
func unescapeValue(v string) string {
	if !strings.Contains(v, `\`) {
		return v
	}
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		if v[i] == '\\' && i+1 < len(v) {
			i++
		}
		b.WriteByte(v[i])
	}
	return b.String()
}

// minBarePassword is the length from which Known removes a password
// wherever it appears. A shorter password is removed only where it
// stands as a password, so ordinary words in the output stay readable
// (MTIX-95.15).
const minBarePassword = 6

// Known returns s with every trace of the known DSNs removed, then with
// every remaining DSN-shaped substring masked by DSN (FR-18.17,
// MTIX-95.15).
//
// For each known DSN it removes the exact value (surrounding whitespace
// trimmed) everywhere, and every password the DSN may carry: wherever it
// appears when it has at least minBarePassword characters, otherwise
// only where it stands as a password (see maskAsPassword). The
// passwords are found without parsing the DSN, so a DSN that does not
// parse (no scheme, invalid escapes, unescaped reserved characters) is
// covered too; see knownPasswords. Longer values are removed first, so
// a password is never left partly visible behind a shorter candidate.
// Every occurrence is removed. Blank known values are ignored.
func Known(s string, dsns ...string) string {
	bare, short := knownSecrets(dsns)
	for _, secret := range bare {
		s = strings.ReplaceAll(s, secret, knownMask)
	}
	for _, pw := range short {
		s = maskAsPassword(s, pw)
	}
	return DSN(s)
}

// knownSecrets returns the distinct non-empty values Known removes for
// dsns (MTIX-95.15). bare holds each DSN (surrounding whitespace
// trimmed, as a secrets file is read) and every password form of at
// least minBarePassword characters, longest first; short holds the
// shorter password forms, whose masks (see maskAsPassword) are anchored
// on both sides, so their order does not matter.
func knownSecrets(dsns []string) (bare, short []string) {
	seen := map[string]bool{}
	for _, dsn := range dsns {
		trimmed := strings.TrimSpace(dsn)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		bare = append(bare, trimmed)
		for _, pw := range knownPasswords(trimmed) {
			for _, form := range passwordForms(pw) {
				if form == "" || seen[form] {
					continue
				}
				seen[form] = true
				if len(form) >= minBarePassword {
					bare = append(bare, form)
				} else {
					short = append(short, form)
				}
			}
		}
	}
	longestFirst(bare)
	return bare, short
}

// longestFirst sorts values by descending length, keeping the order of
// equal lengths.
func longestFirst(values []string) {
	sort.SliceStable(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
}

// maskAsPassword returns s with pw removed only where it stands as a
// password (MTIX-95.15): in user info (":pw@") and as the whole value of
// a password setting (password=pw or password = 'pw', ended by '&',
// whitespace or the end of s).
func maskAsPassword(s, pw string) string {
	s = strings.ReplaceAll(s, ":"+pw+"@", ":"+knownMask+"@")
	quoted := regexp.QuoteMeta(pw)
	setting := regexp.MustCompile(`(?i)(password\s*=\s*)(?:'` + quoted + `'|` + quoted + `)([&\s]|$)`)
	return setting.ReplaceAllString(s, "${1}"+knownMask+"${2}")
}

// knownPasswords returns every password candidate in dsn without
// parsing it (MTIX-95.15):
//
//   - the user-info password before the first '@' and before the last
//     '@' after the scheme (or from the start when there is no scheme),
//     so a password holding an unescaped '@' or '/' and a query holding
//     an '@' are both covered;
//   - every password= setting, in URL-query or keyword/value form, as
//     written and, when single-quoted, unquoted.
func knownPasswords(dsn string) []string {
	var out []string
	rest := dsn
	if i := strings.Index(rest, "://"); i >= 0 {
		rest = rest[i+len("://"):]
	}
	for _, at := range []int{strings.Index(rest, "@"), strings.LastIndex(rest, "@")} {
		if at < 0 {
			continue
		}
		// User info without ':' yields an empty password, which
		// knownSecrets drops: a user name alone is not a secret.
		_, pw, _ := strings.Cut(rest[:at], ":")
		out = append(out, pw)
	}
	for _, v := range passwordValues(dsn) {
		out = append(out, v)
		inner := v
		if len(v) >= 2 && strings.HasPrefix(v, "'") && strings.HasSuffix(v, "'") {
			inner = v[1 : len(v)-1]
		}
		if inner != v || strings.Contains(v, `\`) {
			out = append(out, unescapeValue(inner))
		}
	}
	return out
}

// passwordForms returns pw as written plus the forms it can take in
// output (MTIX-95.15): percent-decoded (path and query rules), and each
// decoded value re-encoded the way a URL's user info and query encode
// it, since a DSN rebuilt from its parsed form prints those encodings.
func passwordForms(pw string) []string {
	decoded := []string{pw}
	if d, err := url.PathUnescape(pw); err == nil {
		decoded = append(decoded, d)
	}
	if d, err := url.QueryUnescape(pw); err == nil {
		decoded = append(decoded, d)
	}
	out := append([]string{}, decoded...)
	for _, d := range decoded {
		out = append(out,
			strings.TrimPrefix(url.UserPassword("", d).String(), ":"),
			url.QueryEscape(d))
	}
	return out
}
