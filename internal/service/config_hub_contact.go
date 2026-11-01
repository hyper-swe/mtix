// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"fmt"
	"time"
)

const hubContactKey = "sync.hub_contact"

// GrandfatherInterval is the daemon cadence used when none is supplied: the
// fixed 5s the daemon always defaulted to.
const GrandfatherInterval = 5 * time.Second

// HubContactGrandfathered reports whether this board predates sync.hub_contact
// and has not set it. `config get` still returns a parseable value (interval
// at the default period) and flags this state separately; the daemon uses its
// own --interval.
func (cs *ConfigService) HubContactGrandfathered() bool { return cs.legacyHubContact }

// DefaultListenValue is the sync.listen value that DefaultListenHold selects;
// docs and tests render the shipped default from it.
func DefaultListenValue() string { return defaultListenValue() }

func defaultListenValue() string {
	if DefaultListenHold {
		return "live"
	}
	return "off"
}

// HubContact returns the effective contact mode (MTIX-112.20.1). A config
// file written before sync.hub_contact existed, and not set since, is
// grandfathered: it keeps contacting the hub at daemonInterval (the daemon's
// own --interval, so an upgrade never raises the contact rate) and reports
// true so doctor can name the command that switches it to activity.
func (cs *ConfigService) HubContact(daemonInterval time.Duration) (HubContactMode, bool, error) {
	if cs.legacyHubContact {
		if daemonInterval < MinContactInterval {
			daemonInterval = GrandfatherInterval
		}
		return HubContactMode{Kind: HubContactInterval, Interval: daemonInterval}, true, nil
	}
	mode, err := ParseHubContact(cs.values[hubContactKey])
	if err != nil {
		return HubContactMode{}, false, fmt.Errorf("read %s: %w", hubContactKey, err)
	}
	return mode, false, nil
}

// ListenSettings returns the parsed listen-hold settings.
func (cs *ConfigService) ListenSettings() (ListenSettings, error) {
	var out ListenSettings
	hold, err := ParseListen(cs.values["sync.listen"])
	if err != nil {
		return out, fmt.Errorf("read sync.listen: %w", err)
	}
	interval, err := ParseListenInterval(cs.values["sync.listen_interval"])
	if err != nil {
		return out, fmt.Errorf("read sync.listen_interval: %w", err)
	}
	maxIdle, err := ParseListenMaxIdle(cs.values["sync.listen_max_idle"])
	if err != nil {
		return out, fmt.Errorf("read sync.listen_max_idle: %w", err)
	}
	return ListenSettings{Hold: hold, Interval: interval, MaxIdle: maxIdle}, nil
}

// ConfigDefault returns the shipped default of a workspace config key, so a
// doctor fix can name the exact value that repairs it.
func ConfigDefault(key string) string { return configDefaults[key] }
