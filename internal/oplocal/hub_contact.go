// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package oplocal

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ActivityKind names a signal that restarts a board's hub-contact window
// (MTIX-112.20.1, ADR-006 D18). A parked wait, a wait-loop heartbeat and a
// live registration are deliberately not kinds: they must never extend it.
type ActivityKind string

// Activity kinds recorded by CLI and MCP commands.
const (
	// ActivityMutation is a local write to the board.
	ActivityMutation ActivityKind = "mutation"
	// ActivityAddressedSend is a comment addressed to another agent.
	ActivityAddressedSend ActivityKind = "addressed_send"
	// ActivityGlue is an R1 glue signal.
	ActivityGlue ActivityKind = "glue"
	// ActivityTurn is a heartbeat run by a model turn or a person.
	ActivityTurn ActivityKind = "turn"
)

// HubActivity is the last activity on one board, kept operator-locally so it
// never syncs and never touches the board or tasks.json.
type HubActivity struct {
	At   time.Time    `json:"at"`
	Kind ActivityKind `json:"kind"`
}

// ListenLease is one armed listener (a hook waiter) on a board. The pid plus
// its start time identify one process; liveness is checked by the reader.
type ListenLease struct {
	PID       int       `json:"pid"`
	StartTime string    `json:"start_time"`
	Agent     string    `json:"agent,omitempty"`
	ArmedAt   time.Time `json:"armed_at"`
	// RefreshedAt is the waiter's latest keep-alive; zero on a lease that was
	// never refreshed (the arm time then stands in).
	RefreshedAt time.Time `json:"refreshed_at,omitempty"`
}

// ListenLeaseTTL is how long a lease stays live without a refresh. A waiter
// refreshes well inside it, so a lease that stops being refreshed (a hung or
// crashed waiter, or a clock stepped back) expires and can never keep a
// listener, and with it the hub contact, alive forever (MTIX-112.20.4).
const ListenLeaseTTL = 15 * time.Minute

// ListenLeaseRefreshEvery is how often a live waiter refreshes its lease.
const ListenLeaseRefreshEvery = ListenLeaseTTL / 3

// LastSeen is the later of the arm time and the latest refresh.
func (l ListenLease) LastSeen() time.Time {
	if l.RefreshedAt.After(l.ArmedAt) {
		return l.RefreshedAt
	}
	return l.ArmedAt
}

// Expired reports whether the lease has gone unrefreshed for ListenLeaseTTL, or
// is dated further ahead of now than the activity clock skew allows (stale).
func (l ListenLease) Expired(now time.Time) bool {
	seen := l.LastSeen()
	if seen.After(now.Add(5 * time.Minute)) {
		return true
	}
	return now.Sub(seen) >= ListenLeaseTTL
}

// HubContact is the outcome of the most recent scheduled hub contact.
type HubContact struct {
	At    time.Time `json:"at"`
	Route string    `json:"route"`
	OK    bool      `json:"ok"`
}

// BoardKey derives the stable per-board record key from the board's .mtix
// directory path. It is a name, not a secret.
func BoardKey(mtixDir string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(mtixDir)))
	return hex.EncodeToString(sum[:])[:16]
}

func validBoardKey(key string) bool {
	if len(key) != 16 {
		return false
	}
	for _, c := range key {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func hubRecordName(prefix, key string) (string, error) {
	if !validBoardKey(key) {
		return "", invalid("board key is invalid")
	}
	return prefix + "-" + key, nil
}

func validActivityKind(k ActivityKind) bool {
	switch k {
	case ActivityMutation, ActivityAddressedSend, ActivityGlue, ActivityTurn:
		return true
	default:
		return false
	}
}

// RecordHubActivity stores the board's last activity (MTIX-112.20.1).
func (s *State) RecordHubActivity(key string, a HubActivity) error {
	name, err := hubRecordName("hubact", key)
	if err != nil {
		return err
	}
	if !validActivityKind(a.Kind) || a.At.IsZero() {
		return invalid("hub activity needs a known kind and a time")
	}
	return s.WriteJSON(name, a)
}

// ReadHubActivity returns the board's last activity; absent means none.
func (s *State) ReadHubActivity(key string) (HubActivity, bool, error) {
	var a HubActivity
	name, err := hubRecordName("hubact", key)
	if err != nil {
		return a, false, err
	}
	reports, err := s.ReadJSON(name, &a)
	if err != nil || len(reports) != 0 || a.At.IsZero() {
		return HubActivity{}, false, err
	}
	if !validActivityKind(a.Kind) {
		return HubActivity{}, false, invalid("hub activity kind is invalid")
	}
	return a, true, nil
}

// RecordHubContact stores the outcome of the latest scheduled contact.
func (s *State) RecordHubContact(key string, c HubContact) error {
	name, err := hubRecordName("hubcontact", key)
	if err != nil {
		return err
	}
	return s.WriteJSON(name, c)
}

// ReadHubContact returns the latest scheduled contact; absent means none.
func (s *State) ReadHubContact(key string) (HubContact, bool, error) {
	var c HubContact
	name, err := hubRecordName("hubcontact", key)
	if err != nil {
		return c, false, err
	}
	reports, err := s.ReadJSON(name, &c)
	if err != nil || len(reports) != 0 || c.At.IsZero() {
		return HubContact{}, false, err
	}
	return c, true, nil
}

func leaseName(key string, pid int) (string, error) {
	if pid <= 0 {
		return "", invalid("lease pid must be positive")
	}
	return "lease-" + key + "-" + strconv.Itoa(pid), nil
}

// WriteListenLease arms or refreshes one listener lease. A lease write is
// never activity, for a first arm, a refresh or a re-arm by another waiter
// process: it can never move the activity window or the listen-hold clamp
// (session start is recorded by the attach glue instead).
func (s *State) WriteListenLease(key string, l ListenLease) error {
	if !validBoardKey(key) {
		return invalid("board key is invalid")
	}
	name, err := leaseName(key, l.PID)
	if err != nil {
		return err
	}
	if l.StartTime == "" || l.ArmedAt.IsZero() {
		return invalid("lease start time and arm time are required")
	}
	return s.WriteJSON(name, l)
}

// RemoveListenLease deletes one lease; an absent lease is not an error.
func (s *State) RemoveListenLease(key string, pid int) (err error) {
	if !validBoardKey(key) {
		return invalid("board key is invalid")
	}
	name, err := leaseName(key, pid)
	if err != nil {
		return err
	}
	dir, err := s.directory(false)
	if missingState(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer closeWith(dir, &err)
	rmErr := os.Remove(filepath.Join(dir.Name(), name+".json"))
	if rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
		return failure("remove listen lease", rmErr)
	}
	return nil
}

// ListListenLeases returns every readable lease armed on the board.
func (s *State) ListListenLeases(key string) (out []ListenLease, err error) {
	if !validBoardKey(key) {
		return nil, invalid("board key is invalid")
	}
	dir, err := s.directory(false)
	if missingState(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closeWith(dir, &err)
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return nil, failure("list listen leases", err)
	}
	prefix := "lease-" + key + "-"
	for _, e := range entries {
		base, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !strings.HasPrefix(base, prefix) {
			continue
		}
		var l ListenLease
		reports, readErr := s.ReadJSON(base, &l)
		if readErr != nil || len(reports) != 0 || l.PID <= 0 {
			continue // unreadable or foreign lease: not live
		}
		out = append(out, l)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}
