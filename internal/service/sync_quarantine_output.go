// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import "github.com/hyper-swe/mtix/internal/store/sqlite"

// QuarantineAcknowledgement is the immutable local receipt exposed by quarantine
// commands. Its JSON fields belong to the service contract (MTIX-187.2.8).
type QuarantineAcknowledgement struct {
	EventID        string `json:"event_id"`
	RawEventSHA256 string `json:"raw_event_sha256"`
	Source         string `json:"source"`
	Actor          string `json:"actor"`
	Reason         string `json:"reason"`
	AcknowledgedAt string `json:"acknowledged_at"`
}

// QuarantineAcknowledgementResult adds per-call repeat metadata to the first
// receipt without changing the retained audit data (MTIX-187.2.8).
type QuarantineAcknowledgementResult struct {
	QuarantineAcknowledgement
	AlreadyAcknowledged bool `json:"already_acknowledged"`
}

func quarantineAcknowledgementOutput(a sqlite.QuarantineAcknowledgement) QuarantineAcknowledgement {
	return QuarantineAcknowledgement{
		EventID: a.EventID, RawEventSHA256: a.RawEventSHA256, Source: a.Source,
		Actor: a.Actor, Reason: a.Reason, AcknowledgedAt: a.AcknowledgedAt,
	}
}
