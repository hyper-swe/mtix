// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"io"
	"time"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
)

const (
	fixAddressedPush  = "mtix sync push"
	fixAddressedRelay = "mtix doctor"
	fixPushOnSend     = "mtix config set " + service.PushOnAddressedSendKey + " true"
)

// addressedPushFailureText describes a failed requested push and what to run.
// The message itself is safe: a failed push never loses it, the event stays
// pending and goes with the next push.
func addressedPushFailureText(rec oplocal.AddressedPush) string {
	return fmt.Sprintf("the automatic push of an addressed send failed at %s (%s); the message is still pending. Fix: %s; %s",
		rec.At.UTC().Format(time.RFC3339), rec.ErrorClass, addressedPushFix(rec), addressedPushClears(rec))
}

// addressedPushFix names the next step for a failure class: a relay failure
// is diagnosed by doctor's relay rows, anything else by running the push.
func addressedPushFix(rec oplocal.AddressedPush) string {
	if rec.ErrorClass == relayFailedClass {
		return fixAddressedRelay
	}
	return fixAddressedPush
}

// addressedPushClears says what turns the failure green again.
func addressedPushClears(rec oplocal.AddressedPush) string {
	if rec.ErrorClass == relayFailedClass {
		return "it clears at the next successful relay pass (the daemon, or your next addressed send)"
	}
	return "it clears when a push completes"
}

// showAddressedPushFailure prints a persisted push failure once, on the next
// command after it (ADR-007 D21, MTIX-112.20.2). Best effort: unreadable
// operator state never fails the command, doctor reports it.
func showAddressedPushFailure(stderr io.Writer, mtixDir string) {
	src, err := newHubContactSource(mtixDir)
	if err != nil {
		return
	}
	rec, present, err := src.state.ReadAddressedPush(src.key())
	if err != nil || !present || rec.OK || rec.Shown {
		return
	}
	fmt.Fprintf(stderr, "warning: %s\n", addressedPushFailureText(rec))
	rec.Shown = true
	_ = src.state.RecordAddressedPush(src.key(), rec) // best effort: it only stops a repeat of the warning
}

// addressedPushDoctorRow is the sync.push-on-send row (text and --json): the
// switch, and the latest requested push with the exact fix when it failed.
// It reads local state only and never contacts the hub.
func addressedPushDoctorRow(src *hubContactSource) DoctorCheck {
	row := DoctorCheck{Name: "sync.push-on-send", Pass: true}
	cfg, err := service.NewConfigService(src.configPath())
	if err != nil {
		row.Pass, row.Detail, row.Fix = false, err.Error(), fixPushOnSend
		return row
	}
	on, err := cfg.PushOnAddressedSend()
	if err != nil {
		row.Pass, row.Detail, row.Fix = false, err.Error(), fixPushOnSend
		return row
	}
	if !on {
		row.Detail = service.PushOnAddressedSendKey + " is false: an addressed send does not push by itself"
		return row
	}
	rec, present, err := src.state.ReadAddressedPush(src.key())
	switch {
	case err != nil:
		row.Pass, row.Detail, row.Fix = false, "addressed push state unreadable: "+err.Error(), fixOperatorHome
	case !present:
		row.Detail = "an addressed send pushes the pending events by itself; none has pushed yet"
	case rec.OK:
		row.Detail = "last automatic push of an addressed send succeeded at " + rec.At.UTC().Format(time.RFC3339)
	default:
		row.Pass, row.Detail, row.Fix = false, addressedPushFailureText(rec), addressedPushFix(rec)
	}
	return row
}
