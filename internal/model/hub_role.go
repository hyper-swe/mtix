// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// hubRoleNamePattern is the only role-name form mtix names in hub
// statements (SQL Rule 1a, MTIX-95.1): lower-case ASCII letters, digits and
// underscores, starting with a letter or underscore, at most 63 bytes.
var hubRoleNamePattern = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

// ValidHubRoleName reports whether name has the identifier form mtix
// accepts for hub role, schema and object names (SQL Rule 1a, MTIX-95.1).
// A name outside it is never placed in a statement mtix runs.
func ValidHubRoleName(name string) bool {
	return hubRoleNamePattern.MatchString(name)
}

// DataAPIRoles returns the role names an HTTP data-API layer uses for
// anonymous and signed-in callers (ADR-006 D26, MTIX-95.1). These roles can
// never be kept by `mtix sync harden`.
func DataAPIRoles() []string {
	return []string{"anon", "authenticated"}
}

// ParseKeepRoles parses the comma-separated role list of the
// sync.keep_roles key (MTIX-95.1). Blank input means no kept roles. Each
// name is trimmed and checked by ValidateKeepRoles; the result is sorted
// and de-duplicated.
//
// Returns ErrInvalidInput for an empty entry, an invalid name, or a role
// that cannot be kept.
func ParseKeepRoles(value string) ([]string, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	parts := strings.Split(value, ",")
	names := make([]string, 0, len(parts))
	for _, p := range parts {
		name := strings.TrimSpace(p)
		if name == "" {
			return nil, fmt.Errorf("kept roles %q: empty role name: %w", value, ErrInvalidInput)
		}
		names = append(names, name)
	}
	return ValidateKeepRoles(names)
}

// ValidateKeepRoles checks a list of roles to keep, as given by repeated
// --keep-role flags (MTIX-95.1), and returns it sorted and de-duplicated.
// A kept role must be a valid role name, and must not be PUBLIC, a
// data-API role or a predefined pg_ role: keeping one of those would keep
// access for every caller the hardening exists to shut out.
//
// Returns ErrInvalidInput naming the first refused entry.
func ValidateKeepRoles(names []string) ([]string, error) {
	set := map[string]bool{}
	for _, name := range names {
		if err := checkKeepRole(name); err != nil {
			return nil, err
		}
		set[name] = true
	}
	if len(set) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

// checkKeepRole refuses one role name that cannot be kept.
func checkKeepRole(name string) error {
	lower := strings.ToLower(name)
	if lower == "public" || strings.HasPrefix(lower, "pg_") {
		return fmt.Errorf("role %q cannot be kept: %w", name, ErrInvalidInput)
	}
	for _, api := range DataAPIRoles() {
		if lower == api {
			return fmt.Errorf("role %q cannot be kept: it serves data-API callers: %w", name, ErrInvalidInput)
		}
	}
	if !ValidHubRoleName(name) {
		return fmt.Errorf("kept role %q is not a valid role name (lower-case letters, digits and _): %w",
			name, ErrInvalidInput)
	}
	return nil
}
