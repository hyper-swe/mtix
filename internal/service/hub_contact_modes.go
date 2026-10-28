// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// DefaultListenHold is the single switch for the Q1 founder decision: when
// true, a live listening session keeps the hub contacted at the listen
// interval until the idle clamp (sync.listen = live). Flip it to false to
// restore strict activity-only contact (sync.listen = off) by default. Tests
// and docs read this constant, so flipping it is a one-constant change.
const DefaultListenHold = true

const (
	// DefaultListenInterval is the contact cadence while the hold is armed.
	DefaultListenInterval = 10 * time.Second
	// DefaultListenMaxIdle is the idle clamp after the last real activity.
	DefaultListenMaxIdle = 8 * time.Hour
	// MaxListenMaxIdle is the largest idle clamp an operator may set.
	MaxListenMaxIdle = 72 * time.Hour
	// MinContactInterval is the smallest operator-set contact period.
	MinContactInterval = time.Second
)

// ErrInvalidHubContact wraps every malformed hub-contact setting.
var ErrInvalidHubContact = fmt.Errorf("invalid hub contact setting: %w", model.ErrInvalidInput)

// HubContactKind is the contact mode of sync.hub_contact.
type HubContactKind int

// Contact modes.
const (
	// HubContactActivity contacts the hub only inside an activity window.
	HubContactActivity HubContactKind = iota + 1
	// HubContactInterval contacts the hub on a fixed period.
	HubContactInterval
	// HubContactManual never contacts the hub from the scheduler.
	HubContactManual
)

// HubContactMode is a parsed sync.hub_contact value.
type HubContactMode struct {
	Kind     HubContactKind
	Interval time.Duration // interval mode only
}

// String renders the mode as its config value.
func (m HubContactMode) String() string {
	switch m.Kind {
	case HubContactInterval:
		return "interval:" + m.Interval.String()
	case HubContactManual:
		return "manual"
	default:
		return "activity"
	}
}

// ParseHubContact parses activity | interval:<dur> | manual; empty is activity.
func ParseHubContact(v string) (HubContactMode, error) {
	switch v {
	case "", "activity":
		return HubContactMode{Kind: HubContactActivity}, nil
	case "manual":
		return HubContactMode{Kind: HubContactManual}, nil
	}
	rest, ok := strings.CutPrefix(v, "interval:")
	if !ok {
		return HubContactMode{}, fmt.Errorf(
			"sync.hub_contact %q: want activity, interval:<duration> or manual: %w", v, ErrInvalidHubContact)
	}
	d, err := time.ParseDuration(rest)
	if err != nil || d < MinContactInterval {
		return HubContactMode{}, fmt.Errorf(
			"sync.hub_contact %q: interval must be a duration of at least 1s: %w", v, ErrInvalidHubContact)
	}
	return HubContactMode{Kind: HubContactInterval, Interval: d}, nil
}

// ListenSettings are the parsed listen-hold settings.
type ListenSettings struct {
	Hold     bool
	Interval time.Duration
	MaxIdle  time.Duration
}

// ParseListen parses sync.listen: live or off; empty is the default.
func ParseListen(v string) (bool, error) {
	switch v {
	case "":
		return DefaultListenHold, nil
	case "live":
		return true, nil
	case "off":
		return false, nil
	}
	return false, fmt.Errorf("sync.listen %q: want live or off: %w", v, ErrInvalidHubContact)
}

// ParseListenInterval parses sync.listen_interval (at least 1s).
func ParseListenInterval(v string) (time.Duration, error) {
	d, err := time.ParseDuration(v)
	if err != nil || d < MinContactInterval {
		return 0, fmt.Errorf("sync.listen_interval %q: want a duration of at least 1s: %w", v, ErrInvalidHubContact)
	}
	return d, nil
}

// ParseListenMaxIdle parses sync.listen_max_idle (positive, at most 72h).
func ParseListenMaxIdle(v string) (time.Duration, error) {
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 || d > MaxListenMaxIdle {
		return 0, fmt.Errorf("sync.listen_max_idle %q: want a duration above 0 and at most 72h: %w", v, ErrInvalidHubContact)
	}
	return d, nil
}

// CostWarner returns the one-time interval-mode cost warning.
type CostWarner struct {
	mu   sync.Mutex
	done bool
}

// NewCostWarner creates a warner that has not warned yet.
func NewCostWarner() *CostWarner { return &CostWarner{} }

// Warn returns the warning text the first time an interval mode is seen,
// and the empty string afterwards or for any other mode.
func (w *CostWarner) Warn(m HubContactMode) string {
	if m.Kind != HubContactInterval {
		return ""
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.done {
		return ""
	}
	w.done = true
	perHour := int(time.Hour / m.Interval)
	return fmt.Sprintf("sync.hub_contact %s contacts the hub about %d times an hour per board, "+
		"all day, so a scale-to-zero database never pauses; use 'mtix config set sync.hub_contact activity' "+
		"to contact it only after activity", m, perHour)
}
