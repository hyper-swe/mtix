// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Sentinel errors for the session registry contract check.
var (
	// ErrMalformedRegistry marks a registry sample that is not a JSON object.
	ErrMalformedRegistry = errors.New("malformed registry sample")
	// ErrRegistryContract marks a sample set that breaks the pinned
	// contract the idle/busy signal relies on (S5, MTIX-112.40.1).
	ErrRegistryContract = errors.New("registry contract broken")
)

// requiredKeys returns the registry keys the listener relies on, read from
// the session registry record of the scratch session's own pid.
func requiredKeys() []string {
	return []string{"pid", "sessionId", "cwd", "messagingSocketPath"}
}

// Record is one sample of a session registry file. Present records which
// keys the sample carried, because absence is part of the contract.
type Record struct {
	Present    map[string]bool
	PID        int
	SessionID  string
	Cwd        string
	SocketPath string
	Status     string
	UpdatedAt  int64
}

// ParseRegistry reads one JSON object per line (blank lines skipped),
// each a sample of a registry file. A line that is not an object, or whose
// known keys have the wrong type, is ErrMalformedRegistry naming the line.
func ParseRegistry(r io.Reader) ([]Record, error) {
	var out []Record
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		rec, err := parseRecord(line)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", n, err)
		}
		out = append(out, rec)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read registry samples: %w", err)
	}
	return out, nil
}

func parseRecord(line string) (Record, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return Record{}, fmt.Errorf("%w: %w", ErrMalformedRegistry, err)
	}
	rec := Record{Present: map[string]bool{}}
	for k := range raw {
		rec.Present[k] = true
	}
	fields := []struct {
		key string
		dst any
	}{
		{"pid", &rec.PID}, {"sessionId", &rec.SessionID}, {"cwd", &rec.Cwd},
		{"messagingSocketPath", &rec.SocketPath}, {"status", &rec.Status},
		{"statusUpdatedAt", &rec.UpdatedAt},
	}
	for _, f := range fields {
		v, ok := raw[f.key]
		if !ok {
			continue
		}
		if err := json.Unmarshal(v, f.dst); err != nil {
			return Record{}, fmt.Errorf("%w: key %s: %w", ErrMalformedRegistry, f.key, err)
		}
	}
	return rec, nil
}

// CheckRegistry asserts the registry contract over ordered samples of one
// session: every required key present, pid equal to the driver's own child,
// status only idle or busy (absent only before the first status), a
// non-decreasing statusUpdatedAt, no repeat of a status under a newer
// timestamp, and both idle and busy observed. All violations are
// returned together in one ErrRegistryContract (S5, MTIX-112.40.1).
func CheckRegistry(recs []Record, childPID int) error {
	if len(recs) == 0 {
		return fmt.Errorf("%w: no samples", ErrRegistryContract)
	}
	var v []string
	for i, rec := range recs {
		v = append(v, keyViolations(i, rec, childPID)...)
	}
	v = append(v, statusViolations(recs)...)
	if len(v) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrRegistryContract, strings.Join(v, "; "))
}

func keyViolations(i int, rec Record, childPID int) []string {
	var v []string
	for _, k := range requiredKeys() {
		if !rec.Present[k] {
			v = append(v, fmt.Sprintf("sample %d: required key %s missing", i+1, k))
		}
	}
	if rec.Present["pid"] && rec.PID != childPID {
		v = append(v, fmt.Sprintf("sample %d: pid %d is not the child pid %d", i+1, rec.PID, childPID))
	}
	return v
}

func statusViolations(recs []Record) []string {
	var v []string
	var prev *Record
	sawIdle, sawBusy := false, false
	for i := range recs {
		rec := &recs[i]
		if !rec.Present["status"] {
			if prev != nil {
				v = append(v, fmt.Sprintf("sample %d: status absent after the first status", i+1))
			}
			continue
		}
		sawIdle = sawIdle || rec.Status == "idle"
		sawBusy = sawBusy || rec.Status == "busy"
		v = append(v, transitionViolations(i, prev, rec)...)
		prev = rec
	}
	if !sawIdle {
		v = append(v, "idle status never observed")
	}
	if !sawBusy {
		v = append(v, "busy status never observed")
	}
	return v
}

func transitionViolations(i int, prev, rec *Record) []string {
	var v []string
	if rec.Status != "idle" && rec.Status != "busy" {
		v = append(v, fmt.Sprintf("sample %d: status %q is neither idle nor busy", i+1, rec.Status))
	}
	if !rec.Present["statusUpdatedAt"] {
		return append(v, fmt.Sprintf("sample %d: statusUpdatedAt missing beside status", i+1))
	}
	if prev == nil {
		return v
	}
	switch {
	case rec.UpdatedAt < prev.UpdatedAt:
		v = append(v, fmt.Sprintf("sample %d: statusUpdatedAt went backwards", i+1))
	case rec.UpdatedAt > prev.UpdatedAt && rec.Status == prev.Status:
		v = append(v, fmt.Sprintf("sample %d: no idle/busy alternation: %s repeated under a newer statusUpdatedAt", i+1, rec.Status))
	case rec.UpdatedAt == prev.UpdatedAt && rec.Status != prev.Status:
		v = append(v, fmt.Sprintf("sample %d: status changed without a statusUpdatedAt change", i+1))
	}
	return v
}
