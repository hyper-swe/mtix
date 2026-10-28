// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/hyper-swe/mtix/internal/model"
)

// Quarantine handle sizes (MTIX-187.7.6). A handle is a prefix of the
// lower-case hex SHA-256 of the exact event id bytes. It is always typeable
// and printable, even for ids with NUL, invalid UTF-8 or shell metacharacters.
const (
	// QuarantineHandleLen is the number of hex characters printed.
	QuarantineHandleLen = 12
	// QuarantineHandleMin is the shortest prefix accepted.
	QuarantineHandleMin = 8
	quarantineHandleMax = 64
)

// QuarantineHandleFull returns the full lower-case hex SHA-256 of the exact
// event id bytes (MTIX-187.7.6).
func QuarantineHandleFull(eventID string) string {
	sum := sha256.Sum256([]byte(eventID))
	return hex.EncodeToString(sum[:])
}

// QuarantineHandle returns the printed handle of an event id (MTIX-187.7.6).
func QuarantineHandle(eventID string) string {
	return QuarantineHandleFull(eventID)[:QuarantineHandleLen]
}

// QuarantineHandles returns, for each id in ids, the shortest prefix of at
// least QuarantineHandleLen hex characters of its digest that no other id in
// the set shares, so a printed handle always resolves to exactly one event
// (MTIX-187.7.6).
func QuarantineHandles(ids []string) []string { return quarantineHandles(ids, QuarantineHandleLen) }

// quarantineHandles is QuarantineHandles with a settable base length, so tests
// can force collisions that a 12-character base makes impractical to find.
func quarantineHandles(ids []string, base int) []string {
	fulls := make([]string, len(ids))
	groups := map[string][]int{}
	for i, id := range ids {
		fulls[i] = QuarantineHandleFull(id)
		key := fulls[i][:base]
		groups[key] = append(groups[key], i)
	}
	out := make([]string, len(ids))
	for i := range ids {
		n := base
		for n < quarantineHandleMax && sharesPrefix(fulls, groups[fulls[i][:base]], i, n) {
			n++
		}
		out[i] = fulls[i][:n]
	}
	return out
}

func sharesPrefix(fulls []string, group []int, self, n int) bool {
	for _, j := range group {
		if j != self && fulls[j][:n] == fulls[self][:n] {
			return true
		}
	}
	return false
}

// QuarantineHandleAmong returns the printed handle of id within the set of all
// retained event ids (id need not be listed in ids).
func QuarantineHandleAmong(ids []string, id string) string {
	return quarantineHandleAmong(ids, id, QuarantineHandleLen)
}

func quarantineHandleAmong(ids []string, id string, base int) string {
	all := ids
	found := false
	for _, other := range ids {
		if other == id {
			found = true
			break
		}
	}
	if !found {
		all = append(append([]string{}, ids...), id)
	}
	handles := quarantineHandles(all, base)
	for i, other := range all {
		if other == id {
			return handles[i]
		}
	}
	return QuarantineHandleFull(id)[:base]
}

// MatchQuarantineHandle resolves a handle prefix to exactly one of ids. Surrounding
// space and letter case are ignored. A prefix shorter than QuarantineHandleMin
// or that is not hex returns ErrInvalidInput; no match returns ErrNotFound; more
// than one match is refused with the length that would separate them
// (MTIX-187.7.6).
func MatchQuarantineHandle(ids []string, handle string) (string, error) {
	prefix := strings.ToLower(strings.TrimSpace(handle))
	if len(prefix) < QuarantineHandleMin || len(prefix) > quarantineHandleMax || !isLowerHex(prefix) {
		return "", fmt.Errorf("handle must be %d to %d hex characters (see mtix sync quarantine list): %w",
			QuarantineHandleMin, quarantineHandleMax, model.ErrInvalidInput)
	}
	var matches []string
	for _, id := range ids {
		if strings.HasPrefix(QuarantineHandleFull(id), prefix) {
			matches = append(matches, id)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no quarantined event has handle %s: %w", prefix, model.ErrNotFound)
	case 1:
		return matches[0], nil
	}
	return "", fmt.Errorf("handle %s is ambiguous (%d events); use a longer handle, at least %d hex characters: %w",
		prefix, len(matches), separatingLength(matches, len(prefix)), model.ErrInvalidInput)
}

// separatingLength returns the shortest prefix length above have that tells
// every match apart, or the full digest length when none does.
func separatingLength(matches []string, have int) int {
	for n := have + 1; n < quarantineHandleMax; n++ {
		seen := map[string]bool{}
		distinct := true
		for _, id := range matches {
			key := QuarantineHandleFull(id)[:n]
			if seen[key] {
				distinct = false
				break
			}
			seen[key] = true
		}
		if distinct {
			return n
		}
	}
	return quarantineHandleMax
}

func isLowerHex(s string) bool {
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// ResolveQuarantineHandle maps a handle to the event id of one retained
// quarantined event, for `mtix sync quarantine acknowledge --handle`
// (MTIX-187.7.6). It only reads.
func (s *SyncService) ResolveQuarantineHandle(ctx context.Context, handle string) (string, error) {
	events, err := s.ListQuarantined(ctx)
	if err != nil {
		return "", fmt.Errorf("resolve quarantine handle: %w", err)
	}
	ids := make([]string, 0, len(events))
	for _, q := range events {
		ids = append(ids, q.EventID)
	}
	return MatchQuarantineHandle(ids, handle)
}
