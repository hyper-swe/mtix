// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"fmt"
	"time"

	"github.com/hyper-swe/mtix/internal/service"
)

const (
	fixHubContact       = "mtix config set sync.hub_contact activity"
	fixUnreadableInputs = "run any mtix mutation command (it rewrites the activity record), or fix the operator state directory: " + fixOperatorHome
	fixOperatorHome     = "set XDG_CONFIG_HOME to an operator-owned directory outside every sandbox-writable root, then run: mtix hooks status --json"
)

// hubContactConfigKeys are the listen keys whose bad value stops all hub contact.
var hubContactConfigKeys = []struct {
	key   string
	parse func(string) error
}{
	{"sync.listen", func(v string) error { _, err := service.ParseListen(v); return err }},
	{"sync.listen_interval", func(v string) error { _, err := service.ParseListenInterval(v); return err }},
	{"sync.listen_max_idle", func(v string) error { _, err := service.ParseListenMaxIdle(v); return err }},
}

// badHubContactKey names the first hub-contact key with an unparseable value
// and the exact command that resets it to its shipped default.
func badHubContactKey(cfg *service.ConfigService, daemonInterval time.Duration) (key, fix string, err error) {
	if _, _, modeErr := cfg.HubContact(daemonInterval); modeErr != nil {
		return "sync.hub_contact", "mtix config set sync.hub_contact " + service.ConfigDefault("sync.hub_contact"), modeErr
	}
	for _, k := range hubContactConfigKeys {
		raw, getErr := cfg.Get(k.key)
		if getErr != nil {
			continue
		}
		if parseErr := k.parse(raw); parseErr != nil {
			return k.key, "mtix config set " + k.key + " " + service.ConfigDefault(k.key), parseErr
		}
	}
	return "", "", nil
}

// appendHubContactChecks adds the sync.hub-contact and sync.listen rows
// (MTIX-112.20.1). Both are local reads: doctor never contacts the hub here.
func appendHubContactChecks(report DoctorReport) DoctorReport {
	if app.mtixDir == "" {
		return report
	}
	src, err := newHubContactSource(app.mtixDir)
	if err != nil {
		return appendDoctorCheck(report, DoctorCheck{Name: "sync.hub-contact", Pass: false,
			Detail: err.Error(), Fix: fixOperatorHome})
	}
	for _, c := range hubContactDoctorChecks(src, hubConfigured(app.mtixDir), src.now()) {
		report = appendDoctorCheck(report, c)
	}
	report = appendDoctorCheck(report, listenerDoctorRow(src, hubConfigured(app.mtixDir), relayDir() != "", daemonLockHeld)) // MTIX-112.20.4
	return report
}

// hubContactDoctorChecks builds both rows from config and operator-local
// state. Every failure names the exact command that fixes it.
func hubContactDoctorChecks(src *hubContactSource, hub bool, now time.Time) []DoctorCheck {
	return []DoctorCheck{hubContactRow(src, hub, now), listenRow(src, now)}
}

func hubContactRow(src *hubContactSource, hub bool, now time.Time) DoctorCheck {
	row := DoctorCheck{Name: "sync.hub-contact", Pass: true}
	cfg, err := service.NewConfigService(src.configPath())
	if err != nil {
		row.Pass, row.Detail, row.Fix = false, err.Error(), fixHubContact
		return row
	}
	if key, fix, parseErr := badHubContactKey(cfg, src.grandfatherInterval); key != "" {
		row.Pass, row.Fix = false, fix
		row.Detail = fmt.Sprintf("%s is invalid (%v): no hub contact will happen until it is fixed", key, parseErr)
		return row
	}
	mode, grandfathered, err := cfg.HubContact(src.grandfatherInterval)
	if err != nil {
		row.Pass, row.Detail, row.Fix = false, err.Error(), fixHubContact
		return row
	}
	modeText := mode.String()
	if grandfathered {
		modeText = "interval (grandfathered: the daemon's own --interval)"
	}
	row.Detail = "mode " + modeText + "; " + lastActivityText(src, now) + "; " + lastContactText(src, now)
	if !hub {
		row.Detail += "; no hub configured, so the daemon makes no hub contact"
		return row
	}
	row.Detail += "; " + contactNowText(src, now)
	if _, snapErr := src.snapshot(); snapErr != nil {
		// The daemon makes no hub contact on unreadable inputs, so this is a failure, not a pass.
		row.Pass, row.Fix = false, fixUnreadableInputs
	}
	if grandfathered {
		row.Warn = true
		row.Detail += "; grandfathered: this board predates sync.hub_contact and keeps contacting the hub at the daemon's own interval"
		row.Fix = fixHubContact
	}
	return row
}

// contactNowText says whether the scheduler would contact the hub now, or why not.
func contactNowText(src *hubContactSource, now time.Time) string {
	in, err := src.snapshot()
	if err != nil {
		return "no hub contact will happen: inputs unreadable (" + err.Error() + ")"
	}
	sched := service.NewContactScheduler(func() time.Time { return now },
		func() (service.ContactInputs, error) { return in, nil })
	switch d := sched.Decide(); d.Reason {
	case "manual":
		return "no hub contact will happen: mode is manual"
	case "idle":
		return "no hub contact will happen until new activity (the activity window is closed)"
	default:
		return "hub contact is scheduled (" + d.Reason + ")"
	}
}

func listenRow(src *hubContactSource, now time.Time) DoctorCheck {
	row := DoctorCheck{Name: "sync.listen", Pass: true}
	cfg, err := service.NewConfigService(src.configPath())
	if err != nil {
		row.Pass, row.Detail, row.Fix = false, err.Error(), "mtix config set sync.listen "+service.ConfigDefault("sync.listen")
		return row
	}
	if key, fix, parseErr := badHubContactKey(cfg, src.grandfatherInterval); key != "" {
		row.Pass, row.Fix = false, fix
		row.Detail = fmt.Sprintf("%s is invalid (%v)", key, parseErr)
		return row
	}
	listen, listenErr := cfg.ListenSettings()
	mode, _, modeErr := cfg.HubContact(src.grandfatherInterval)
	if err := errors.Join(listenErr, modeErr); err != nil {
		row.Pass, row.Detail, row.Fix = false, err.Error(), "mtix config set sync.listen "+service.ConfigDefault("sync.listen")
		return row
	}
	if !listen.Hold {
		row.Detail = "listen off: hub contact follows sync.hub_contact only (route poll)"
		return row
	}
	live, leaseErr := src.liveLeases()
	if leaseErr != nil {
		row.Pass, row.Detail, row.Fix = false, leaseErr.Error(), fixOperatorHome
		return row
	}
	row.Detail = fmt.Sprintf("listen live, route poll every %s, idle clamp %s; ", listen.Interval, listen.MaxIdle)
	switch {
	case mode.Kind == service.HubContactManual:
		row.Detail += "inactive: sync.hub_contact is manual, so the hold never contacts the hub"
	case !live:
		row.Detail += "no live waiter lease, no hold (a waiter, mtix inbox --wait, arms a lease and starts the listener)"
	default:
		row.Detail += holdText(src, listen, now)
	}
	return row
}

func holdText(src *hubContactSource, listen service.ListenSettings, now time.Time) string {
	a, present, err := src.state.ReadHubActivity(src.key())
	if err != nil || !present {
		return "no activity recorded, no hold"
	}
	end := a.At.Add(listen.MaxIdle)
	if !now.Before(end) {
		return "hold ended at " + end.Format(time.RFC3339) + " (idle clamp after the last activity)"
	}
	return "hold ends " + end.Format(time.RFC3339) + " (idle clamp after the last activity) unless new activity"
}

func lastActivityText(src *hubContactSource, now time.Time) string {
	a, present, err := src.state.ReadHubActivity(src.key())
	if err != nil {
		return "last activity unreadable (" + err.Error() + ")"
	}
	if !present {
		return "no activity recorded"
	}
	return fmt.Sprintf("last activity %s ago (%s)", now.Sub(a.At).Truncate(time.Second), a.Kind)
}

func lastContactText(src *hubContactSource, now time.Time) string {
	c, present, err := src.state.ReadHubContact(src.key())
	if err != nil {
		return "last contact unreadable (" + err.Error() + ")"
	}
	if !present {
		return "no scheduled contact yet"
	}
	return fmt.Sprintf("last contact %s ago (route %s)", now.Sub(c.At).Truncate(time.Second), c.Route)
}
